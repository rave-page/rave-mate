// Package crewlink connects remote capture nodes (crew members' rave-mate instances) to the
// event's mocap master over the rave.page backend relay (CREW_RELAY_CONTRACT.md).
//
// Transport shape mirrors internal/bridge: SSE downstream + POST upstream over stdlib
// net/http, envelope {sid, seq, kind, payload_b64}, opaque base64 payloads ≤256KiB. Unlike the
// account bridge there is NO accept handshake and NO ARQ: mocap relay rooms are event-scoped
// fire-and-forget pub/sub - pose frames tolerate loss by design (the next frame supersedes),
// contract §8. Payload shapes live in wire.go (this repo is their source of truth).
//
// Never log a payload or a token.
package crewlink

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
	"net/url"
	"strconv"
	"strings"
	"time"
)

const logTag = "crewlink"

// Contract-locked limits (CREW_RELAY_CONTRACT.md §3). Exceeding them is the client's bug.
const (
	MaxPayload    = 256 << 10 // decoded bytes per relay frame
	SessionTTL    = 90 * time.Second
	HeartbeatEach = 25 * time.Second

	rateWindow = 10 * time.Second // send-rate window (RateSendPer10s)
)

// Roles (contract §1).
const (
	RoleNode   = "node"
	RoleMaster = "master"
)

// Problem-detail codes we branch on (mirror the bridge codes + the mocap-room additions).
const (
	CodeNotFound               = "NOT_FOUND"
	CodeRateLimited            = "RATE_LIMITED"
	CodeFrameTooLarge          = "FRAME_TOO_LARGE"
	CodeSessionLimit           = "SESSION_LIMIT_REACHED"
	CodeRelayUnknownPeer       = "RELAY_UNKNOWN_PEER"
	CodeNodeBroadcastForbidden = "NODE_BROADCAST_FORBIDDEN"
)

// Errors callers branch on.
var (
	// ErrSessionGone - session expired/revoked server-side → leave the loop and re-join.
	ErrSessionGone = errors.New("crewlink: session no longer exists")
	// ErrUnauthorized - no/invalid bearer. The user is signed out.
	ErrUnauthorized = errors.New("crewlink: unauthorized")
	// ErrRateLimited carries the server's Retry-After.
	ErrRateLimited = errors.New("crewlink: rate limited")
	ErrTooLarge    = errors.New("crewlink: frame too large")
	// ErrUnknownPeer - directed send to a sid the room no longer knows (presence will catch up).
	ErrUnknownPeer = errors.New("crewlink: unknown peer sid")
)

// APIError is a decoded API error body (same live-API shape the bridge decodes).
type APIError struct {
	Status     int
	Code       string
	Detail     string
	TraceID    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("crewlink: %d %s: %s", e.Status, e.Code, e.Detail)
}

// Unwrap maps the server's code onto the sentinel errors for errors.Is.
func (e *APIError) Unwrap() error {
	switch e.Code {
	case CodeNotFound:
		return ErrSessionGone
	case CodeRateLimited:
		return ErrRateLimited
	case CodeFrameTooLarge:
		return ErrTooLarge
	case CodeRelayUnknownPeer:
		return ErrUnknownPeer
	}
	if e.Status == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	return nil
}

// TokenSource yields the account's current access token (satisfied by shared/auth.Manager -
// the same narrow seam bridge and studio use).
type TokenSource interface {
	Token() string
}

// Member is one live room session (join response members[] + presence payloads).
type Member struct {
	SID      string `json:"sid"`
	UserID   string `json:"user_id"`
	Role     string `json:"role"` // "node" | "master"
	Tier     string `json:"tier,omitempty"`
	Label    string `json:"label,omitempty"`
	JoinedAt string `json:"joined_at,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`
}

// Presence is one decoded presence event.
type Presence struct {
	Type   string `json:"type"` // "join" | "leave" | "kick"
	SID    string `json:"sid"`
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	Tier   string `json:"tier,omitempty"`
	Label  string `json:"label,omitempty"`
}

// JoinResult is the joinMocapRoom response.
type JoinResult struct {
	SID        string   `json:"sid"`
	SessionTTL int      `json:"session_ttl_s"`
	Heartbeat  int      `json:"heartbeat_s"`
	Members    []Member `json:"members"`
}

// Client speaks the 5 mocap-room endpoints. Safe for concurrent use.
type Client struct {
	base   string
	tokens TokenSource

	// http serves the short control calls; stream has NO timeout (the SSE downstream lives
	// for hours, bounded by the request context instead) - the bridge two-client pattern.
	http   *http.Client
	stream *http.Client
}

// NewClient builds the relay API client. base is the API root (cfg.APIBaseURL).
func NewClient(base string, tokens TokenSource) *Client {
	return &Client{
		base:   strings.TrimRight(base, "/"),
		tokens: tokens,
		http:   &http.Client{Timeout: 15 * time.Second},
		stream: &http.Client{Timeout: 0},
	}
}

// do issues an authed control request and decodes a problem body on failure.
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

