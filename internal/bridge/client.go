// Package bridge reaches rave-mate instances you are NOT sitting at, through the user's
// rave.page account.
//
// rave.page is a cryptographically BLIND rendezvous/relay: it knows which of the account's
// devices are online and shuttles opaque blobs between them. It is NOT the trust root and NOT
// a party to any security decision - see internal/authz. Everything of substance is end-to-end:
//
//	peerlink AKE   ECDH + Ed25519-signed transcript (the frames are public key material;
//	               safe in the clear, exactly as on the LAN ws:// transport)
//	AEAD upgrade   the transport switches to AES-256-GCM keyed from the handshake secret, so
//	               every subsequent byte - the authz credential, remote-control commands, the
//	               RemoteUI Library stream - is ciphertext to the relay. Without this the
//	               "blind server" property would be worthless: peerlink's data plane is
//	               plaintext + HMAC (fine on a LAN, not across a third party).
//	authz gate     TOTP bootstrap → pinned identities → pairwise token.
//
// Transport shape (see the backend contract): SSE downstream, POST upstream, envelope
// {sid, seq, kind, payload_b64}. The relay is FIRE-AND-FORGET (Redis pub/sub): a 202 means
// "published", NOT "delivered". Conn therefore runs its own ARQ - sequence, cumulative ack,
// retransmit - on top. Nothing here may assume the server delivered anything.
//
// Never log a payload, a token, or a code.
package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/logbus"
)

const logTag = "bridge"

// Caps mirrored from the backend contract. Exceeding them is the client's bug, not the
// server's - we pace and chunk to stay inside.
const (
	MaxPayload    = 256 << 10 // decoded bytes per relay frame (server → 413)
	SessionTTL    = 90 * time.Second
	HeartbeatEach = 25 * time.Second // < the 30s advisory, so a slow round trip can't expire us

	// Rate ceilings (per session, per 10s window). We pace well under them: a 429 costs a
	// Retry-After stall that would stutter the whole link.
	relayPerWindow  = 600
	signalPerWindow = 30
	rateWindow      = 10 * time.Second
)

// Capabilities advertised at registration, so the far end knows what this device serves.
const (
	CapLocalStudio = "bridge.localStudio" // serves the internal/studio protocol over the relay
	CapPeerlink    = "peerlink.wan"       // serves the peerlink AKE + data channels over the relay
)

// Frame kinds.
const (
	KindSignal   = "signal"   // handshake/rendezvous plane - no mutual accept needed
	KindRelay    = "relay"    // data plane - requires mutual accept (else 403)
	KindPresence = "presence" // server-generated; downstream only
)

// Problem-detail codes we branch on.
const (
	CodeRelayNotAccepted = "RELAY_NOT_ACCEPTED"
	CodeNotFound         = "NOT_FOUND"
	CodeRateLimited      = "RATE_LIMITED"
	CodeFrameTooLarge    = "FRAME_TOO_LARGE"
	CodeSessionLimit     = "SESSION_LIMIT_REACHED"
	CodeLinkLimit        = "LINK_LIMIT_REACHED"
)

// Errors callers branch on.
var (
	// ErrNotAccepted - relay before the peer accepted us. Transient: the ARQ retries.
	ErrNotAccepted = errors.New("bridge: relay not accepted by peer yet")
	// ErrSessionGone - our session expired or was deregistered → re-register + reconnect.
	ErrSessionGone = errors.New("bridge: session no longer exists")
	// ErrUnauthorized - no/invalid bearer. The user is signed out.
	ErrUnauthorized = errors.New("bridge: unauthorized")
	// ErrRateLimited carries the server's Retry-After.
	ErrRateLimited = errors.New("bridge: rate limited")
	ErrTooLarge    = errors.New("bridge: frame too large")
)

// APIError is a decoded RFC7807 problem detail.
type APIError struct {
	Status     int
	Code       string
	Detail     string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bridge: %d %s: %s", e.Status, e.Code, e.Detail)
}

// Unwrap maps the server's code onto the sentinel errors so callers can errors.Is on them.
func (e *APIError) Unwrap() error {
	switch e.Code {
	case CodeRelayNotAccepted:
		return ErrNotAccepted
	case CodeNotFound:
		return ErrSessionGone
	case CodeRateLimited:
		return ErrRateLimited
	case CodeFrameTooLarge:
		return ErrTooLarge
	}
	if e.Status == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	return nil
}

