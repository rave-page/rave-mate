//go:build windows && cgo

package mfenc

import (
	"testing"
	"time"
)

// fakeClock drives the ledger's nowFn so a multi-minute crash streak is asserted in microseconds.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func withClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{t: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
	prev := nowFn
	nowFn = c.now
	ResetPoison()
	t.Cleanup(func() {
		nowFn = prev
		ResetPoison()
	})
	return c
}

// TestPoisonSurvivesRouteLifetimes is THE regression test for the field defect: the log said
// "consecutive fails 1" on every route, so "poison after 3 fast-fails → ffmpeg" could never fire
// and an AV loop degraded to nothing instead of degrading to a working encoder.
//
// NON-VACUOUS: crashes here are 2 MINUTES apart, i.e. far outside the old 30 s crashWindow that
// was measured from the child's LAST SPAWN. The old counter reset to 1 at every one of them and
// would report fails=1,1,1 and never poison. This asserts 1,2,3 + poisoned.
func TestPoisonSurvivesRouteLifetimes(t *testing.T) {
	c := withClock(t)
	const luid = 0x18ed4
	const enc = "AMDh264Encoder"

	var got []int
	for i := 0; i < 3; i++ {
		if i > 0 {
			// Human-paced route retry: the child already respawned right after the previous
			// crash, so "time since last spawn" is large. Only a CRASH-TO-CRASH streak survives.
			c.add(2 * time.Minute)
		}
		fails, _ := NoteCrash(luid, enc, "ProcessInput")
		got = append(got, fails)
	}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("crash streak did not accumulate across route lifetimes: got %v, want [1 2 3]", got)
	}
	reason, bad := PoisonedTuple(luid, enc)
	if !bad {
		t.Fatal("3 crashes did not poison (adapter, encoder) - the safety net still cannot engage")
	}
	if reason == "" {
		t.Fatal("poisoned with no reason string - the route panel would show a silent degrade")
	}
}

// TestPoisonCountsCrashWithNoLiveSessions covers the second hole: poison entries used to be
// written only for sessions still registered at crash time, so an AV during teardown (which is
// exactly when the field crash was reported) poisoned nothing even at the limit. The ledger is
// updated from the child exit path with whatever encoder name is known, live sessions or not.
func TestPoisonCountsCrashWithNoLiveSessions(t *testing.T) {
	c := withClock(t)
	const luid = 0x163a8
	// encoder name unknown (crash during teardown, nothing left to ask): still one ledger row.
	for i := 0; i < failLimit; i++ {
		c.add(time.Second)
		NoteCrash(luid, "", "Release(IMFTransform)")
	}
	reason, bad := PoisonedTuple(luid, "")
	if !bad {
		t.Fatalf("teardown-time crashes never poisoned: %q", reason)
	}
}

// TestStreakRestartsAfterQuietWindow pins the STATED reset policy: a crash far outside failWindow
// starts a new streak rather than extending an old one, so an occasional unrelated fault years
// apart never accumulates into a poison.
func TestStreakRestartsAfterQuietWindow(t *testing.T) {
	c := withClock(t)
	const luid, enc = 1, "NVIDIA H.264 Encoder MFT"
	if fails, _ := NoteCrash(luid, enc, ""); fails != 1 {
		t.Fatalf("first crash fails=%d, want 1", fails)
	}
	c.add(failWindow + time.Minute)
	fails, poisoned := NoteCrash(luid, enc, "")
	if fails != 1 || poisoned {
		t.Fatalf("crash outside failWindow extended the streak: fails=%d poisoned=%v", fails, poisoned)
	}
}

