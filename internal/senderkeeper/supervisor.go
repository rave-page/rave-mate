package senderkeeper

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrHeld: the keeper is reachable but VRAM admission refused creating a NEW destination.
var ErrHeld = errors.New("senderkeeper: VRAM admission refused a new destination")

// ErrRecreateSpent: a destination that existed before the keeper died already had its ONE guarded
// re-create this app session; the receive holds instead of churning senders in Resolume.
var ErrRecreateSpent = errors.New("senderkeeper: destination lost twice - holding (one re-create per session)")

// Options wires a Supervisor. Dial is required; Start (spawn a detached keeper) may be nil in processes
// that only ADOPT (the media child sits in a kill-on-close job without breakaway: it must never be the
// keeper's parent, so only the daemon starts it).
type Options struct {
	Dial  func() (Conn, error)
	Start func() error
	// Admit gates creating a NEW destination (the unified VRAM budget for a new sender + follow).
	// nil = admit. Adopting an existing destination is never gated (it costs nothing new).
	Admit func(name string, w, h int) bool
	// StartWait bounds how long to wait for a just-started keeper's pipe (default 2 s).
	StartWait time.Duration
}

// Supervisor adopts keeper-owned destinations and creates missing ones through admission. Lifecycle
// (1b): the daemon starts the keeper on first need; the keeper outlives daemon, media child and
// self-update; every (re)start adopts by name (same handle -> Resolume sees nothing new); a keeper
// death costs at most ONE guarded re-create per destination per app session.
type Supervisor struct {
	o Options

	mu        sync.Mutex
	conn      Conn
	known     map[string]bool // destinations this session has seen in the keeper (bounded: remembered set)
	recreated map[string]bool // destinations already re-created once after a keeper loss
}

// NewSupervisor builds a Supervisor (no I/O until first use).
func NewSupervisor(o Options) *Supervisor {
	if o.StartWait <= 0 {
		o.StartWait = 2 * time.Second
	}
	return &Supervisor{o: o, known: map[string]bool{}, recreated: map[string]bool{}}
}

// connLocked returns a live connection, dialing (and, when allowed, starting the keeper once).
func (s *Supervisor) connLocked() (Conn, error) {
	if s.conn != nil {
		return s.conn, nil
	}
	c, err := s.o.Dial()
	if err != nil && s.o.Start != nil {
		if serr := s.o.Start(); serr != nil {
			return nil, fmt.Errorf("%w: start: %v", ErrUnavailable, serr)
		}
		deadline := time.Now().Add(s.o.StartWait)
		for err != nil && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			c, err = s.o.Dial()
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if h, herr := c.Hello(); herr != nil || h.Proto < 1 {
		_ = c.Close()
		return nil, fmt.Errorf("%w: handshake: %v", ErrUnavailable, herr)
	}
	s.conn = c
	return c, nil
}

// drop forgets a broken connection (the keeper may have died; the next call redials/restarts).
func (s *Supervisor) dropLocked() {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// Ensure returns the named destination: ADOPTED when the keeper already holds it (no admission, no new
// sender), else created through Admit. A destination known earlier this session that is now missing
// means the keeper died: exactly one guarded re-create, then ErrRecreateSpent.
func (s *Supervisor) Ensure(name string, w, h int) (Sender, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for try := 0; try < 2; try++ {
		c, err := s.connLocked()
		if err != nil {
			return Sender{}, false, err
		}
		list, err := c.List()
		if err != nil {
			s.dropLocked()
			continue // keeper died between calls: redial (and restart) once
		}
		for _, x := range list {
			if x.Name == name {
				got, _, err := c.Ensure(name, w, h) // adopt: unchanged geometry/handle
				if err != nil {
					s.dropLocked()
					return Sender{}, false, err
				}
				s.known[name] = true
				return got, false, nil
			}
		}
		if s.known[name] {
			if s.recreated[name] {
				return Sender{}, false, ErrRecreateSpent
			}
			s.recreated[name] = true
		}
		if s.o.Admit != nil && !s.o.Admit(name, w, h) {
			return Sender{}, false, ErrHeld
		}
		got, created, err := c.Ensure(name, w, h)
		if err != nil {
			s.dropLocked()
			return Sender{}, false, err
		}
		s.known[name] = true
		return got, created, nil
	}
	return Sender{}, false, ErrUnavailable
}

// Has reports whether the keeper already holds name (adoption costs nothing new: callers skip the
// new-sender admission). Unreachable keeper => false.
func (s *Supervisor) Has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connLocked()
	if err != nil {
		return false
	}
	l, err := c.List()
	if err != nil {
		s.dropLocked()
		return false
	}
	for _, x := range l {
		if x.Name == name {
			return true
		}
	}
	return false
}

// Ping connects (starting the keeper when this Supervisor may) and handshakes.
func (s *Supervisor) Ping() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.connLocked()
	return err
}

// Clear parks a destination (one transparent frame). Best-effort.
func (s *Supervisor) Clear(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.connLocked()
	if err != nil {
		return err
	}
	if err := c.Clear(name); err != nil {
		s.dropLocked()
		return err
	}
	return nil
}

// Release destroys a destination (explicit stop / forgotten receive only - never on app exit).
func (s *Supervisor) Release(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.known, name)
	delete(s.recreated, name)
	c, err := s.connLocked()
	if err != nil {
		return err
	}
	if err := c.Release(name); err != nil {
		s.dropLocked()
		return err
	}
	return nil
}

// Close drops the control connection. The keeper and its senders stay up (that is the point).
func (s *Supervisor) Close() {
	s.mu.Lock()
	s.dropLocked()
	s.mu.Unlock()
}
