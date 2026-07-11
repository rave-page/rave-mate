package bridge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBridge implements the documented backend contract in-process, INCLUDING the properties
// that make it hostile to a naive client:
//
//   - fire-and-forget: it can drop published frames (Redis pub/sub loss model). A 202 is
//     "published", never "delivered".
//   - relay stays 403 RELAY_NOT_ACCEPTED until BOTH directions have accepted.
//   - 413 over the payload cap, 429 with Retry-After over the rate ceiling.
//   - 404 (never 403) for a foreign/unknown sid - BOLA-safe.
//
// It is the only way to exercise the full path: a real end-to-end WAN test needs a signed-in
// rave.page account, which the isolated test instances deliberately do not have.
type fakeBridge struct {
	t *testing.T

	mu        sync.Mutex
	sessions  map[string]Session
	accepts   map[string]bool        // "from→to"
	streams   map[string]chan []byte // sid → SSE event bodies
	sent      [][]byte               // every payload the server saw (to assert it's ciphertext)
	seq       int
	dropEvery int  // drop every Nth published frame; 0 = lossless
	rateLimit bool // next send → 429
	nextSID   int

	srv *httptest.Server
}

func newFakeBridge(t *testing.T) *fakeBridge {
	f := &fakeBridge{
		t:        t,
		sessions: map[string]Session{},
		accepts:  map[string]bool{},
		streams:  map[string]chan []byte{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/realtime/bridge/sessions", f.handleSessions)
	mux.HandleFunc("/realtime/bridge/sessions/", f.handleSessionSub)
	mux.HandleFunc("/realtime/bridge/send", f.handleSend)
	mux.HandleFunc("/realtime/bridge/stream", f.handleStream)
	f.srv = httptest.NewServer(f.authed(mux))
	t.Cleanup(f.srv.Close)
	return f
}

// authed enforces the contract's bearer requirement (header, or ?token= for EventSource).
func (f *fakeBridge) authed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if tok == "" {
			f.problem(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing bearer")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (f *fakeBridge) problem(w http.ResponseWriter, status int, code, detail string) {
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "10")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"title": code, "detail": detail, "status": status,
		"details": map[string]string{"code": code},
	})
}

