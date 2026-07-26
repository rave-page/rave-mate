//go:build windows

package midi

import (
	"fmt"
	"testing"
)

// TestFailedOpenLeaksNothing proves the WP-8 callback fix: repeated midiInOpen failures
// (the MMSYSERR_ALLOCATED retry storm) allocate at most ONE NewCallback trampoline per
// process and leave no registry entries behind.
func TestFailedOpenLeaksNothing(t *testing.T) {
	origOpen, origStart := midiInOpenCall, midiInStartCall
	defer func() { midiInOpenCall, midiInStartCall = origOpen, origStart }()

	base := registeredInputs()
	midiInOpenCall = func(_ *uintptr, _, _, _ uintptr) uintptr { return 4 } // MMSYSERR_ALLOCATED

	for i := 0; i < 50; i++ {
		if _, err := openWinmm(0, fmt.Sprintf("fake-%d", i)); err == nil {
			t.Fatal("open must fail")
		}
	}
	if got := registeredInputs(); got != base {
		t.Errorf("registry leaked %d entries across failed opens", got-base)
	}
	if n := cbAllocs.Load(); n > 1 {
		t.Errorf("trampoline allocated %d times, want <=1 per process", n)
	}

	// midiInStart failure path must unregister too.
	midiInOpenCall = func(h *uintptr, _, _, _ uintptr) uintptr { *h = 0xBAD; return 0 }
	midiInStartCall = func(_ uintptr) uintptr { return 5 } // MMSYSERR_INVALHANDLE-ish
	for i := 0; i < 10; i++ {
		if _, err := openWinmm(0, "fake-start"); err == nil {
			t.Fatal("start must fail")
		}
	}
	if got := registeredInputs(); got != base {
		t.Errorf("registry leaked %d entries across failed starts", got-base)
	}
	if n := cbAllocs.Load(); n > 1 {
		t.Errorf("trampoline allocated %d times, want <=1 per process", n)
	}
}

// TestOpenCloseRegistry proves a successful open registers exactly one entry and Close
// removes it (no growth across open/close churn).
func TestOpenCloseRegistry(t *testing.T) {
	origOpen, origStart := midiInOpenCall, midiInStartCall
	defer func() { midiInOpenCall, midiInStartCall = origOpen, origStart }()
	midiInOpenCall = func(h *uintptr, _, _, _ uintptr) uintptr { *h = 0; return 0 }
	midiInStartCall = func(_ uintptr) uintptr { return 0 }

	base := registeredInputs()
	for i := 0; i < 20; i++ {
		in, err := openWinmm(0, "fake-ok")
		if err != nil {
			t.Fatal(err)
		}
		if got := registeredInputs(); got != base+1 {
			t.Fatalf("after open: registry %d, want %d", got, base+1)
		}
		_ = in.Close()
		if got := registeredInputs(); got != base {
			t.Fatalf("after close: registry %d, want %d", got, base)
		}
	}
}