// decodeProblem turns a 4xx/5xx into an APIError (bounded read; live-API error shape - the
// human text is `message`, the machine verdict `details.code`).
func decodeProblem(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	e := &APIError{Status: resp.StatusCode}
	var p struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Title   string `json:"title"`
		TraceID string `json:"trace_id"`
		Details struct {
			Code string `json:"code"`
		} `json:"details"`
	}
	if json.Unmarshal(raw, &p) == nil {
		e.Code = p.Details.Code
		e.TraceID = p.TraceID
		e.Detail = firstNonEmpty(p.Message, p.Detail, p.Title)
	}
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			e.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	if e.RetryAfter == 0 && resp.StatusCode == http.StatusTooManyRequests {
		e.RetryAfter = rateWindow
	}
	return e
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ── the endpoints ────────────────────────────────────────────────────────────

type joinReq struct {
	Role  string `json:"role"`
	Tier  string `json:"tier,omitempty"`
	Label string `json:"label,omitempty"`
}

// Join creates a session in the event's mocap room (`joinMocapRoom`). role=master requires
// event-editor rights server-side; non-crew callers get a BOLA-safe 404. eventID is user-typed
// config: rejected when blank, path-escaped so it can never splice the route.
func (c *Client) Join(ctx context.Context, eventID, role, tier, label string) (JoinResult, error) {
	var out JoinResult
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return out, errors.New("crewlink: event id required (Settings -> Capture crew)")
	}
	err := c.do(ctx, http.MethodPost, "/realtime/mocap/rooms/"+url.PathEscape(eventID)+"/sessions",
		joinReq{Role: role, Tier: tier, Label: label}, &out)
	return out, err
}

// Heartbeat refreshes the session TTL and re-runs the server's crew-check
// (`heartbeatMocapSession`) - the revocation surface: a revoked member 404s here.
func (c *Client) Heartbeat(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodPost, "/realtime/mocap/sessions/"+sid+"/heartbeat", nil, nil)
}

// Leave drops the session (`leaveMocapRoom`); the room sees presence `leave`.
func (c *Client) Leave(ctx context.Context, sid string) error {
	return c.do(ctx, http.MethodDelete, "/realtime/mocap/sessions/"+sid, nil, nil)
}

type sendReq struct {
	SID        string `json:"sid"`
	ToSID      string `json:"to_sid,omitempty"`
	Seq        int64  `json:"seq"`
	PayloadB64 string `json:"payload_b64"`
}

// Send publishes one frame (`sendMocapFrame`). 202 = published, NOT delivered (fire-and-forget
// pub/sub). toSID directs the frame; nodes MUST send directed (role=node broadcast is a
// server-side 403-class problem). Empty toSID = room broadcast (masters only, ctrl).
func (c *Client) Send(ctx context.Context, sid, toSID string, seq int64, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrTooLarge // catch here rather than eat a 413 round trip
	}
	return c.do(ctx, http.MethodPost, "/realtime/mocap/send", sendReq{
		SID: sid, ToSID: toSID, Seq: seq,
		PayloadB64: base64.StdEncoding.EncodeToString(payload),
	}, nil)
}

// ── SSE downstream ───────────────────────────────────────────────────────────

// StreamHandlers are the SSE callbacks. Each runs on the stream goroutine: keep them cheap
// and non-blocking or the stream stalls and the server starts dropping.
type StreamHandlers struct {
	OnHello    func(sid string)
	OnRelay    func(fromSID string, seq int64, payload []byte)
	OnPresence func(Presence)
}

// Stream opens the SSE downstream (`streamMocapRoom`) and pumps events until ctx ends or the
// stream breaks. Auth goes in the Authorization HEADER (never ?token= - it lands in proxy
// logs). Returns ErrSessionGone when the server no longer knows the sid (→ re-join).
func (c *Client) Stream(ctx context.Context, sid string, h StreamHandlers) error {
	tok := c.tokens.Token()
	if tok == "" {
		return ErrUnauthorized
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/realtime/mocap/stream?sid="+sid, nil)
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

// pump parses the SSE wire: `event:` + `data:` lines terminated by a blank line. Comment
// padding and `id:` are ignored (no replay on reconnect; re-joining resyncs presence).
func (c *Client) pump(ctx context.Context, body io.Reader, h StreamHandlers) error {
	br := bufio.NewReaderSize(body, 64<<10)
	var (
		event string
		data  strings.Builder
	)
	// Largest legitimate event: a relay frame (256KiB → ~342KiB base64) + envelope. Past the
	// cap is malformed; refuse rather than grow without limit.
	const maxEvent = 512 << 10

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // clean close → caller re-joins
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "": // blank line terminates an event
			if event != "" && data.Len() > 0 {
				dispatch(event, data.String(), h)
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"): // proxy-buffer-defeat padding
			continue
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data.Len() > maxEvent {
				return errors.New("crewlink: oversized SSE event")
			}
			data.WriteString(strings.TrimPrefix(strings.TrimSpace(line[len("data:"):]), " "))
		}
	}
}

// dispatch decodes one complete SSE event onto the handlers.
func dispatch(event, data string, h StreamHandlers) {
	switch event {
	case "hello":
		var v struct {
			SID string `json:"sid"`
		}
		if json.Unmarshal([]byte(data), &v) == nil && h.OnHello != nil {
			h.OnHello(v.SID)
		}
	case "heartbeat":
		// Server liveness; it refreshes our session TTL. Nothing to do.
	case "presence":
		var p Presence
		if json.Unmarshal([]byte(data), &p) == nil && h.OnPresence != nil {
			h.OnPresence(p)
		}
	case "relay":
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
		if h.OnRelay != nil {
			h.OnRelay(v.SID, v.Seq, payload)
		}
	}
}