func (f *fakeBridge) handleSessions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req registerReq
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.NodeID == "" {
			f.problem(w, http.StatusBadRequest, "VALIDATION_FAILED", "node_id required")
			return
		}
		f.mu.Lock()
		f.nextSID++
		sid := fmt.Sprintf("bses_%032x", f.nextSID)
		s := Session{
			SID: sid, NodeID: req.NodeID, DisplayName: req.DisplayName,
			Capabilities: req.Capabilities, ConnectedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		f.sessions[sid] = s
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(registerResp{Session: s, TTL: 90, Heartbeat: 30})
	case http.MethodGet:
		f.mu.Lock()
		out := make([]Session, 0, len(f.sessions))
		for _, s := range f.sessions {
			out = append(out, s)
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": out})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSessionSub routes /sessions/{sid}, /sessions/{sid}/heartbeat, /sessions/{sid}/accept.
func (f *fakeBridge) handleSessionSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/realtime/bridge/sessions/")
	sid, action, _ := strings.Cut(rest, "/")

	f.mu.Lock()
	_, known := f.sessions[sid]
	f.mu.Unlock()
	if !known {
		f.problem(w, http.StatusNotFound, "NOT_FOUND", "no such session") // never 403 - BOLA-safe
		return
	}

	switch {
	case action == "heartbeat" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]int{"ttl_seconds": 90})
	case action == "accept" && r.Method == http.MethodPost:
		var body struct {
			PeerSID string `json:"peer_sid"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.PeerSID == "" {
			f.problem(w, http.StatusBadRequest, "VALIDATION_FAILED", "peer_sid required")
			return
		}
		f.mu.Lock()
		_, peerKnown := f.sessions[body.PeerSID]
		if peerKnown {
			f.accepts[sid+"→"+body.PeerSID] = true
		}
		f.mu.Unlock()
		if !peerKnown {
			f.problem(w, http.StatusNotFound, "NOT_FOUND", "no such peer")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case action == "" && r.Method == http.MethodDelete:
		f.mu.Lock()
		delete(f.sessions, sid)
		if ch, ok := f.streams[sid]; ok {
			close(ch)
			delete(f.streams, sid)
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeBridge) handleSend(w http.ResponseWriter, r *http.Request) {
	var req sendReq
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		f.problem(w, http.StatusBadRequest, "VALIDATION_FAILED", "bad json")
		return
	}
	payload, err := base64.StdEncoding.DecodeString(req.PayloadB64)
	if err != nil {
		f.problem(w, http.StatusBadRequest, "VALIDATION_FAILED", "bad base64")
		return
	}
	if len(payload) > MaxPayload {
		f.problem(w, http.StatusRequestEntityTooLarge, CodeFrameTooLarge, "payload over cap")
		return
	}
	if req.Kind != KindSignal && req.Kind != KindRelay {
		f.problem(w, http.StatusBadRequest, "VALIDATION_FAILED", "bad kind")
		return
	}

	f.mu.Lock()
	if f.rateLimit {
		f.mu.Unlock()
		f.problem(w, http.StatusTooManyRequests, CodeRateLimited, "slow down")
		return
	}
	_, senderOK := f.sessions[req.SID]
	_, targetOK := f.sessions[req.ToSID]
	if !senderOK || !targetOK {
		f.mu.Unlock()
		f.problem(w, http.StatusNotFound, CodeNotFound, "unknown session")
		return
	}
	// Relay requires MUTUAL accept. Signal never does.
	if req.Kind == KindRelay {
		if !f.accepts[req.SID+"→"+req.ToSID] || !f.accepts[req.ToSID+"→"+req.SID] {
			f.mu.Unlock()
			f.problem(w, http.StatusForbidden, CodeRelayNotAccepted, "not mutually accepted")
			return
		}
	}
	f.sent = append(f.sent, payload)
	f.seq++
	drop := f.dropEvery > 0 && f.seq%f.dropEvery == 0
	ch := f.streams[req.ToSID]
	f.mu.Unlock()

	// 202 = PUBLISHED. Whether it arrives is another matter entirely - that's the whole
	// fire-and-forget hazard the client's ARQ exists to absorb.
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})

	if drop || ch == nil {
		return
	}
	event := fmt.Sprintf("event: %s\ndata: %s\n\n", req.Kind, mustJSON(map[string]any{
		"sid": req.SID, "seq": req.Seq, "kind": req.Kind, "payload_b64": req.PayloadB64,
	}))
	select {
	case ch <- []byte(event):
	default: // slow consumer → server drops (contract: 200-frame buffer, drop-on-full)
	}
}

func (f *fakeBridge) handleStream(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	f.mu.Lock()
	_, ok := f.sessions[sid]
	if !ok {
		f.mu.Unlock()
		f.problem(w, http.StatusNotFound, CodeNotFound, "no such session")
		return
	}
	ch := make(chan []byte, 256)
	f.streams[sid] = ch
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)

	// The ~2KB anti-buffering pad + hello, exactly as the contract describes.
	_, _ = fmt.Fprintf(w, ":%s\n\n", strings.Repeat("p", 2048))
	_, _ = fmt.Fprintf(w, "event: hello\ndata: %s\n\n", mustJSON(map[string]string{"type": "hello", "sid": sid}))
	if fl != nil {
		fl.Flush()
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			_, _ = w.Write(ev)
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

// setLoss makes the server drop every Nth published frame.
func (f *fakeBridge) setLoss(everyNth int) {
	f.mu.Lock()
	f.dropEvery = everyNth
	f.mu.Unlock()
}

func (f *fakeBridge) setRateLimit(on bool) {
	f.mu.Lock()
	f.rateLimit = on
	f.mu.Unlock()
}

// payloads returns every payload the server saw - used to prove it only ever held ciphertext.
func (f *fakeBridge) payloads() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte{}, f.sent...)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// staticToken is a TokenSource for tests.
type staticToken string

func (s staticToken) Token() string { return string(s) }
