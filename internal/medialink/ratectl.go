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

// rateLadder maps free VRAM to an ABSOLUTE RateHint per the owner's ladder: trim BITRATE first,
// then FRAMERATE. RESOLUTION is the last resort and is deliberately NOT touched here (its live op is
// unwired and it is the most visible degradation - a resolution cut re-plans the encode pipeline).
// base* are the route's negotiated ceilings (the healthy target the hint restores on recovery).
// Pure, so the whole ladder is testable off-set.
func rateLadder(freeMB, floorMB uint64, baseKbps, baseFPS int, stream uint16) RateHint {
	if floorMB == 0 {
		floorMB = govDefaultFloorMB
	}
	h := RateHint{Type: MetaRate, Stream: stream, MaxBitrateKbps: baseKbps, MaxFPS: baseFPS}
	switch {
	case freeMB >= floorMB*govHealthyMult: // healthy: full quality (restore base)
	case freeMB >= floorMB: // tier 1: trim bitrate only
		h.MaxBitrateKbps = govKbps(baseKbps, 60)
	case freeMB >= floorMB/2: // tier 2: bitrate + framerate
		h.MaxBitrateKbps = govKbps(baseKbps, 35)
		h.MaxFPS = govFPS(baseFPS, 2)
	default: // tier 3: hard trim (resolution STILL untouched - last resort)
		h.MaxBitrateKbps = govKbps(baseKbps, 20)
		h.MaxFPS = govFPS(baseFPS, 3)
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
func (rm *RouteManager) rateGovernor(ctx context.Context, rio *routeIO, stream uint16, baseKbps, baseFPS int) {
	if rm.headroom == nil || baseKbps <= 0 {
		return
	}
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
		h := rateLadder(free, rm.govFloorMB, baseKbps, baseFPS, stream)
		if sent && h == last {
			continue
		}
		if rio.writeMeta(h, rm.clock.Now()) != nil {
			return
		}
		last, sent = h, true
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
