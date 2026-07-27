//go:build windows && cgo

package mfenc

// Increment-3 gates for adapter-affinity resolution (item 3, risk R7). The probe policy is pure, so
// the rules that matter - bounded attempts, cached negatives, never retrying a non-source failure -
// are asserted without hardware. The live re-place is in inc3_measure_spout_windows_test.go.

import (
	"errors"
	"fmt"
	"testing"
)

func TestAffinityCandidatesExcludePrimaryAndDedupe(t *testing.T) {
	resetAffinity()
	got := affinityCandidates("s", 0x10, []int64{0x10, 0x20, 0x20, 0x30})
	want := []int64{0x20, 0x30}
	if len(got) != len(want) {
		t.Fatalf("candidates = %#x, want %#x", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %#x, want %#x", got, want)
		}
	}
	if len(affinityCandidates("s", 0x10, []int64{0x10})) != 0 {
		t.Fatal("a single-adapter machine must produce NO candidates (nothing to move to)")
	}
}

// TestAffinityCandidatesPreferCachedAdapter: the SECOND route on a sender must not re-probe every
// adapter - the known-good one goes first.
func TestAffinityCandidatesPreferCachedAdapter(t *testing.T) {
	resetAffinity()
	recordAffinity("cam", 0x30)
	got := affinityCandidates("cam", 0x10, []int64{0x10, 0x20, 0x30})
	if len(got) == 0 || got[0] != 0x30 {
		t.Fatalf("candidates = %#x, want the cached adapter 0x30 first", got)
	}
}

// TestAffinityNegativeIsCached: a sender no adapter can open must be probed ONCE. Without this a
// hopeless sender pays a full adapter sweep on every route open.
func TestAffinityNegativeIsCached(t *testing.T) {
	resetAffinity()
	calls := 0
	openOn := func(ProcOpts) (*ProcSession, error) {
		calls++
		return nil, ErrZeroCopyRefused
	}
	o := ProcOpts{LUID: 0x10, Spout: &SpoutSource{Name: "ghost"}}
	cands := []int64{0x20, 0x30}
	if _, _, err := replaceOnAffineAdapter(o, cands, openOn); !errors.Is(err, ErrZeroCopyRefused) {
		t.Fatalf("err = %v, want ErrZeroCopyRefused", err)
	}
	if calls != 2 {
		t.Fatalf("probed %d adapters, want exactly one attempt per candidate (2)", calls)
	}
	if !AdapterAffinityProbed("ghost") {
		t.Fatal("the negative result was not remembered")
	}
	if _, ok := AdapterAffinity("ghost"); ok {
		t.Fatal("a negative result must not report a usable adapter")
	}
	if got := AdapterMoves(); got != 0 {
		t.Fatalf("AdapterMoves = %d after only failures", got)
	}
}

// TestAffinityStopsOnNonSourceFailure: an encoder-side failure is not an affinity problem. Probing
// on would spawn a child per adapter for a rig that is simply broken.
func TestAffinityStopsOnNonSourceFailure(t *testing.T) {
	resetAffinity()
	calls := 0
	openOn := func(ProcOpts) (*ProcSession, error) {
		calls++
		return nil, fmt.Errorf("mfenc: poisoned tuple")
	}
	o := ProcOpts{LUID: 0x10, Spout: &SpoutSource{Name: "cam"}}
	if _, _, err := replaceOnAffineAdapter(o, []int64{0x20, 0x30, 0x40}, openOn); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("probed %d adapters after an ENCODER-side failure, want 1", calls)
	}
}

// TestAffinityNoCandidatesNeverMoves is the R7 hard rule expressed as a test: with no candidates
// the resolver refuses outright, so an explicitly pinned encode device can never be overridden.
func TestAffinityNoCandidatesNeverMoves(t *testing.T) {
	resetAffinity()
	called := false
	openOn := func(ProcOpts) (*ProcSession, error) { called = true; return nil, nil }
	o := ProcOpts{LUID: 0x10, Spout: &SpoutSource{Name: "cam"}}
	if _, _, err := replaceOnAffineAdapter(o, nil, openOn); !errors.Is(err, ErrZeroCopyRefused) {
		t.Fatalf("err = %v, want ErrZeroCopyRefused", err)
	}
	if called {
		t.Fatal("an open was attempted with no candidates - adapters must never move silently")
	}
	// Same rule for a readback session: affinity is a zero-copy-only concept.
	if _, _, err := replaceOnAffineAdapter(ProcOpts{LUID: 0x10}, []int64{0x20}, openOn); !errors.Is(err, ErrZeroCopyRefused) {
		t.Fatalf("err = %v, want ErrZeroCopyRefused for a non-zero-copy session", err)
	}
	if called {
		t.Fatal("a readback session must never be re-placed")
	}
}

// TestAffinitySuccessRecordsAndCounts: a successful move caches the adapter, counts, and marks the
// session so the route panel can show it.
func TestAffinitySuccessRecordsAndCounts(t *testing.T) {
	resetAffinity()
	openOn := func(o ProcOpts) (*ProcSession, error) {
		if o.LUID != 0x30 {
			return nil, ErrZeroCopyRefused
		}
		return &ProcSession{child: &procChild{luid: 0x30}, zeroCopy: true}, nil
	}
	o := ProcOpts{LUID: 0x10, Spout: &SpoutSource{Name: "cam"}}
	s, luid, err := replaceOnAffineAdapter(o, []int64{0x20, 0x30, 0x40}, openOn)
	if err != nil {
		t.Fatalf("re-place failed: %v", err)
	}
	if luid != 0x30 {
		t.Fatalf("landed on %#x, want 0x30", luid)
	}
	if !s.AdapterMoved() || s.movedFrom != 0x10 {
		t.Fatalf("session does not record the move (movedFrom=%#x)", s.movedFrom)
	}
	if s.AdapterLUID() != 0x30 {
		t.Fatalf("AdapterLUID = %#x, want 0x30", s.AdapterLUID())
	}
	if got, ok := AdapterAffinity("cam"); !ok || got != 0x30 {
		t.Fatalf("affinity cache = (%#x,%v), want (0x30,true)", got, ok)
	}
	if got := AdapterMoves(); got != 1 {
		t.Fatalf("AdapterMoves = %d, want 1", got)
	}
	// The 4th candidate must not have been touched after success.
	resetAffinity()
}
