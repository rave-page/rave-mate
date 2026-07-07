// Package obs is an obs-websocket v5 client over github.com/coder/websocket.
// Connect performs the v5 handshake; Request correlates op:6/op:7 by requestId;
// a background read loop routes responses to per-request channels and fans op:5
// events out to SubscribeEvents subscribers.
package obs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// ── OBS-WS v5 op codes ──────────────────────────────────────────────────────

const (
	opHello      = 0
	opIdentify   = 1
	opIdentified = 2
	opRequest    = 6
	opResponse   = 7
	opEvent      = 5
)

// envelope is the outer op/d wrapper used for every frame.
type envelope struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
}

// helloData is op:0 Hello payload.
type helloData struct {
	OBSWebSocketVersion string      `json:"obsWebSocketVersion"`
	RPCVersion          int         `json:"rpcVersion"`
	Authentication      *authParams `json:"authentication,omitempty"`
}

type authParams struct {
	Challenge string `json:"challenge"`
	Salt      string `json:"salt"`
}

// identifyData is op:1 Identify payload.
type identifyData struct {
	RPCVersion     int    `json:"rpcVersion"`
	Authentication string `json:"authentication,omitempty"`
}

// requestFrame is the op:6 Request payload.
type requestFrame struct {
	RequestType string `json:"requestType"`
	RequestID   string `json:"requestId"`
	RequestData any    `json:"requestData,omitempty"`
}

// responseFrame is the op:7 RequestResponse payload.
type responseFrame struct {
	RequestType   string          `json:"requestType"`
	RequestID     string          `json:"requestId"`
	RequestStatus requestStatus   `json:"requestStatus"`
	ResponseData  json.RawMessage `json:"responseData,omitempty"`
}

type requestStatus struct {
	Result  bool   `json:"result"`
	Code    int    `json:"code"`
	Comment string `json:"comment,omitempty"`
}

// eventFrame is the op:5 Event payload.
type eventFrame struct {
	EventType string          `json:"eventType"`
	EventData json.RawMessage `json:"eventData,omitempty"`
}

// Event is one obs-websocket op:5 event delivered to subscribers.
type Event struct {
	Type string
	Data json.RawMessage
}

// Client is a connected obs-websocket v5 session.
type Client struct {
	ws      *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	seq     atomic.Int64
	mu      sync.Mutex
	waiters map[string]chan responseFrame
	subs    map[int]chan Event
	nextSub int
}

// authString computes the obs-websocket v5 authentication string.
// secret = base64(sha256(password + salt))
// auth   = base64(sha256(secret + challenge))
func authString(password, salt, challenge string) string {
	h := sha256.New()
	h.Write([]byte(password + salt))
	secret := base64.StdEncoding.EncodeToString(h.Sum(nil))

	h.Reset()
	h.Write([]byte(secret + challenge))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// Connect dials the obs-websocket server and performs the v5 handshake.
func Connect(ctx context.Context, host string, port int, password string) (*Client, error) {
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("%s:%d", host, port)}
	ws, _, err := websocket.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("obs dial: %w", err)
	}
	ws.SetReadLimit(4 << 20) // 4 MiB

	// receive op:0 Hello
	var hello envelope
	if err := readJSON(ctx, ws, &hello); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("obs hello recv: %w", err)
	}
	if hello.Op != opHello {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("obs: expected op %d Hello, got %d", opHello, hello.Op)
	}
	var hd helloData
	if err := json.Unmarshal(hello.Data, &hd); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("obs hello decode: %w", err)
	}

	// build op:1 Identify
	id := identifyData{RPCVersion: 1}
	if hd.Authentication != nil {
		if password == "" {
			_ = ws.CloseNow()
			return nil, fmt.Errorf("obs: server requires authentication but no password supplied")
		}
		id.Authentication = authString(password, hd.Authentication.Salt, hd.Authentication.Challenge)
	}
	if err := writeJSON(ctx, ws, envelope{Op: opIdentify, Data: mustMarshal(id)}); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("obs identify send: %w", err)
	}

	// receive op:2 Identified
	var ided envelope
	if err := readJSON(ctx, ws, &ided); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("obs identified recv: %w", err)
	}
	if ided.Op != opIdentified {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("obs: expected op %d Identified, got %d (auth failure?)", opIdentified, ided.Op)
	}

	cCtx, cancel := context.WithCancel(ctx)
	c := &Client{
		ws:      ws,
		ctx:     cCtx,
		cancel:  cancel,
		waiters: make(map[string]chan responseFrame),
		subs:    make(map[int]chan Event),
	}
	go c.readLoop()
	return c, nil
}