// TokenSource yields the account's current access token. Satisfied by shared/auth.Manager -
// the same narrow seam internal/studio uses.
type TokenSource interface {
	Token() string
}

// Session is a registered device session on the account.
type Session struct {
	SID          string   `json:"sid"`
	NodeID       string   `json:"node_id"`
	DisplayName  string   `json:"display_name"`
	Capabilities []string `json:"capabilities"`
	ConnectedAt  string   `json:"connected_at"`
}

// Has reports whether the session advertises a capability.
func (s Session) Has(cap string) bool {
	for _, c := range s.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// Frame is one relay/signal/presence envelope. Payload is the DECODED bytes - opaque to the
// server and to this layer alike.
type Frame struct {
	SID     string // the SENDER's sid
	Seq     int64
	Kind    string
	Payload []byte
}

// Client speaks the 7 bridge endpoints. Safe for concurrent use.
type Client struct {
	base   string
	tokens TokenSource
	log    *logbus.Bus

	// http is for the short control calls. Separate from stream (below): a 15s timeout would
	// guillotine a long-lived SSE connection.
	http *http.Client
	// stream has NO client timeout - the SSE downstream is meant to stay open for hours. Its
	// lifetime is bounded by the request context and by read deadlines instead.
	stream *http.Client
}

// NewClient builds the bridge API client. base is the API root (cfg.APIBaseURL).
func NewClient(base string, tokens TokenSource, log *logbus.Bus) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		tokens: tokens,
		log:    log,
		http:   &http.Client{Timeout: 15 * time.Second},
		stream: &http.Client{Timeout: 0},
	}
}

// do issues an authed control request and decodes an RFC7807 problem on failure.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	tok := c.tokens.Token()
	if tok == "" {
		return ErrUnauthorized
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return decodeProblem(resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused
	return nil
}

// decodeProblem turns a 4xx/5xx into an APIError. The body is a problem detail; we read a
// bounded prefix (a hostile/bugged upstream must not stream us out of memory).
func decodeProblem(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	e := &APIError{Status: resp.StatusCode}
	var p struct {
		Detail  string `json:"detail"`
		Title   string `json:"title"`
		Details struct {
			Code string `json:"code"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &p) == nil {
		e.Code, e.Detail = p.Details.Code, p.Detail
		if e.Detail == "" {
			e.Detail = p.Title
		}
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	if e.RetryAfter == 0 && resp.StatusCode == http.StatusTooManyRequests {
		e.RetryAfter = rateWindow // the contract's window, when the header is missing
	}
	return e
}

// ── the 7 endpoints ──────────────────────────────────────────────────────────

type registerReq struct {
	NodeID       string   `json:"node_id"`
	DisplayName  string   `json:"display_name,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type registerResp struct {
	Session   Session `json:"session"`
	TTL       int     `json:"ttl_seconds"`
	Heartbeat int     `json:"heartbeat_seconds"`
}

// Register creates this device's session. Rate: 12/min per account.
func (c *Client) Register(ctx context.Context, nodeID, displayName string, caps []string) (Session, error) {
	var out registerResp
	err := c.do(ctx, http.MethodPost, "/realtime/bridge/sessions",
		registerReq{NodeID: nodeID, DisplayName: displayName, Capabilities: caps}, &out)
	return out.Session, err
}

// ListSessions returns the account's online sessions. The contract gives no replay on SSE
// reconnect, so this is how presence is re-synced after a stream drop.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	var out struct {
		Sessions []Session `json:"sessions"`
	}
	err := c.do(ctx, http.MethodGet, "/realtime/bridge/sessions", nil, &out)
	return out.Sessions, err
}

// Heartbeat refreshes presence TTL. Not needed while a stream is open (the server's 15s
// stream heartbeat refreshes it), but kept for the pre-stream window and as a health probe.
// A 404 here means the session is gone → re-register.
func (c *Client) Heartbeat(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodPost, "/realtime/bridge/sessions/"+sid+"/heartbeat", nil, nil)
}

// Deregister drops the session and emits presence offline.
func (c *Client) Deregister(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodDelete, "/realtime/bridge/sessions/"+sid, nil, nil)
}

// Accept marks sid→peerSid accepted for RELAY frames. Relay flows only after BOTH directions
// are accepted; marks expire after 5 idle minutes and are refreshed by relay traffic.
func (c *Client) Accept(ctx context.Context, sid, peerSid string) error {
	return c.do(ctx, http.MethodPost, "/realtime/bridge/sessions/"+sid+"/accept",
		map[string]string{"peer_sid": peerSid}, nil)
}

