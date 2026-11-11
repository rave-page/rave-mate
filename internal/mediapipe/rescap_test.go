package mediapipe

import (
	"testing"
	"time"
)

func TestResCapGateEngageIsImmediateRestoreIsHeld(t *testing.T) {
	now := time.Unix(0, 0)
	g := newResCapGate(2160) // uncapped baseline = source height
	g.now = func() time.Time { return now }

	// ENGAGE to 1080 applies at once (first change, gap not yet started).
	if v, changed := g.decide(1080); !changed || v != 1080 {
		t.Fatalf("engage: got (%d,%v) want (1080,true)", v, changed)
	}
	// A relax request back to 2160 does NOT apply immediately - it must hold.
	if v, changed := g.decide(2160); changed || v != 1080 {
		t.Fatalf("relax before hold: got (%d,%v) want (1080,false)", v, changed)
	}
	// Still within the restore-hold window: no change.
	now = now.Add(resCapRestoreHold - time.Second)
	if _, changed := g.decide(2160); changed {
		t.Fatal("relax applied before the hold window elapsed")
	}
	// Past both the hold and the min-gap: the restore applies.
	now = now.Add(2 * time.Second)
	if v, changed := g.decide(2160); !changed || v != 2160 {
		t.Fatalf("relax after hold: got (%d,%v) want (2160,true)", v, changed)
	}
}

func TestResCapGateMinGapRateLimitsEngage(t *testing.T) {
	now := time.Unix(0, 0)
	g := newResCapGate(2160)
	g.now = func() time.Time { return now }
	if _, changed := g.decide(1440); !changed {
		t.Fatal("first engage must apply")
	}
	// A deeper cap within the min gap is rejected (one change per gap).
	now = now.Add(resCapMinChangeGap - time.Second)
	if v, changed := g.decide(1080); changed || v != 1440 {
		t.Fatalf("engage within gap: got (%d,%v) want (1440,false)", v, changed)
	}
	// After the gap it applies.
	now = now.Add(2 * time.Second)
	if v, changed := g.decide(1080); !changed || v != 1080 {
		t.Fatalf("engage after gap: got (%d,%v) want (1080,true)", v, changed)
	}
}

func TestResCapGateDedupNoChange(t *testing.T) {
	g := newResCapGate(1080)
	g.now = func() time.Time { return time.Unix(100, 0) }
	if _, changed := g.decide(1080); changed {
		t.Fatal("request equal to applied must not change")
	}
}

func TestEffCapHeight(t *testing.T) {
	if got := effCapHeight(0, 2160); got != 2160 {
		t.Fatalf("no cap -> source: %d", got)
	}
	if got := effCapHeight(1080, 2160); got != 1080 {
		t.Fatalf("real cap: %d", got)
	}
	if got := effCapHeight(4320, 2160); got != 2160 {
		t.Fatalf("cap above source clamps to source: %d", got)
	}
}

func TestCodedSizeForCapHeight(t *testing.T) {
	// 3840x2160 source, base == source, cap to 1080 -> aspect-exact even 1920x1080.
	if w, h := codedSizeForCapHeight(3840, 2160, 3840, 2160, 1080); w != 1920 || h != 1080 {
		t.Fatalf("4K->1080: %dx%d want 1920x1080", w, h)
	}
	// Restore (target >= base) returns the exact base, no rounding drift.
	if w, h := codedSizeForCapHeight(3840, 2160, 3840, 2160, 2160); w != 3840 || h != 2160 {
		t.Fatalf("restore: %dx%d want 3840x2160", w, h)
	}
	// Odd target height rounds down to even; width stays even + aspect-exact.
	if w, h := codedSizeForCapHeight(1280, 720, 1280, 720, 481); w%2 != 0 || h%2 != 0 || h != 480 {
		t.Fatalf("odd target: %dx%d want even, h=480", w, h)
	}
	// A base already below the source: a cap above the base cannot upscale.
	if w, h := codedSizeForCapHeight(3840, 2160, 1280, 720, 1080); w != 1280 || h != 720 {
		t.Fatalf("cap above base must not upscale: %dx%d want 1280x720", w, h)
	}
}
