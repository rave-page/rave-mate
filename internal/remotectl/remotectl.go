// Package remotectl is a request/response RPC layer over the peerlink ChanControl data
// channel: one rave-mate instance drives a paired instance's Automations + Library managers.
// Symmetric - every instance is both server (Register handlers) and client (Call). Frames are
// JSON, correlated by id; delivery rides peerlink's reliable+ordered, HMAC'd data frames.
// Wire it by passing OnControl to peerbridge.SetControlSink and a SendTo-bound SendFunc.
package remotectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/debuglog"
)

const (
	kindReq = "req"
	kindRes = "res"

	// maxControlFrame caps an encoded request/response. Below peerlink's 32 MiB transport frame cap,
	// leaving headroom for the peerlink data-frame envelope. Generous so RPC results like screenshots
	// (and future media chunks) fit; library pages still size themselves to stay well under this.
	maxControlFrame = 24 << 20

	// peerlink already authenticates + MACs every data frame, so frames here carry no seq/mac
	// of their own - only the studio-shaped {t,id,method,params}/{t,id,ok,result,error} body.

	// serveTimeout bounds a single inbound handler so a wedged handler can't leak a goroutine.
	serveTimeout = 60 * time.Second

	// DefaultCallTimeout is the client-side round-trip budget when a caller doesn't set one.
	DefaultCallTimeout = 12 * time.Second
)

// Logger is the logbus subset used here (decoupled for testing).
type Logger interface {
	Info(source, msg string, fields map[string]any)
	Warn(source, msg string, fields map[string]any)
}

// SendFunc delivers an encoded control frame to one peer (peerlink SendTo on ChanControl).
type SendFunc func(nodeID string, payload []byte) error

// Handler serves one remote method. params is raw JSON (may be nil); return a JSON-marshalable
// result or an error (its text is sent to the caller).
type Handler func(ctx context.Context, peerNodeID string, params json.RawMessage) (any, error)

// frame mirrors the Local Studio wire body (app/core/studio/protocol.ts) so peer control is
// the same logic system as browser control - just a different transport.
type frame struct {
	T      string          `json:"t"` // "req" | "res"
	ID     string          `json:"id"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     bool            `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Err    string          `json:"error,omitempty"`
}

// Endpoint multiplexes outbound calls + inbound handlers over one control channel.
type Endpoint struct {
	log  Logger
	send SendFunc

	mu       sync.Mutex
	handlers map[string]Handler
	pending  map[string]chan frame
	seq      uint64
}

// New builds an endpoint. send delivers a frame to the named peer over ChanControl.
func New(log Logger, send SendFunc) *Endpoint {
	return &Endpoint{log: log, send: send, handlers: map[string]Handler{}, pending: map[string]chan frame{}}
}

// Register installs a server-side handler for method (last registration wins).
func (e *Endpoint) Register(method string, h Handler) {
	e.mu.Lock()
	e.handlers[method] = h
	e.mu.Unlock()
}

func (e *Endpoint) nextID() string { return strconv.FormatUint(atomic.AddUint64(&e.seq, 1), 36) }

// OnControl is the inbound sink (matches peerbridge SetControlSink): dispatch a request to a
// handler (off-thread) or deliver a response to its waiter.
func (e *Endpoint) OnControl(peerNodeID string, payload []byte) {
	var f frame
	if err := json.Unmarshal(payload, &f); err != nil {
		return
	}
	switch f.T {
	case kindReq:
		go e.serve(peerNodeID, f)
	case kindRes:
		e.mu.Lock()
		ch := e.pending[f.ID]
		delete(e.pending, f.ID)
		e.mu.Unlock()
		if ch != nil {
			ch <- f // buffered; non-blocking
		}
	}
}

func (e *Endpoint) serve(peerNodeID string, req frame) {
	defer debuglog.Recover(nil, "remotectl", false) // serve runs in its own goroutine; nil bus: decoupled via Logger iface
	e.mu.Lock()
	h := e.handlers[req.Method]
	e.mu.Unlock()
	resp := frame{T: kindRes, ID: req.ID}

	if h == nil {
		resp.Err = "unknown method: " + req.Method
		e.reply(peerNodeID, resp)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), serveTimeout)
	defer cancel()
	result, err := h(ctx, peerNodeID, req.Params)
	if err != nil {
		resp.Err = err.Error()
		e.reply(peerNodeID, resp)
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Err = "marshal result: " + err.Error()
		e.reply(peerNodeID, resp)
		return
	}
	if len(raw) > maxControlFrame {
		resp.Err = fmt.Sprintf("result too large (%d bytes); narrow the request", len(raw))
		e.reply(peerNodeID, resp)
		return
	}
	resp.OK = true
	resp.Result = raw
	e.reply(peerNodeID, resp)
}

func (e *Endpoint) reply(nodeID string, f frame) {
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	if err := e.send(nodeID, raw); err != nil && e.log != nil {
		e.log.Warn("remotectl", "reply failed", map[string]any{"method": f.ID, "error": err.Error()})
	}
}

// Call invokes method on nodeID and returns the raw JSON result. Blocks until the peer answers
// or ctx is done; call off the UI thread.
func (e *Endpoint) Call(ctx context.Context, nodeID, method string, params any) (json.RawMessage, error) {
	if e == nil || e.send == nil {
		return nil, errors.New("remotectl: endpoint unavailable")
	}
	if nodeID == "" {
		return nil, errors.New("remotectl: no target peer")
	}
	var praw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		praw = b
	}
	id := e.nextID()
	ch := make(chan frame, 1)
	e.mu.Lock()
	e.pending[id] = ch
	e.mu.Unlock()
	defer func() { e.mu.Lock(); delete(e.pending, id); e.mu.Unlock() }()

	raw, err := json.Marshal(frame{T: kindReq, ID: id, Method: method, Params: praw})
	if err != nil {
		return nil, err
	}
	if len(raw) > maxControlFrame {
		return nil, fmt.Errorf("remotectl: request too large (%d bytes)", len(raw))
	}
	if err := e.send(nodeID, raw); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f := <-ch:
		if f.Err != "" {
			return nil, errors.New(f.Err)
		}
		return f.Result, nil
	}
}

// Do is the typed Call: unmarshal the result into T. Empty result → zero T.
func Do[T any](ctx context.Context, e *Endpoint, nodeID, method string, params any) (T, error) {
	var out T
	raw, err := e.Call(ctx, nodeID, method, params)
	if err != nil {
		return out, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
