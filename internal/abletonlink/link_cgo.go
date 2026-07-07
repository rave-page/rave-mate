//go:build abletonlink && cgo

package abletonlink

// Real Ableton Link backend via the official abl_link C wrapper (cgo). Built ONLY with
// `-tags abletonlink` (cgo on) - the default build ships link_stub.go and this file is
// excluded, so CI never compiles the C++/asio/Winsock Link runtime.
//
// EXTERNAL BUILD-TIME DEP (not vendored - Link is dual GPLv2+/commercial). A prebuilt static
// lib carries the C++/asio Link runtime so this package only needs the C wrapper header + the
// lib (no C++ translation unit in the package - cgo would reject a stray .cpp in the untagged
// build). Two prep steps before `-tags abletonlink` builds (documented in SUPPLY_CHAIN.md /
// a `make link-sdk` target):
//
//	1. Checkout github.com/Ableton/link (with submodules: asio) under third_party/link.
//	2. Compile the abl_link wrapper (extensions/abl_link/src/abl_link.cpp) + the header-only
//	   Link runtime into a static archive → third_party/link/lib/libabl_link.a
//	   (g++ -std=c++17 -c abl_link.cpp with the include dirs below, then `ar rcs`).
//
// Then set CGO_ENABLED=1 and build with the mingw static toolchain. -extldflags=-static folds
// in libstdc++/libgcc so the exe runs on a clean PC. On Windows Link needs ws2_32/iphlpapi/winmm.
//
// USER TO-DO (distribution-time, non-blocking for dev): request Ableton's free commercial
// Link license (standard grant for Link-enabled apps) before shipping.

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/link/extensions/abl_link/include
#cgo LDFLAGS: -L${SRCDIR}/../../third_party/link/lib -labl_link -lstdc++
#cgo windows LDFLAGS: -lws2_32 -liphlpapi -lwinmm
#cgo !windows LDFLAGS: -lpthread

#include <stdlib.h>
#include "abl_link.h"
*/
import "C"

import (
	"sync"
	"time"
)

// link is the real cgo-backed Session wrapping one abl_link instance + an app-side session
// state buffer. All access is serialized under mu (abl_link capture/commit on the app timeline
// is not internally synchronized for concurrent writers).
type link struct {
	mu      sync.Mutex
	l       C.abl_link               // the Link instance (owns discovery + the shared timeline)
	st      C.abl_link_session_state // reusable app-side session-state scratch buffer
	quantum float64
	closed  bool
}

// NewLink creates a real Link session at the given quantum (phrase beats), initially disabled
// (call SetEnabled(true) to join). Tempo starts at 120 until the DJ bridge sets it.
func NewLink(quantum float64) (Session, error) {
	if quantum <= 0 {
		quantum = DefaultQuantum
	}
	k := &link{
		l:       C.abl_link_create(120.0),
		st:      C.abl_link_create_session_state(),
		quantum: quantum,
	}
	return k, nil
}

func (k *link) Available() bool { return true }

func (k *link) SetEnabled(on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	C.abl_link_enable(k.l, C.bool(on))
}

// micros returns Link's shared clock in microseconds (caller holds mu).
func (k *link) micros() C.int64_t { return C.abl_link_clock_micros(k.l) }

func (k *link) State(now time.Time) State {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return State{Quantum: k.quantum}
	}
	q := C.double(k.quantum)
	t := k.micros()
	C.abl_link_capture_app_session_state(k.l, k.st)
	return State{
		Available: true,
		Enabled:   bool(C.abl_link_is_enabled(k.l)),
		Tempo:     float64(C.abl_link_tempo(k.st)),
		Beat:      float64(C.abl_link_beat_at_time(k.st, t, q)),
		Phase:     float64(C.abl_link_phase_at_time(k.st, t, q)),
		Quantum:   k.quantum,
		Peers:     int(C.abl_link_num_peers(k.l)),
		Playing:   bool(C.abl_link_is_playing(k.st)),
		At:        now,
	}
}

func (k *link) SetTempo(bpm float64, _ time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed || bpm <= 0 {
		return
	}
	C.abl_link_capture_app_session_state(k.l, k.st)
	C.abl_link_set_tempo(k.st, C.double(bpm), k.micros())
	C.abl_link_commit_app_session_state(k.l, k.st)
}

func (k *link) ForceBeat(beat float64, _ time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	C.abl_link_capture_app_session_state(k.l, k.st)
	C.abl_link_force_beat_at_time(k.st, C.double(beat), C.uint64_t(k.micros()), C.double(k.quantum))
	C.abl_link_commit_app_session_state(k.l, k.st)
}

func (k *link) RequestBeat(beat float64, _ time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	C.abl_link_capture_app_session_state(k.l, k.st)
	C.abl_link_request_beat_at_time(k.st, C.double(beat), k.micros(), C.double(k.quantum))
	C.abl_link_commit_app_session_state(k.l, k.st)
}

func (k *link) SetQuantum(q float64) {
	if q <= 0 {
		return
	}
	k.mu.Lock()
	k.quantum = q
	k.mu.Unlock()
}

func (k *link) SetStartStopSyncEnabled(on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	C.abl_link_enable_start_stop_sync(k.l, C.bool(on))
}

func (k *link) SetPlaying(playing bool, _ time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	C.abl_link_capture_app_session_state(k.l, k.st)
	C.abl_link_set_is_playing(k.st, C.bool(playing), C.uint64_t(k.micros()))
	C.abl_link_commit_app_session_state(k.l, k.st)
}

func (k *link) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil
	}
	k.closed = true
	C.abl_link_destroy_session_state(k.st)
	C.abl_link_destroy(k.l)
	return nil
}
