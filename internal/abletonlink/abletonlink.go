// Package abletonlink publishes rave-mate's fused DJ tempo/beat onto an Ableton Link
// session so Link-aware apps (Resolume, Ableton Live, Serato, VDMX) follow the master
// tempo + phrase phase. rave-mate is the Traktor-to-Link bridge Traktor itself can't be.
//
// The real Link backend is the official abl_link C wrapper via cgo, gated behind the
// `abletonlink` build tag and isolated in the featurehost "abletonlink" child (cgo + the
// C++/asio/Winsock Link runtime = the build risk, so it never runs in the daemon and the
// default build ships without it). NewLink returns ErrUnavailable in the untagged build;
// callers fall back to Stub{} and the feature reports unavailable.
//
// Session is the backend-agnostic abstraction. TimeSource adapts a Session to
// mediasync.TimeSource (internal/mediasync/clock.go) so the media-sync chaser can lock OBS
// media to Link musical time - the "house clock plugs in later" seam.
package abletonlink

import (
	"errors"
	"time"
)

// ErrUnavailable is returned by NewLink when no real Link backend is compiled in
// (default build, `abletonlink` tag off) or cgo is disabled.
var ErrUnavailable = errors.New("abletonlink: Link backend not compiled (build with -tags abletonlink)")

// DefaultQuantum is the default phrase length in beats (one 16-beat phrase = 4 bars of 4/4).
const DefaultQuantum = 16.0

// State is one sampled snapshot of the Link session's shared timeline.
type State struct {
	Available bool      `json:"available"` // a real Link backend is present + running
	Enabled   bool      `json:"enabled"`   // joined the Link session (peer-discoverable)
	Tempo     float64   `json:"tempo"`     // session tempo, BPM
	Beat      float64   `json:"beat"`      // beats on the shared timeline at At (monotonic while playing)
	Phase     float64   `json:"phase"`     // phase within the phrase, [0,Quantum) - 0 = phrase start
	Quantum   float64   `json:"quantum"`   // phrase length in beats
	Peers     int       `json:"peers"`     // number of connected Link peers
	Playing   bool      `json:"playing"`   // start/stop-sync transport state
	At        time.Time `json:"at"`        // wall time the snapshot was taken
}

// PhraseFraction returns the phrase position as 0..1 (Phase/Quantum). 0 at a phrase start.
func (s State) PhraseFraction() float64 {
	if s.Quantum <= 0 {
		return 0
	}
	f := s.Phase / s.Quantum
	if f < 0 {
		return 0
	}
	if f >= 1 {
		return f - float64(int(f))
	}
	return f
}

// Session is a live Ableton Link session (real cgo backend or a no-op stub). All methods are
// safe for concurrent use. Tempo is BPM; beats/phase are on the Link shared timeline.
type Session interface {
	// Available reports whether this is a real Link backend (false for Stub).
	Available() bool
	// SetEnabled joins (true) or leaves (false) the Link session.
	SetEnabled(bool)
	// State samples the current shared-timeline state at now.
	State(now time.Time) State
	// SetTempo sets the session tempo (BPM) at now - proposed to all peers.
	SetTempo(bpm float64, now time.Time)
	// ForceBeat maps beat to now with a hard jump (phrase realign - the resync primitive).
	ForceBeat(beat float64, now time.Time)
	// RequestBeat gently maps beat to now within the current quantum (phase align, no tempo jump).
	RequestBeat(beat float64, now time.Time)
	// SetQuantum sets the phrase length in beats (8/16/32).
	SetQuantum(quantum float64)
	// SetStartStopSyncEnabled toggles transport (start/stop) sharing across peers.
	SetStartStopSyncEnabled(bool)
	// SetPlaying sets the shared transport state at now (start/stop-sync must be enabled).
	SetPlaying(playing bool, now time.Time)
	// Close releases the backend.
	Close() error
}

// TimeSource adapts a Session to mediasync.TimeSource: Link musical time as a monotonic
// timeline position. Position = Beat × (60/Tempo) seconds while the session is enabled and
// the tempo is known; running=false when Link is unavailable/disabled/tempo-less so the
// media chaser leaves sources untouched (identical to a stopped WallClock).
type TimeSource struct {
	s   Session
	now func() time.Time // injectable for tests
}

// NewTimeSource wraps a Session as a mediasync.TimeSource house clock.
func NewTimeSource(s Session) *TimeSource { return &TimeSource{s: s, now: time.Now} }

// Position returns Link musical time as a duration + whether the clock is running.
func (t *TimeSource) Position() (time.Duration, bool) {
	st := t.s.State(t.clock())
	if !st.Available || !st.Enabled || st.Tempo <= 0 {
		return 0, false
	}
	secPerBeat := 60.0 / st.Tempo
	return time.Duration(st.Beat * secPerBeat * float64(time.Second)), true
}

func (t *TimeSource) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// Stub is a no-op Session used when no real Link backend is present. Available()==false;
// every method is inert. State reports Available:false so the TimeSource stays stopped.
type Stub struct{}

// NewStub returns an inert Session (Available()==false).
func NewStub() Stub { return Stub{} }

func (Stub) Available() bool                { return false }
func (Stub) SetEnabled(bool)                {}
func (Stub) State(time.Time) State          { return State{Quantum: DefaultQuantum} }
func (Stub) SetTempo(float64, time.Time)    {}
func (Stub) ForceBeat(float64, time.Time)   {}
func (Stub) RequestBeat(float64, time.Time) {}
func (Stub) SetQuantum(float64)             {}
func (Stub) SetStartStopSyncEnabled(bool)   {}
func (Stub) SetPlaying(bool, time.Time)     {}
func (Stub) Close() error                   { return nil }

var _ Session = Stub{}
