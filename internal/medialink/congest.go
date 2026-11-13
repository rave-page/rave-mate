package medialink

import (
	"context"
	"time"
)

// defaultCongestCeilKbps is the controller ceiling when the route carries no bitrate budget.
const defaultCongestCeilKbps = 20000

// congest.go is the §-congestion controller (Unit 4): a receiver-side AIMD controller driven by
// datagram LOSS + a simplified GCC-style DELAY-GRADIENT over-use detector, producing a target
// bitrate bounded by the negotiated [floor, ceiling]. Bitrate is cut first, framerate only when
// bitrate is already pinned at the floor and congestion persists (the owner's ladder). On a clean
// LAN (zero loss, flat one-way delay) it climbs to and holds the ceiling without oscillating.
//
// The sender applies the target through the existing RateHint / MetaRate path (ratectl.go); the
// router combines this hint with the VRAM governor's hint via min() before emitting, so the two
// controllers compose (the VRAM agent owns ratectl.go; this file is independent).

// Thresholds. Loss below lossLow is left to NACK/retransmit (so 2% random loss does NOT back the
// bitrate off); the over-use detector ignores sub-millisecond LAN delay jitter.
const (
	ccLossLow        = 0.05              // below this, loss is NACK territory - no backoff
	ccLossHigh       = 0.15              // above this, hard multiplicative decrease
	ccOveruseNs      = int64(4_000_000)  // delay-gradient over-use threshold (4 ms/sample of queue build)
	ccOveruseHardNs  = int64(20_000_000) // strong over-use (20 ms/sample) → hard decrease
	ccDecMild        = 0.90              // mild multiplicative decrease
	ccDecHard        = 0.75              // hard multiplicative decrease
	ccIncFracPerSec  = 0.25              // additive increase: 25% of ceiling per second toward ceiling
	ccGradSmooth     = 0.3               // gradient EWMA weight
	ccLossSmooth     = 0.5               // loss EWMA weight
	ccMinFPSFraction = 2                 // fps floor = baseFPS/this when forced below bitrate floor
)

// congestConfig bounds a controller to the negotiated budget.
type congestConfig struct {
	FloorKbps int // never trim below (minGovKbps-class floor)
	CeilKbps  int // the negotiated bitrate (RateHint restores to this when healthy)
	BaseFPS   int // nominal fps (for the fps ladder rung)
	Stream    uint16
}

// congestController holds the AIMD + over-use-detector state. Not safe for concurrent use - one
// controller per route, stepped by that route's single governor goroutine.
type congestController struct {
	cfg      congestConfig
	target   float64 // current target bitrate (kbps)
	gradEwma float64 // smoothed signed owd gradient (ns/sample)
	lossEwma float64
	lastOwd  int64
	hasOwd   bool
}

func newCongestController(cfg congestConfig) *congestController {
	if cfg.CeilKbps <= 0 {
		cfg.CeilKbps = 20000
	}
	if cfg.FloorKbps <= 0 || cfg.FloorKbps > cfg.CeilKbps {
		cfg.FloorKbps = minGovKbps
	}
	return &congestController{cfg: cfg, target: float64(cfg.CeilKbps)}
}

// update folds one feedback sample (interval dtSec, datagram lossFrac 0..1, smoothed signed one-way
// delay owdNs) into the estimate and returns the RateHint to emit. Pure given its state.
func (c *congestController) update(dtSec float64, lossFrac float64, owdNs int64) RateHint {
	if dtSec <= 0 {
		dtSec = 1
	}
	// Delay gradient: trend of the smoothed one-way delay. A sustained positive gradient = a queue
	// building ahead of us = over-use (a constant clock offset cancels in the difference).
	var grad float64
	if c.hasOwd {
		grad = float64(owdNs - c.lastOwd)
	}
	c.lastOwd, c.hasOwd = owdNs, true
	c.gradEwma += (grad - c.gradEwma) * ccGradSmooth
	c.lossEwma += (lossFrac - c.lossEwma) * ccLossSmooth

	overusing := int64(c.gradEwma) > ccOveruseNs
	lossy := c.lossEwma > ccLossLow
	hard := c.lossEwma > ccLossHigh || int64(c.gradEwma) > ccOveruseHardNs

	switch {
	case overusing || lossy:
		factor := ccDecMild
		if hard {
			factor = ccDecHard
		}
		c.target *= factor
	default:
		c.target += float64(c.cfg.CeilKbps) * ccIncFracPerSec * dtSec // additive increase
	}
	if c.target > float64(c.cfg.CeilKbps) {
		c.target = float64(c.cfg.CeilKbps)
	}
	if c.target < float64(c.cfg.FloorKbps) {
		c.target = float64(c.cfg.FloorKbps)
	}

	h := RateHint{Type: MetaRate, Stream: c.cfg.Stream, MaxBitrateKbps: int(c.target)}
	// FPS rung: only when bitrate is already pinned at the floor AND congestion persists (bitrate
	// first, then fps - the owner's ladder). Resolution is never touched here (VRAM governor's last
	// resort).
	if int(c.target) <= c.cfg.FloorKbps && (overusing || lossy) && c.cfg.BaseFPS > 0 {
		fps := c.cfg.BaseFPS / ccMinFPSFraction
		if fps < minGovFPS {
			fps = minGovFPS
		}
		h.MaxFPS = fps
	}
	return h
}