// TestPoisonNeedsProofOfHealthToClear pins the other half of the policy: quiet time ALONE never
// un-poisons (that would re-arm a crash loop on a timer). Only "it really produced output" plus
// quiet time clears it.
func TestPoisonNeedsProofOfHealthToClear(t *testing.T) {
	c := withClock(t)
	const luid, enc = 2, "Intel Quick Sync Video H.264 Encoder MFT"
	for i := 0; i < failLimit; i++ {
		c.add(time.Second)
		NoteCrash(luid, enc, "")
	}
	if _, bad := PoisonedTuple(luid, enc); !bad {
		t.Fatal("setup: expected a poisoned tuple")
	}
	c.add(forgetAfter + time.Minute) // lots of quiet time, no proof of health
	if _, bad := PoisonedTuple(luid, enc); !bad {
		t.Fatal("quiet time alone un-poisoned the tuple - that re-arms the crash loop on a timer")
	}
	NoteHealthy(luid, enc) // a session on this tuple really produced an AU
	c.add(forgetAfter + time.Minute)
	if reason, bad := PoisonedTuple(luid, enc); bad {
		t.Fatalf("proof of health + quiet time did not clear the poison: %q", reason)
	}
}

// TestPoisonIsPerEncoderNotPerAdapter: poisoning the hardware MFT must NOT poison the software
// tier on the same adapter - that is the rung that keeps a poisoned adapter producing video.
func TestPoisonIsPerEncoderNotPerAdapter(t *testing.T) {
	c := withClock(t)
	const luid = 3
	for i := 0; i < failLimit; i++ {
		c.add(time.Second)
		NoteCrash(luid, "AMDh264Encoder", "ProcessInput")
	}
	if _, bad := PoisonedTuple(luid, "AMDh264Encoder"); !bad {
		t.Fatal("setup: hardware tuple should be poisoned")
	}
	if reason, bad := PoisonedTuple(luid, swEncoderKey); bad {
		t.Fatalf("poisoning the hardware MFT also poisoned the software tier (%q) - a poisoned adapter would lose video entirely", reason)
	}
}

// TestPoisonedOnPredictsFromLastBoundEncoder: an open must be able to consult the ledger BEFORE
// the child reports which MFT it bound, which is what NoteEncoder's per-adapter memory is for.
func TestPoisonedOnPredictsFromLastBoundEncoder(t *testing.T) {
	c := withClock(t)
	const luid = 4
	if _, bad := PoisonedOn(luid); bad {
		t.Fatal("unknown adapter reported poisoned")
	}
	NoteEncoder(luid, "AMDh264Encoder")
	for i := 0; i < failLimit; i++ {
		c.add(time.Second)
		NoteCrash(luid, "AMDh264Encoder", "")
	}
	if _, bad := PoisonedOn(luid); !bad {
		t.Fatal("PoisonedOn did not see the poisoned encoder the child would bind again")
	}
}

// TestStageNamesCoverTheZigContract: the stage codes are a cross-process contract with
// native/zigenc (mf.zig Stage, pinned by a Zig test on that side). A code the child can latch but
// the parent cannot name produces an unattributed crash report - the exact gap this fixes.
func TestStageNamesCoverTheZigContract(t *testing.T) {
	// Every code mf.zig can store, mirrored here. Keep in lockstep with Stage in mf.zig.
	codes := []uint32{
		10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25,
		40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54,
		60, 61, 70, 71, 72, 73, 74, 75, 76, 77, 78,
	}
	for _, c := range codes {
		if n, ok := stageNames[c]; !ok || n == "" {
			t.Errorf("stage %d has no name: a crash there would be unattributed", c)
		}
	}
	if stageName(0) != "" {
		t.Errorf("stage 0 should render empty (nothing latched), got %q", stageName(0))
	}
	if got := stageName(46); got != "waiting for METransformNeedInput" {
		// This is the stage the AMD field failure sat in for 2 s per frame; if it ever stops being
		// nameable the next report loses its headline.
		t.Errorf("stage 46 = %q, want the async-wait name", got)
	}
	if got := stageName(9999); got == "" {
		t.Error("unknown stage rendered empty - a future child's codes must still be reportable")
	}
}