type sendReq struct {
	SID        string `json:"sid"`
	ToSID      string `json:"to_sid"`
	Seq        int64  `json:"seq"`
	Kind       string `json:"kind"`
	PayloadB64 string `json:"payload_b64"`
}

// Send publishes one frame. 202 = published, NOT delivered - the caller is responsible for
// end-to-end confirmation and retransmission (see Conn's ARQ).
func (c *Client) Send(ctx context.Context, sid, toSID string, seq int64, kind string, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrTooLarge // catch it here rather than eat a 413 round trip
	}
	return c.do(ctx, http.MethodPost, "/realtime/bridge/send", sendReq{
		SID: sid, ToSID: toSID, Seq: seq, Kind: kind,
		PayloadB64: base64.StdEncoding.EncodeToString(payload),
	}, nil)
}

// ── SSE downstream ───────────────────────────────────────────────────────────

// PresenceEvent is the decoded payload of a presence frame.
type PresenceEvent struct {
	Event   string  `json:"event"` // "online" | "offline"
	Session Session `json:"session"`
}

// StreamHandlers are the SSE callbacks. Each runs on the stream goroutine: keep them cheap
// and non-blocking, or the stream stalls and the server starts dropping (200-frame buffer).
type StreamHandlers struct {
	OnHello    func(sid string)
	OnFrame    func(Frame)         // signal + relay
	OnPresence func(PresenceEvent) // decoded presence
}

// Stream opens the SSE downstream for a session and pumps frames until ctx ends or the stream
// breaks. Returns ErrSessionGone when the server no longer knows the sid (→ re-register).
//
// Auth goes in the Authorization HEADER, not the ?token= query the browser must use: a token
// in a URL lands in proxy and access logs. The contract allows both; we take the safe one.
func (c *Client) Stream(ctx context.Context, sid string, h StreamHandlers) error {
	tok := c.tokens.Token()
	if tok == "" {
		return ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/realtime/bridge/stream?sid="+sid, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := c.stream.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return decodeProblem(resp)
	}
	return c.pump(ctx, resp.Body, h)
}

// pump parses the SSE wire: `event:` + `data:` lines terminated by a blank line. Comment lines
// (the server's ~2KB anti-buffering pad) and `id:` are ignored - the contract says Last-Event-ID
// is not honoured, so there is nothing to resume from.
func (c *Client) pump(ctx context.Context, body io.Reader, h StreamHandlers) error {
	br := bufio.NewReaderSize(body, 64<<10)
	var (
		event string
		data  strings.Builder
	)
	// A single SSE event is bounded: the largest legitimate one is a relay frame
	// (256KiB → ~342KiB base64) plus envelope. Anything past the cap is malformed; refuse it
	// rather than grow without limit.
	const maxEvent = 512 << 10

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // clean close → caller reconnects
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "": // blank line terminates an event
			if event != "" && data.Len() > 0 {
				c.dispatch(event, data.String(), h)
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"): // proxy-buffer-defeat padding
			continue
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data.Len() > maxEvent {
				return errors.New("bridge: oversized SSE event")
			}
			data.WriteString(strings.TrimPrefix(strings.TrimSpace(line[len("data:"):]), " "))
		}
	}
}

// dispatch decodes one complete SSE event onto the handlers.
func (c *Client) dispatch(event, data string, h StreamHandlers) {
	switch event {
	case "hello":
		var v struct {
			SID string `json:"sid"`
		}
		if json.Unmarshal([]byte(data), &v) == nil && h.OnHello != nil {
			h.OnHello(v.SID)
		}
	case "heartbeat":
		// Server liveness only; it also refreshes our session TTL. Nothing to do.
	case KindPresence, KindSignal, KindRelay:
		var v struct {
			SID        string `json:"sid"`
			Seq        int64  `json:"seq"`
			Kind       string `json:"kind"`
			PayloadB64 string `json:"payload_b64"`
		}
		if json.Unmarshal([]byte(data), &v) != nil {
			return
		}
		payload, err := base64.StdEncoding.DecodeString(v.PayloadB64)
		if err != nil {
			return
		}
		if event == KindPresence {
			var pe PresenceEvent
			if json.Unmarshal(payload, &pe) == nil && h.OnPresence != nil {
				h.OnPresence(pe)
			}
			return
		}
		if h.OnFrame != nil {
			h.OnFrame(Frame{SID: v.SID, Seq: v.Seq, Kind: v.Kind, Payload: payload})
		}
	}
}
