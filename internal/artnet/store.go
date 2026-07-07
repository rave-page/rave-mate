package artnet

import (
	"sort"
	"sync"
	"time"
)

// Store is the thread-safe DMX universe cache: 15-bit universe → 512 slots, with per-universe
// last-seen, sequence-wrap handling, source IP + a packet-rate estimate. A global generation
// counter bumps on any slot change so render sinks can cheaply detect "anything changed".
//
// API is source/sink-agnostic: the Art-Net listener is one source (Set), the VRSL grid is one
// sink (Get/Generation). A future peer source (DMX universes arriving over the peer bus) calls
// Set exactly like the listener; a future peer sink polls Generation/Get like the grid - no
// store change needed. Reader is the minimal read surface a renderer depends on.
type Store struct {
	mu   sync.RWMutex
	unis map[uint16]*universe
	gen  uint64
}

// Reader is the read-only universe surface a renderer consumes (satisfied by *Store).
type Reader interface {
	// Get returns universe u's 512 slots (false if never seen).
	Get(u uint16) ([512]byte, bool)
	// Generation returns a counter that increases whenever any slot value changes.
	Generation() uint64
}

type universe struct {
	data     [512]byte
	lastSeen time.Time
	lastSeq  byte
	packets  uint64
	srcIP    string

	// rolling 1s packet-rate window.
	winStart time.Time
	winCount int
	pps      float64
}

// UniverseStat is a per-universe status snapshot for the UI / ctl.
type UniverseStat struct {
	Universe uint16
	Packets  uint64
	PPS      float64
	LastSeen time.Time
	SourceIP string
}

// NewStore builds an empty universe store.
func NewStore() *Store { return &Store{unis: map[uint16]*universe{}} }

// Set applies an ArtDmx frame. It drops out-of-order packets (sequence-wrap aware) and bumps the
// generation only when a slot value actually changes. Returns true if the frame was accepted
// (in-order), regardless of whether values changed. now is injected for testability.
func (s *Store) Set(u uint16, seq byte, data []byte, srcIP string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	un := s.unis[u]
	if un == nil {
		un = &universe{winStart: now}
		s.unis[u] = un
	}
	if !seqNewer(seq, un.lastSeq) {
		return false // stale / reordered
	}
	un.lastSeq = seq
	un.lastSeen = now
	un.srcIP = srcIP
	un.packets++
	// packet-rate: recompute each ≥1s window.
	un.winCount++
	if el := now.Sub(un.winStart); el >= time.Second {
		un.pps = float64(un.winCount) / el.Seconds()
		un.winStart = now
		un.winCount = 0
	}
	changed := false
	n := len(data)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		if un.data[i] != data[i] {
			un.data[i] = data[i]
			changed = true
		}
	}
	if changed {
		s.gen++
	}
	return true
}

// Get returns universe u's 512 slots (false if never seen).
func (s *Store) Get(u uint16) ([512]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	un := s.unis[u]
	if un == nil {
		return [512]byte{}, false
	}
	return un.data, true
}

// Generation returns the change counter (bumps on any slot value change).
func (s *Store) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gen
}

// Stats returns per-universe status, universe-sorted. staleAfter zeroes the PPS of universes
// idle longer than that (a stopped source shouldn't keep reporting its last rate).
func (s *Store) Stats(now time.Time, staleAfter time.Duration) []UniverseStat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]UniverseStat, 0, len(s.unis))
	for u, un := range s.unis {
		pps := un.pps
		if now.Sub(un.lastSeen) > staleAfter {
			pps = 0
		}
		out = append(out, UniverseStat{Universe: u, Packets: un.packets, PPS: pps, LastSeen: un.lastSeen, SourceIP: un.srcIP})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Universe < out[j].Universe })
	return out
}

// seqNewer reports whether sequence cur supersedes last (Art-Net wraps 1..255; 0 = disabled).
func seqNewer(cur, last byte) bool {
	if cur == 0 || last == 0 {
		return true
	}
	d := cur - last // mod-256 distance; 1..127 = newer
	return d != 0 && d < 128
}
