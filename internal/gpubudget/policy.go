package gpubudget

import (
	"fmt"
	"strings"

	"rave.page/mate/internal/gpumem"
)

// Mode is the admission preset (config mediaLink.vramPolicy). See AdmitNeed and
// .devnotes/VRAM_FULL_CARD_POLICY.md.
type Mode string

const (
	// ModeStrict is the pre-2026-11-13 rule: free - cost >= geometry reserve (768..1536 MB).
	ModeStrict Mode = "strict"
	// ModeBalanced (default): evidence-based need + margin, verified attempt in the tight band.
	ModeBalanced Mode = "balanced"
	// ModePermissive attempts (verified) whenever the need fits at all.
	ModePermissive Mode = "permissive"
)

// ParseMode maps a config string to a Mode; unknown/empty => balanced (ok=false for unknown non-empty).
func ParseMode(s string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeStrict:
		return ModeStrict, true
	case ModePermissive:
		return ModePermissive, true
	case ModeBalanced, "":
		return ModeBalanced, true
	}
	return ModeBalanced, false
}

// Resolved returns the effective mode ("" and unknown => balanced).
func (m Mode) Resolved() Mode {
	r, _ := ParseMode(string(m))
	return r
}

// Margin defaults: a margin is slack for estimate error + other apps' jitter, so it scales with what we
// allocate - not a fixed guess about what Resolume might need. 64 MB floor covers the device/driver
// objects no surface count sees; 25 % covers the estimate error observed between computed and learned
// costs (4K decode estimate 343 MB vs measured 56-114 MB: estimates err HIGH, so 25 % is conservative).
const (
	DefaultMarginMinMB uint64  = 64
	DefaultMarginPct   float64 = 0.25
)

// MarginMB = max(MarginMinMB, MarginPct * need).
func MarginMB(need float64, pol Policy) uint64 {
	minMB := pol.MarginMinMB
	if minMB == 0 {
		minMB = DefaultMarginMinMB
	}
	pct := pol.MarginPct
	if pct <= 0 {
		pct = DefaultMarginPct
	}
	m := uint64(need*pct + 0.5)
	if m < minMB {
		m = minMB
	}
	return m
}

// FollowCostMB is what a NEW sender of g makes a GL receiver (Resolume) allocate when it follows it:
// one RGBA frame for its composition copy (the shared texture itself is opened, not copied). A kept
// sender (same handle) costs nothing new - callers pass 0 for it.
func FollowCostMB(g Geom) float64 {
	px := float64(g.Pixels())
	if px <= 0 {
		px = 1920 * 1080
	}
	return px * bppRGBA / mib
}

// CollapseFloorMB: after a verified attempt, free VRAM below this means the card is at the edge where
// driver-resident allocations (interop) start failing - back out.
const CollapseFloorMB uint64 = 48

// VerifyAttempt checks headroom AFTER a successful tight-band allocation. ok=false => the caller backs
// the allocation out and holds. Unknown headroom after => ok (fail open, like admission).
func VerifyAttempt(after gpumem.Headroom) (ok bool, reason string) {
	if !after.Present {
		return true, "headroom unknown after attempt - kept"
	}
	if after.FreeMB < CollapseFloorMB {
		return false, fmt.Sprintf("free VRAM collapsed to %d MB (< %d MB) after the allocation", after.FreeMB, CollapseFloorMB)
	}
	return true, fmt.Sprintf("verified: free %d MB after the allocation", after.FreeMB)
}