// Done is closed when the connection drops (read error or Close).
func (c *Client) Done() <-chan struct{} { return c.ctx.Done() }

// SubscribeEvents streams op:5 events (buffered; drops on overflow). The channel closes
// when the connection drops.
func (c *Client) SubscribeEvents() (<-chan Event, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextSub
	c.nextSub++
	ch := make(chan Event, 32)
	c.subs[id] = ch
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if s, ok := c.subs[id]; ok {
			delete(c.subs, id)
			close(s)
		}
	}
}

// Close shuts down the connection and the background read loop.
func (c *Client) Close() error {
	c.cancel()
	return c.ws.Close(websocket.StatusNormalClosure, "bye")
}

// Request sends an op:6 Request and waits for the matching op:7 response.
// Returns responseData (may be nil) or an error if result==false.
func (c *Client) Request(ctx context.Context, requestType string, data any) (json.RawMessage, error) {
	id := fmt.Sprintf("req-%d", c.seq.Add(1))
	ch := make(chan responseFrame, 1)
	c.mu.Lock()
	c.waiters[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.waiters, id)
		c.mu.Unlock()
	}()

	frame := requestFrame{RequestType: requestType, RequestID: id, RequestData: data}
	if err := writeJSON(ctx, c.ws, envelope{Op: opRequest, Data: mustMarshal(frame)}); err != nil {
		return nil, fmt.Errorf("obs request send: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, fmt.Errorf("obs: connection closed")
	case resp := <-ch:
		if !resp.RequestStatus.Result {
			// requestStatus.result==false: surfaced as an error with the comment.
			return nil, fmt.Errorf("obs %s failed (code %d): %s",
				requestType, resp.RequestStatus.Code, resp.RequestStatus.Comment)
		}
		return resp.ResponseData, nil
	}
}

// readLoop decodes frames: op:7 to waiting Request callers, op:5 to event subscribers.
// On read error (conn closed) it cancels the client ctx - Done() fires, pending Requests
// unblock, subscriber channels close.
func (c *Client) readLoop() {
	defer func() {
		c.cancel()
		c.mu.Lock()
		for id, ch := range c.subs {
			delete(c.subs, id)
			close(ch)
		}
		c.mu.Unlock()
	}()
	for {
		var env envelope
		if err := readJSON(c.ctx, c.ws, &env); err != nil {
			return // context cancelled or conn closed
		}
		switch env.Op {
		case opResponse:
			var rf responseFrame
			if err := json.Unmarshal(env.Data, &rf); err != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.waiters[rf.RequestID]
			c.mu.Unlock()
			if ok {
				ch <- rf
			}
		case opEvent:
			var ef eventFrame
			if err := json.Unmarshal(env.Data, &ef); err != nil {
				continue
			}
			c.mu.Lock()
			subs := make([]chan Event, 0, len(c.subs))
			for _, s := range c.subs {
				subs = append(subs, s)
			}
			c.mu.Unlock()
			for _, s := range subs {
				select {
				case s <- Event{Type: ef.EventType, Data: ef.EventData}:
				default: // slow subscriber: drop rather than stall the read loop
				}
			}
		}
	}
}

// ── wire helpers ─────────────────────────────────────────────────────────────

func readJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	_, raw, err := ws.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func writeJSON(ctx context.Context, ws *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, raw)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
