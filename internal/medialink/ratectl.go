package medialink

import (
	"context"
	"time"
)

// govHealthyMult: free VRAM must reach floor*this to be treated as fully healthy (hysteresis above
// the floor where tier-1 trimming begins), so the governor does not oscillate at the boundary.
const govHealthyMult = 2

// Rate-ladder floors: never trim below these or the stream becomes useless.
const (
	minGovKbps = 1000
	minGovFPS  = 10
	// govDefaultFloorMB is the free-VRAM floor the governor protects when none is configured.
	govDefaultFloorMB = 1024
)

// OnResolutionCapChange, when non-nil, is called whenever the governor changes the resolution cap
// (engage or lift). App wires it to mfenc.NoteDecodeResizeGrace so the local decoder tolerates the
// brief re-init stall when the coded size changes, instead of recycling a healthy pipeline. nil-safe.
var OnResolutionCapChange func()

// govResolutionCapH is the last-resort resolution cap: at the DEEPEST pressure tier the governor
// caps the sender to 1080p. A route already at/below 1080p is unaffected (the sender only scales a
// taller source), so the most-visible degradation only ever hits the highest-res routes under the
// worst pressure - exactly where the alternative is a held/frozen pipeline.
const govResolutionCapH = 1080

// rateLadder maps free VRAM to an ABSOLUTE RateHint: trim BITRATE first, then FRAMERATE, and - only
// at the deepest tier - RESOLUTION as the last resort (Decision 2026-11-11, superseding the earlier
// "never cut resolution" rule: under VRAM admission failure the lead chose a visible downscale over a
// held/frozen pipeline; see .devnotes/VRAM_BUDGET_AND_PLANNER). MaxHeight caps the CODED resolution
// ONLY: the sender scales down before encode; the receiver scales back up into its unchanged
// nominal-size destination, so the republished geometry Resolume sees never changes (Decision
// 2026-11-11: "resolution cap = coded resolution only; destination geometry is invariant"). base* are
// the route's negotiated ceilings (the healthy target the hint restores on recovery). Pure, so the
// whole ladder is tested off-set; MaxHeight is omitempty so old peers ignore it.
func rateLadder(freeMB, floorMB uint64, baseKbps, baseFPS int, stream uint16) RateHint {
	if floorMB == 0 {
		floorMB = govDefaultFloorMB
	}
	h := RateHint{Type: MetaRate, Stream: stream, MaxBitrateKbps: baseKbps, MaxFPS: baseFPS}
	switch {
	case freeMB >= floorMB*govHealthyMult: // healthy: full quality (restore base)
	case freeMB >= floorMB: // tier 1: trim bitrate only
		h.MaxBitrateKbps = govKbps(baseKbps, 60)
	case freeMB >= floorMB/2: // tier 2: bitrate + framerate (resolution still untouched)
		h.MaxBitrateKbps = govKbps(baseKbps, 35)
		h.MaxFPS = govFPS(baseFPS, 2)
	default: // tier 3: hard trim + resolution cap (last resort)
		h.MaxBitrateKbps = govKbps(baseKbps, 20)
		h.MaxFPS = govFPS(baseFPS, 3)
		h.MaxHeight = govResolutionCapH
	}
	return h
}

// govKbps returns pct% of base, floored at minGovKbps (or base if base is already below the floor).
func govKbps(base, pct int) int {
	v := base * pct / 100
	if v >= minGovKbps {
		return v
	}
	if base < minGovKbps {
		return base
	}
	return minGovKbps
}

// govFPS returns base/div, floored at minGovFPS (or base if base is already below the floor).
func govFPS(base, div int) int {
	if base <= 0 || div <= 0 {
		return base
	}
	v := base / div
	if v >= minGovFPS {
		return v
	}
	if base < minGovFPS {
		return base
	}
	return minGovFPS
}

// rateGovernor is the recv-side (DJ-PC) VRAM congestion controller: it watches primary-adapter
// headroom and emits MetaRate backpressure so the sender trims output (bitrate first, then fps;
// resolution last-resort, unwired). Runs only for compressed video routes with a known base
// bitrate. Emits ONLY on a change (dedup), including restoring the base on recovery; fails open
// (governor off or headroom unknown => never emits, sender stays at full).
func (rm *RouteManager) rateGovernor(ctx context.Context, rio *routeIO, stream uint16, budget func(codedH int) int, baseFPS int) {
	baseKbps := budget(0)
	if rm.headroom == nil || baseKbps <= 0 {
		return
	}
	assertBudgetSane(rm, stream, baseKbps)
	t := time.NewTicker(rm.reportEvery)
	defer t.Stop()
	var last RateHint
	var sent bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if rm.governorOn != nil && !rm.governorOn() {
			continue // disabled: leave the sender at full (never emit)
		}
		free, present := rm.headroom()
		if !present {
			continue // headroom unknown: fail open
		}
		h := rateLadder(free, rm.govFloorMB, budget(0), baseFPS, stream)
		capBudgetToCoded(&h, budget)                                                                            // item 2: a resolution cap lowers the bitrate to the coded-size model
		rio.st.setBudget(rm.budgetModeStr(), h.MaxBitrateKbps, baseKbps, 0, budgetReason(h, baseKbps, baseFPS)) // item 5
		if sent && h == last {
			continue
		}
		capChanged := h.MaxHeight != last.MaxHeight // includes the first emit (last zero-value)
		if rio.writeMeta(h, rm.clock.Now()) != nil {
			return
		}
		last, sent = h, true
		if capChanged && OnResolutionCapChange != nil {
			OnResolutionCapChange() // coded size about to change downstream: grant the decoder re-init grace
		}
		rm.infof("rate governor", map[string]any{"stream": stream, "freeMB": free,
			"maxKbps": h.MaxBitrateKbps, "maxFPS": h.MaxFPS, "baseKbps": baseKbps, "baseFPS": baseFPS})
	}
}

// RateControlSource is an optional Source extension (the §2.5 sibling of KeyframeSource): the route
// caps the encoder's output when the receiver sends a MetaRate hint - the DJ-PC VRAM governor's
// backpressure. Encoder-backed video sources implement it; a source without it ignores rate hints,
// so an older sender degrades cleanly. Bitrate is a live lever on every encoder; height/fps are
// best-effort.
type RateControlSource interface {
	Source
	SetRateHint(RateHint)
}
