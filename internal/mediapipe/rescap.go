package mediapipe

import (
	"sync"
	"time"
)

// rescap.go - shared live resolution-cap plumbing for BOTH encode engines (the native MF bridge and
// the ffmpeg substitute), so the receiver's VRAM MaxHeight hint applies the same way and cannot flap.
// A resolution change is the most visible degradation and the most expensive to apply (encoder MFT
// re-type / child respawn), so it is rate-limited and a RESTORE needs the pressure to have been gone
// for a sustained window.

const (
	// resCapMinChangeGap: at most one coded-resolution change per this long (anti-flap).
	resCapMinChangeGap = 10 * time.Second
	// resCapRestoreHold: a relax (toward full res) applies only after the relaxed request has held
	// for this long - so a brief dip back under pressure does not bounce the resolution up and down.
	resCapRestoreHold = 10 * time.Second
)

// resCapGate rate-limits live coded-resolution-cap changes. Values are effective coded HEIGHTS where
// LARGER = less cap; a caller with a 0 ("no cap") sentinel normalises it to the source height first.
// ENGAGE (a smaller height, more cap) applies as soon as the min gap allows; RESTORE (a larger
// height, less cap) applies only after the relaxed request has held for resCapRestoreHold. Shared by
// mfBridge and encoder, each with its own instance seeded at its uncapped baseline.
type resCapGate struct {
	mu          sync.Mutex
	now         func() time.Time // injectable clock (tests)
	applied     int              // currently-applied effective height
	lastChange  time.Time
	pendingH    int // a relax target awaiting its sustained-hold window (0 = none pending)
	pendingFrom time.Time
}

func newResCapGate(baseline int) *resCapGate {
	return &resCapGate{now: time.Now, applied: baseline}
}

// decide reports the effective height to apply for a requested cap and whether it changed. wantH is a
// real height (larger = less cap); the caller passes its uncapped baseline when the hint is lifted.
func (g *resCapGate) decide(wantH int) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if wantH == g.applied {
		g.pendingH = 0 // the request matches reality: cancel any pending relax
		return g.applied, false
	}
	gapOK := g.lastChange.IsZero() || now.Sub(g.lastChange) >= resCapMinChangeGap
	if wantH < g.applied {
		// ENGAGE / deepen the cap: apply as soon as the min gap allows (pressure is now).
		g.pendingH = 0
		if !gapOK {
			return g.applied, false
		}
		g.applied, g.lastChange = wantH, now
		return g.applied, true
	}
	// RESTORE / relax (wantH > applied): require the relaxed request to hold for resCapRestoreHold.
	if g.pendingH != wantH {
		g.pendingH, g.pendingFrom = wantH, now
		return g.applied, false
	}
	if now.Sub(g.pendingFrom) < resCapRestoreHold || !gapOK {
		return g.applied, false
	}
	g.applied, g.lastChange, g.pendingH = wantH, now, 0
	return g.applied, true
}

// effCapHeight normalises a MaxHeight (0 = no cap) to a real effective coded height for the gate:
// 0 or a value at/above the source becomes the source height (the uncapped extreme).
func effCapHeight(maxHeight, srcHeight int) int {
	if maxHeight <= 0 || maxHeight >= srcHeight {
		return srcHeight
	}
	return maxHeight
}

// codedSizeForCapHeight computes the even, aspect-exact coded size for a target cap height, scaling
// from the SOURCE geometry. At/above the base coded height it returns the exact base (restore / no
// cap); it never upscales above the base (which is itself <= source). Pure - the device-free half of
// the native live cap.
func codedSizeForCapHeight(srcW, srcH, baseW, baseH, targetH int) (int, int) {
	if srcH <= 0 || baseH <= 0 || targetH <= 0 || targetH >= baseH {
		return baseW, baseH
	}
	h := targetH &^ 1
	w := (srcW * targetH / srcH) &^ 1
	if h < 2 {
		h = 2
	}
	if w < 2 {
		w = 2
	}
	if h >= baseH {
		return baseW, baseH
	}
	return w, h
}
