// Package mpvipc is a minimal client for mpv's JSON IPC (https://mpv.io/manual/stable/#json-ipc):
// newline-delimited JSON commands over mpv's --input-ipc-server channel (a Windows named pipe, a
// unix socket elsewhere). It drives playback (play/pause/seek) and observes properties (time-pos,
// duration, eof) so the app's own transport UI can control an mpv render window.
package mpvipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a connected mpv IPC channel. Safe for concurrent use.
type Client struct {
	conn io.ReadWriteCloser

	wmu    sync.Mutex // serializes writes
	nextID int64

	pmu     sync.Mutex
	pending map[int64]chan reply
	onProp  func(name string, data json.RawMessage)
	onEvent func(event string)
	closed  bool
}

type reply struct {
	Err  string
	Data json.RawMessage
}

// wire is the union of mpv reply + event frames.
type wire struct {
	RequestID int64           `json:"request_id"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
	Event     string          `json:"event"`
	Name      string          `json:"name"` // property name on property-change events
}

// New wraps an already-connected duplex stream (the platform dialer opens it) and starts the
// reader. Callers normally use Dial.
func New(conn io.ReadWriteCloser) *Client {
	c := &Client{conn: conn, pending: map[int64]chan reply{}}
	go c.readLoop()
	return c
}

// Dial connects to mpv's IPC endpoint (a named pipe / unix socket), retrying until ctx expires -
// mpv creates the endpoint shortly after launch, so a brief poll covers the startup race.
func Dial(ctx context.Context, addr string) (*Client, error) {
	for {
		conn, err := dial(addr)
		if err == nil {
			return New(conn), nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("mpv ipc connect %s: %w", addr, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// OnProperty registers a callback for observed property changes (see ObserveProperty).
func (c *Client) OnProperty(fn func(name string, data json.RawMessage)) {
	c.pmu.Lock()
	c.onProp = fn
	c.pmu.Unlock()
}

// OnEvent registers a callback for mpv events (e.g. "end-file", "idle").
func (c *Client) OnEvent(fn func(event string)) {
	c.pmu.Lock()
	c.onEvent = fn
	c.pmu.Unlock()
}

// Command sends an mpv command and waits for its reply (request_id-correlated). Returns the
// reply data (may be null) or an error if mpv reports one / the channel closes / 5s elapses.
func (c *Client) Command(args ...any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan reply, 1)
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		return nil, fmt.Errorf("mpv ipc closed")
	}
	c.pending[id] = ch
	c.pmu.Unlock()
	defer func() { c.pmu.Lock(); delete(c.pending, id); c.pmu.Unlock() }()

	frame := struct {
		Command   []any `json:"command"`
		RequestID int64 `json:"request_id"`
	}{Command: args, RequestID: id}
	b, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	if err := c.write(append(b, '\n')); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Err != "" && r.Err != "success" {
			return r.Data, fmt.Errorf("mpv: %s", r.Err)
		}
		return r.Data, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("mpv ipc command timeout")
	}
}

// Set sets a property (set_property).
func (c *Client) Set(name string, value any) error {
	_, err := c.Command("set_property", name, value)
	return err
}

// GetFloat reads a numeric property (e.g. time-pos, duration).
func (c *Client) GetFloat(name string) (float64, error) {
	d, err := c.Command("get_property", name)
	if err != nil {
		return 0, err
	}
	var f float64
	if err := json.Unmarshal(d, &f); err != nil {
		return 0, err
	}
	return f, nil
}

// ObserveProperty asks mpv to push change events for name (delivered to OnProperty). id is the
// caller-chosen observe id (unique per property).
func (c *Client) ObserveProperty(id int, name string) error {
	_, err := c.Command("observe_property", id, name)
	return err
}

// Close shuts the IPC channel; pending commands fail. Does not quit mpv (caller sends "quit").
func (c *Client) Close() error {
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		return nil
	}
	c.closed = true
	for _, ch := range c.pending {
		close(ch)
	}
	c.pending = map[int64]chan reply{}
	c.pmu.Unlock()
	return c.conn.Close()
}

func (c *Client) write(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.conn.Write(b)
	return err
}

// readLoop dispatches reply frames to waiting Command calls and property/event frames to callbacks.
func (c *Client) readLoop() {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var w wire
		if json.Unmarshal(line, &w) != nil {
			continue
		}
		switch {
		case w.Event == "property-change":
			c.pmu.Lock()
			fn := c.onProp
			c.pmu.Unlock()
			if fn != nil {
				fn(w.Name, w.Data)
			}
		case w.Event != "":
			c.pmu.Lock()
			fn := c.onEvent
			c.pmu.Unlock()
			if fn != nil {
				fn(w.Event)
			}
		default: // reply to a Command
			c.pmu.Lock()
			ch, ok := c.pending[w.RequestID]
			c.pmu.Unlock()
			if ok {
				ch <- reply{Err: w.Error, Data: w.Data}
			}
		}
	}
}
