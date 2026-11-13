package senderkeeper

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
)

// FakeServer is a GPU-free keeper: the same protocol and semantics as native/zigkeeper (adopt
// existing, immortal geometry, bounded table, unknown ops answered with an error) with fake handles.
// Exported for other packages' tests (mediaroute adoption tests).
type FakeServer struct {
	mu      sync.Mutex
	senders map[string]Sender
	next    uint64
	Creates int // texture creations (a restart-adoption test asserts this stays put)
	Clears  int
	dead    bool
	conns   []net.Conn // live server ends (Kill breaks them, like a dying keeper breaks its pipe)
}

// fakeMax mirrors the keeper's max_senders.
const fakeMax = 16

// NewFakeServer returns an empty fake keeper.
func NewFakeServer() *FakeServer { return &FakeServer{senders: map[string]Sender{}, next: 0x40000002} }

// Dial connects a new client to the fake (one goroutine per connection, like the real pipe's serial
// clients). A dead fake (Kill) refuses.
func (f *FakeServer) Dial() (Conn, error) {
	f.mu.Lock()
	dead := f.dead
	f.mu.Unlock()
	if dead {
		return nil, ErrUnavailable
	}
	a, b := net.Pipe()
	f.mu.Lock()
	f.conns = append(f.conns, b)
	f.mu.Unlock()
	go f.serve(b)
	return NewConn(a), nil
}

// Kill simulates the keeper process dying: every sender is gone, dials fail until Revive.
func (f *FakeServer) Kill() {
	f.mu.Lock()
	f.dead = true
	f.senders = map[string]Sender{}
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// Revive simulates a fresh keeper start (empty table).
func (f *FakeServer) Revive() {
	f.mu.Lock()
	f.dead = false
	f.mu.Unlock()
}

// Senders snapshots the table (sorted by name).
func (f *FakeServer) Senders() []Sender {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Sender, 0, len(f.senders))
	for _, s := range f.senders {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f *FakeServer) serve(c io.ReadWriteCloser) {
	defer c.Close()
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return
		}
		out := f.handle(line)
		if _, err := c.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

func (f *FakeServer) handle(line []byte) []byte {
	var r req
	if json.Unmarshal(line, &r) != nil {
		return []byte(`{"ok":false,"err":"malformed"}`)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Op {
	case "hello":
		return []byte(fmt.Sprintf(`{"ok":true,"proto":%d,"pid":4242,"senders":%d}`, Proto, len(f.senders)))
	case "ensure":
		if s, ok := f.senders[r.Name]; ok {
			return mustJSON(ensureResp{OK: true, Handle: s.Handle, Fmt: 87, W: s.W, H: s.H})
		}
		if r.Name == "" || r.W <= 0 || r.H <= 0 {
			return []byte(`{"ok":false,"err":"BadName"}`)
		}
		if len(f.senders) >= fakeMax {
			return []byte(`{"ok":false,"err":"Full"}`)
		}
		f.next += 4
		s := Sender{Name: r.Name, W: r.W, H: r.H, Handle: f.next, Fmt: 87}
		f.senders[r.Name] = s
		f.Creates++
		return mustJSON(ensureResp{OK: true, Handle: s.Handle, Fmt: 87, W: s.W, H: s.H, Created: true})
	case "list":
		l := make([]Sender, 0, len(f.senders))
		for _, s := range f.senders {
			l = append(l, s)
		}
		return mustJSON(listResp{OK: true, Senders: l})
	case "clear", "release":
		if _, ok := f.senders[r.Name]; !ok {
			return []byte(`{"ok":false,"err":"unknown sender"}`)
		}
		if r.Op == "release" {
			delete(f.senders, r.Name)
		} else {
			f.Clears++
		}
		return []byte(`{"ok":true}`)
	}
	return []byte(`{"ok":false,"err":"unknown op"}`)
}

type ensureResp struct {
	OK      bool   `json:"ok"`
	Handle  uint64 `json:"handle"`
	Fmt     uint32 `json:"fmt"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Created bool   `json:"created"`
}

type listResp struct {
	OK      bool     `json:"ok"`
	Senders []Sender `json:"senders"`
}

func mustJSON[T ensureResp | listResp](v T) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"ok":false,"err":"encode"}`)
	}
	return b
}