// Target reports the current target bitrate (kbps) for telemetry.
func (c *congestController) Target() int { return int(c.target) }

// minRateHint combines the congestion-controller hint with the VRAM governor hint by taking the
// MORE RESTRICTIVE value on each axis (brief req 3: min() of both hints, VRAM governor behaviour
// intact). A zero on an axis means "no cap on that axis" and never wins the min.
func minRateHint(a, b RateHint) RateHint {
	out := RateHint{Type: MetaRate, Stream: a.Stream}
	out.MaxBitrateKbps = minCap(a.MaxBitrateKbps, b.MaxBitrateKbps)
	out.MaxFPS = minCap(a.MaxFPS, b.MaxFPS)
	out.MaxHeight = minCap(a.MaxHeight, b.MaxHeight)
	return out
}

// congestGovernor is the recv-side rate emitter for a UDP route (Unit 4 wiring): each report tick it
// feeds the udpConn's datagram loss + one-way-delay into the controller and emits the resulting
// RateHint, min()-combined with the VRAM governor's ladder (bitrate first, then fps; resolution is
// the VRAM governor's last resort). Emits only on a change (dedup). The sender applies it through
// SetRateHint (RateControlSource) exactly like the VRAM backpressure.
func (rm *RouteManager) congestGovernor(ctx context.Context, rio *routeIO, udp statsSource, stream uint16, budget func(codedH int) int, baseFPS int) {
	ceil := budget(0)
	if ceil <= 0 {
		ceil = defaultCongestCeilKbps
	}
	assertBudgetSane(rm, stream, ceil)
	cc := newCongestController(congestConfig{CeilKbps: ceil, BaseFPS: baseFPS, Stream: stream})
	t := time.NewTicker(rm.reportEvery)
	defer t.Stop()
	var last RateHint
	var sent bool
	prev := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		dt := now.Sub(prev).Seconds()
		prev = now
		s := udp.Stats()
		h := cc.update(dt, s.LossPct/100, s.OwdNs)
		// Fold in the VRAM governor (when present + enabled) by taking the more restrictive cap.
		if rm.headroom != nil && (rm.governorOn == nil || rm.governorOn()) {
			if free, ok := rm.headroom(); ok {
				h = minRateHint(h, rateLadder(free, rm.govFloorMB, ceil, baseFPS, stream))
			}
		}
		capBudgetToCoded(&h, budget) // item 2: a resolution cap lowers the bitrate to the coded-size model
		h.Type, h.Stream = MetaRate, stream
		rio.st.setBudget(rm.budgetModeStr(), h.MaxBitrateKbps, ceil, 0, budgetReason(h, ceil, baseFPS)) // item 5
		if sent && h == last {
			continue
		}
		if rio.writeMeta(h, rm.clock.Now()) != nil {
			return
		}
		last, sent = h, true
		rm.infof("congestion governor", map[string]any{"stream": stream, "lossPct": s.LossPct,
			"owdMs": s.OwdNs / 1e6, "maxKbps": h.MaxBitrateKbps, "maxFPS": h.MaxFPS, "ceilKbps": ceil})
	}
}

// minCap returns the smaller positive value; 0 = "no cap" so it never wins unless both are 0.
func minCap(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}
