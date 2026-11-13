package mediaroute

import (
	"errors"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gpumem"
	"rave.page/mate/internal/logbus"
)

// attemptManager: the sender gate always answers "verified attempt" (tight band); headroom AFTER the
// allocation is driven by *after. Clock is injected.
func attemptManager(sf *sinkFactory, after *uint64, now *time.Time) *Manager {
	m := New(Options{
		Log: logbus.New(32), Router: newFakeRouter(),
		Cfg:      func() config.MediaLinkFeature { return config.MediaLinkFeature{} },
		OpenSink: sf.open,
		Headroom: func() gpumem.Headroom { return gpumem.Headroom{Present: true, FreeMB: *after, BudgetMB: 12288} },
		AdmitSender: func(w, h int) Admission {
			return Admission{Allow: true, Attempt: true, Policy: "balanced", FreeMB: *after, NeedMB: 112, RequiredMB: 176}
		},
	})
	m.now = func() time.Time { return *now }
	return m
}

// A verified attempt whose allocation collapses free VRAM is backed out at once (the sender is closed
// before a decoder or Resolume builds on it), and the next attempt waits out the cooldown - a full card
// never churns a sender every 2 s re-check.
func TestVerifiedAttemptBacksOutAndCoolsDown(t *testing.T) {
	sf := &sinkFactory{shared: true}
	after := uint64(20) // collapse
	now := time.Unix(1_000_000, 0)
	m := attemptManager(sf, &after, &now)
	name := linkPrefix + "VRC"
	if _, err := m.openReceiveSink(name, 3840, 2160); err == nil {
		t.Fatal("collapsed attempt must fail")
	}
	if sf.opens != 1 || sf.sinks[0].closed != 1 {
		t.Fatalf("attempt must open once and back out: opens=%d closed=%d", sf.opens, sf.sinks[0].closed)
	}
	// Within the cooldown and no headroom rise: refused WITHOUT another allocation.
	now = now.Add(10 * time.Second)
	if _, err := m.openReceiveSink(name, 3840, 2160); err == nil {
		t.Fatal("attempt inside cooldown must be refused")
	}
	if sf.opens != 1 {
		t.Fatalf("cooldown must not allocate: opens=%d", sf.opens)
	}
	// Headroom rises by >= need: re-armed immediately, and a healthy after-read keeps the sender.
	after = 20 + 200
	if _, err := m.openReceiveSink(name, 3840, 2160); err != nil {
		t.Fatalf("headroom rise must re-arm the attempt: %v", err)
	}
	if sf.opens != 2 || sf.sinks[1].closed != 0 {
		t.Fatalf("verified attempt must keep the sender: opens=%d", sf.opens)
	}
	m.mu.Lock()
	_, left := m.attempts[name]
	m.mu.Unlock()
	if left {
		t.Fatal("a successful attempt must clear the failure record")
	}
}

// After attemptMaxPerCycle failures, only a headroom rise re-arms (the cooldown alone does not).
func TestVerifiedAttemptMaxPerCycle(t *testing.T) {
	sf := &sinkFactory{shared: true}
	after := uint64(20)
	now := time.Unix(2_000_000, 0)
	m := attemptManager(sf, &after, &now)
	name := linkPrefix + "VRC"
	for i := 0; i < attemptMaxPerCycle; i++ {
		_, _ = m.openReceiveSink(name, 64, 48)
		now = now.Add(attemptCooldown + time.Second)
	}
	n := sf.opens
	_, _ = m.openReceiveSink(name, 64, 48)
	if sf.opens != n {
		t.Fatalf("after %d failures the cooldown must not re-arm: opens %d -> %d", attemptMaxPerCycle, n, sf.opens)
	}
}

// A GPU-destination failure that is VRAM/interop-shaped must not drop to the CPU frame path.
func TestNoFramePathFallbackUnderPressure(t *testing.T) {
	m := New(Options{Log: logbus.New(8), Router: newFakeRouter(),
		Cfg: func() config.MediaLinkFeature { return config.MediaLinkFeature{} },
		AdmitSender: func(w, h int) Admission {
			return Admission{Allow: true, Reason: "ok"}
		}})
	if _, hold := m.noFramePathFallback(errors.New("videoshare: sender \"x\" has no GPU destination texture: GL/DX interop unavailable (Win32 error 0x8876017c)"), 64, 48); !hold {
		t.Fatal("interop failure must hold, not fall back")
	}
	if _, hold := m.noFramePathFallback(errors.New("no OpenGL context"), 64, 48); hold {
		t.Fatal("non-VRAM failure with comfortable budget may use the frame path")
	}
	m.admitSndr = func(w, h int) Admission { return Admission{Allow: true, Attempt: true, Reason: "tight"} }
	if _, hold := m.noFramePathFallback(errors.New("no OpenGL context"), 64, 48); !hold {
		t.Fatal("tight budget must hold, not fall back")
	}
}

func TestHoldText(t *testing.T) {
	now := time.Unix(3_000_000, 0)
	h := Hold{Sender: "rave-mate link VRC", Since: now.Add(-90 * time.Second), FreeMB: 140, NeedMB: 226, RequiredMB: 290,
		Policy: "balanced", Reason: "insufficient VRAM", NextRetry: now.Add(2 * time.Second)}
	s := FormatHolds([]Hold{h}, now)
	for _, want := range []string{"HELD", "free 140 MB", "needs 226 MB", "required 290 MB", "policy balanced", "1m30s"} {
		if !contains(s, want) {
			t.Fatalf("hold text missing %q:\n%s", want, s)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
