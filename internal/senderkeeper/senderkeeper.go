// Package senderkeeper is the Go control side of rave-mate-keeper (native/zigkeeper): a long-lived,
// single-instance, pure-D3D11 Spout sender host that OWNS receive destinations, so a daemon restart, a
// self-update or a media-child crash never makes Resolume see a NEW sender (the one event that can
// fail on a card another app keeps full - .devnotes/VRAM_FULL_CARD_POLICY.md 1b).
//
// Control only (no frames): newline JSON over the keeper's named pipe. The decode child writes into
// the keeper's texture by share handle (OpenSharedResource), exactly as into a SpoutLibrary sender.
package senderkeeper

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Proto is the keeper protocol version this client speaks. The keeper answers hello with its own;
// unknown fields are ignored both ways (version tolerant), a newer keeper keeps serving proto-1 ops.
const Proto = 1

// Sender is one keeper-owned destination.
type Sender struct {
	Name   string `json:"name"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Handle uint64 `json:"handle"`
	Fmt    uint32 `json:"fmt,omitempty"`
}

// Hello is the keeper's handshake answer.
type Hello struct {
	Proto   int `json:"proto"`
	PID     int `json:"pid"`
	Senders int `json:"senders"`
}

// Conn is the keeper control surface (real pipe client or the GPU-free fake).
type Conn interface {
	Hello() (Hello, error)
	// Ensure returns the named sender, creating it at w x h only if absent; an existing one is ADOPTED
	// unchanged (nominal geometry is immortal) and created=false.
	Ensure(name string, w, h int) (s Sender, created bool, err error)
	List() ([]Sender, error)
	Clear(name string) error   // one transparent frame (park)
	Release(name string) error // unregister + free (explicit stop / forget only)
	Close() error
}

type req struct {
	Op    string `json:"op"`
	Proto int    `json:"proto,omitempty"`
	Name  string `json:"name,omitempty"`
	W     int    `json:"w,omitempty"`
	H     int    `json:"h,omitempty"`
}

type resp struct {
	OK      bool   `json:"ok"`
	Err     string `json:"err,omitempty"`
	Proto   int    `json:"proto,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Handle  uint64 `json:"handle,omitempty"`
	Fmt     uint32 `json:"fmt,omitempty"`
	W       int    `json:"w,omitempty"`
	H       int    `json:"h,omitempty"`
	Created bool   `json:"created,omitempty"`
}

// maxLine bounds one response line (the keeper's own reply buffer is 8 KiB).
const maxLine = 16 * 1024

// lineConn speaks the protocol over any byte stream. Serialized: one request in flight.
type lineConn struct {
	mu sync.Mutex
	rw io.ReadWriteCloser
	br *bufio.Reader
}

// NewConn wraps a byte stream (the keeper pipe, or one end of a net.Pipe to a FakeServer).
func NewConn(rw io.ReadWriteCloser) Conn {
	return &lineConn{rw: rw, br: bufio.NewReaderSize(rw, maxLine)}
}

func (c *lineConn) call(r req) (json.RawMessage, resp, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := json.Marshal(r)
	if err != nil {
		return nil, resp{}, err
	}
	if _, err := c.rw.Write(append(b, '\n')); err != nil {
		return nil, resp{}, err
	}
	line, err := c.br.ReadSlice('\n')
	if err != nil {
		return nil, resp{}, err
	}
	raw := append(json.RawMessage(nil), line...)
	var out resp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, resp{}, fmt.Errorf("senderkeeper: bad reply: %w", err)
	}
	if !out.OK {
		return raw, out, fmt.Errorf("senderkeeper: %s: %s", r.Op, out.Err)
	}
	return raw, out, nil
}

func (c *lineConn) Hello() (Hello, error) {
	raw, _, err := c.call(req{Op: "hello", Proto: Proto})
	if err != nil {
		return Hello{}, err
	}
	var h Hello
	err = json.Unmarshal(raw, &h)
	return h, err
}

func (c *lineConn) Ensure(name string, w, h int) (Sender, bool, error) {
	_, r, err := c.call(req{Op: "ensure", Name: name, W: w, H: h})
	if err != nil {
		return Sender{}, false, err
	}
	return Sender{Name: name, W: r.W, H: r.H, Handle: kmtHandle(r.Handle), Fmt: r.Fmt}, r.Created, nil
}

func (c *lineConn) List() ([]Sender, error) {
	raw, _, err := c.call(req{Op: "list"})
	if err != nil {
		return nil, err
	}
	var l struct {
		Senders []Sender `json:"senders"`
	}
	err = json.Unmarshal(raw, &l)
	for i := range l.Senders {
		l.Senders[i].Handle = kmtHandle(l.Senders[i].Handle)
	}
	return l.Senders, err
}

func (c *lineConn) Clear(name string) error {
	_, _, err := c.call(req{Op: "clear", Name: name})
	return err
}

func (c *lineConn) Release(name string) error {
	_, _, err := c.call(req{Op: "release", Name: name})
	return err
}

func (c *lineConn) Close() error { return c.rw.Close() }

// ErrUnavailable means no keeper could be reached or started (platform, missing exe, policy).
var ErrUnavailable = errors.New("senderkeeper: keeper unavailable")

// kmtHandle sign-extends a 32-bit legacy (KMT) share handle the way Windows and the Spout SDK
// (LongToHandle) present it on 64-bit, so a keeper handle compares equal to the Spout registry view.
func kmtHandle(h uint64) uint64 { return uint64(int64(int32(uint32(h)))) }
