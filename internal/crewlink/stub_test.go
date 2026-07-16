package crewlink

// stub_test.go - a minimal in-test relay implementing the mocap-room contract surface the
// client uses (join/heartbeat/leave/send/stream), backed by in-memory rooms. Mirrors the
// server semantics that matter: sid mint, members[] on join, presence events, directed +
// broadcast relay, 404 problem bodies for dead sessions.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

type stubSession struct {
	sid    string
	room   string
	role   string
	tier   string
	label  string
	events chan string // pre-rendered SSE blocks
	gone   bool        // heartbeat/send/stream 404
}

type stubRelay struct {
	mu      sync.Mutex
	nextSID int
	rooms   map[string]map[string]*stubSession // eventID → sid → session
	bySID   map[string]*stubSession
	joins   map[string]int // role → join count
}

func newStubRelay() *stubRelay {
	return &stubRelay{
		rooms: map[string]map[string]*stubSession{},
		bySID: map[string]*stubSession{},
		joins: map[string]int{},
	}
}

func problem(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"status":"error","message":%q,"details":{"code":%q}}`, code, code)
}

func sseBlock(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// deliver queues one SSE block; non-blocking (a stalled consumer drops, like the real relay).
func (s *stubSession) deliver(block string) {
	select {
	case s.events <- block:
	default:
	}
}

func (r *stubRelay) member(s *stubSession) Member {
	return Member{SID: s.sid, UserID: "u-" + s.sid, Role: s.role, Tier: s.tier, Label: s.label}
}

// broadcastPresence sends a presence event to every session in the room except skipSID.
func (r *stubRelay) broadcastPresence(room, typ string, about *stubSession, skipSID string) {
	p := Presence{Type: typ, SID: about.sid, UserID: "u-" + about.sid, Role: about.role, Tier: about.tier, Label: about.label}
	b, _ := json.Marshal(p)
	for sid, sess := range r.rooms[room] {
		if sid == skipSID {
			continue
		}
		sess.deliver(sseBlock("presence", string(b)))
	}
}

// kick drops a session server-side: presence kick to the whole room INCLUDING the victim,
// then the session 404s everywhere (the revoked-member path, contract §8).
func (r *stubRelay) kick(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sess := r.bySID[sid]
	if sess == nil {
		return
	}
	sess.gone = true
	r.broadcastPresence(sess.room, "kick", sess, "")
	sess.deliver(sseBlock("presence", mustJSON(Presence{Type: "kick", SID: sess.sid, UserID: "u-" + sess.sid, Role: sess.role})))
	delete(r.rooms[sess.room], sid)
}

// relayFrom queues a raw relay frame to a target session as if fromSID had sent it (test hook
// for master-issued ctrl frames without a live master).
func (r *stubRelay) relayFrom(fromSID, toSID string, payload []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	target := r.bySID[toSID]
	if target == nil || target.gone {
		return
	}
	target.deliver(sseBlock("relay", mustJSON(map[string]any{
		"sid": fromSID, "seq": 0, "kind": "relay", "payload_b64": b64(payload),
	})))
}

func (r *stubRelay) joinCount(role string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.joins[role]
}

func (r *stubRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
		problem(w, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	path := req.URL.Path
	switch {
	case req.Method == http.MethodPost && strings.HasPrefix(path, "/realtime/mocap/rooms/") && strings.HasSuffix(path, "/sessions"):
		r.handleJoin(w, req, strings.TrimSuffix(strings.TrimPrefix(path, "/realtime/mocap/rooms/"), "/sessions"))
	case req.Method == http.MethodPost && strings.HasPrefix(path, "/realtime/mocap/sessions/") && strings.HasSuffix(path, "/heartbeat"):
		r.handleHeartbeat(w, strings.TrimSuffix(strings.TrimPrefix(path, "/realtime/mocap/sessions/"), "/heartbeat"))
	case req.Method == http.MethodDelete && strings.HasPrefix(path, "/realtime/mocap/sessions/"):
		r.handleLeave(w, strings.TrimPrefix(path, "/realtime/mocap/sessions/"))
	case req.Method == http.MethodPost && path == "/realtime/mocap/send":
		r.handleSend(w, req)
	case req.Method == http.MethodGet && path == "/realtime/mocap/stream":
		r.handleStream(w, req)
	default:
		problem(w, http.StatusNotFound, CodeNotFound)
	}
}

func (r *stubRelay) handleJoin(w http.ResponseWriter, req *http.Request, room string) {
	var body struct {
		Role  string `json:"role"`
		Tier  string `json:"tier"`
		Label string `json:"label"`
	}
	if json.NewDecoder(req.Body).Decode(&body) != nil || (body.Role != RoleNode && body.Role != RoleMaster) {
		problem(w, http.StatusUnprocessableEntity, "VALIDATION")
		return
	}
	r.mu.Lock()
	r.nextSID++
	sess := &stubSession{
		sid: fmt.Sprintf("mses_%08x", r.nextSID), room: room,
		role: body.Role, tier: body.Tier, label: body.Label,
		events: make(chan string, 256),
	}
	if r.rooms[room] == nil {
		r.rooms[room] = map[string]*stubSession{}
	}
	members := make([]Member, 0, len(r.rooms[room])+1)
	for _, other := range r.rooms[room] {
		members = append(members, r.member(other))
	}
	r.rooms[room][sess.sid] = sess
	r.bySID[sess.sid] = sess
	members = append(members, r.member(sess))
	r.joins[body.Role]++
	r.broadcastPresence(room, "join", sess, sess.sid)
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(JoinResult{SID: sess.sid, SessionTTL: 90, Heartbeat: 25, Members: members})
}

func (r *stubRelay) handleHeartbeat(w http.ResponseWriter, sid string) {
	r.mu.Lock()
	sess := r.bySID[sid]
	gone := sess == nil || sess.gone
	r.mu.Unlock()
	if gone {
		problem(w, http.StatusNotFound, CodeNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *stubRelay) handleLeave(w http.ResponseWriter, sid string) {
	r.mu.Lock()
	sess := r.bySID[sid]
	if sess != nil && !sess.gone {
		sess.gone = true
		delete(r.rooms[sess.room], sid)
		r.broadcastPresence(sess.room, "leave", sess, "")
	}
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *stubRelay) handleSend(w http.ResponseWriter, req *http.Request) {
	var body struct {
		SID        string `json:"sid"`
		ToSID      string `json:"to_sid"`
		Seq        int64  `json:"seq"`
		PayloadB64 string `json:"payload_b64"`
	}
	if json.NewDecoder(req.Body).Decode(&body) != nil {
		problem(w, http.StatusUnprocessableEntity, "VALIDATION")
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sender := r.bySID[body.SID]
	if sender == nil || sender.gone {
		problem(w, http.StatusNotFound, CodeNotFound)
		return
	}
	if body.ToSID == "" && sender.role == RoleNode {
		problem(w, http.StatusForbidden, CodeNodeBroadcastForbidden)
		return
	}
	block := sseBlock("relay", mustJSON(map[string]any{
		"sid": body.SID, "seq": body.Seq, "kind": "relay", "payload_b64": body.PayloadB64,
	}))
	if body.ToSID != "" {
		target := r.bySID[body.ToSID]
		if target == nil || target.gone || target.room != sender.room {
			problem(w, http.StatusNotFound, CodeRelayUnknownPeer)
			return
		}
		target.deliver(block)
	} else {
		for sid, sess := range r.rooms[sender.room] {
			if sid != body.SID {
				sess.deliver(block)
			}
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (r *stubRelay) handleStream(w http.ResponseWriter, req *http.Request) {
	sid := req.URL.Query().Get("sid")
	r.mu.Lock()
	sess := r.bySID[sid]
	gone := sess == nil || sess.gone
	r.mu.Unlock()
	if gone {
		problem(w, http.StatusNotFound, CodeNotFound)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		problem(w, http.StatusInternalServerError, "NO_FLUSH")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, sseBlock("hello", `{"sid":"`+sid+`"}`))
	fl.Flush()
	for {
		select {
		case <-req.Context().Done():
			return
		case block := <-sess.events:
			if _, err := fmt.Fprint(w, block); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
