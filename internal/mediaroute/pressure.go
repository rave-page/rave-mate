package mediaroute

import (
	"sort"
	"time"

	"rave.page/mate/internal/gpubudget"
	"rave.page/mate/internal/medialink"
)

// VRAM-pressure release of parked senders. A parked 4K sender holds its 31.6 MiB destination texture
// (+ interop, + the receiving app's own copy) with no route using it; under pressure that VRAM goes
// back to the card. Hysteresis (each layer stops create/destroy churn on a card hovering at the floor):
//   - enter: smoothed-by-dwell - free < floor continuously for pressureDwell (raw free swings 100s of
//     MB/s, incident 2026-11-13);
//   - exit: free >= floor + pressureHystMB; inside the band the state holds (no flip-flop);
//   - pace: one release round per pressureSettle, sized by the estimated texture bytes it frees, so
//     the driver's lagging free count does not trigger over-release;
//   - age: a sender parked < pressureMinIdle is kept - a quick route restart (the case the keep-alive
//     exists for, 12b25a7) never pays a destroy + re-create.
//
// Never released: a REMEMBERED receive's destination. VRAM_FULL_CARD_POLICY "claim early, keep
// forever": re-creating it registers a new GL/DX interop in Resolume, and that registration is what
// fails on a full card - releasing it under pressure would schedule the risky re-create exactly when
// the card is fullest. Peer liveness (the brief's exception) is not observable here: the media child
// holds no peer manager and medialink keeps the last advert of a gone peer. A sender whose receive is
// live or still establishing is never released (it is about to be reused).
const (
	pressureHystMB  uint64 = 256
	pressureDwell          = 6 * time.Second
	pressureSettle         = 4 * time.Second
	pressureMinIdle        = 30 * time.Second
)

// pressureState is the release hysteresis (scan-loop goroutine only).
type pressureState struct {
	belowSince  time.Time // first sample below the floor of the current run (zero = above)
	active      bool      // releasing until free >= floor + pressureHystMB
	lastRelease time.Time
}

// systemFloorMB is the default pressure floor: the gpubudget SYSTEM floor (balanced 700 MB,
// permissive 400 MB, config vramSystemHeadroomMB overrides) - the line below which other apps'
// encoders stall and below which admission already refuses a full allocation.
func (m *Manager) systemFloorMB() uint64 {
	if m.cfg == nil {
		return gpubudget.DefaultSystemFloorBalancedMB
	}
	c := m.cfg()
	mode, _ := gpubudget.ParseMode(c.VramPolicyName())
	return gpubudget.SystemFloorMB(gpubudget.Policy{Mode: mode, SystemFloorMB: uint64(max(c.VramSystemHeadroomMB, 0))})
}

// relievePressure runs on the scan tick: under sustained VRAM pressure, destroy parked senders with
// no active route (oldest first) until the estimated headroom clears the exit threshold. Fails open:
// governor off / headroom unknown => nothing is released.
func (m *Manager) relievePressure() {
	ps := &m.pressure
	if m.headroom == nil || m.cfg == nil || !m.cfg().VramGovernorEnabled() {
		*ps = pressureState{}
		return
	}
	hr := m.headroom()
	if !hr.Present {
		*ps = pressureState{}
		return
	}
	now := m.now()
	floor := m.pressureFloor()
	high := floor + pressureHystMB
	switch {
	case hr.FreeMB >= high:
		ps.active, ps.belowSince = false, time.Time{}
		return
	case hr.FreeMB < floor:
		if ps.belowSince.IsZero() {
			ps.belowSince = now
		}
		if now.Sub(ps.belowSince) >= pressureDwell {
			ps.active = true
		}
	default: // hysteresis band: state holds, the dwell restarts
		ps.belowSince = time.Time{}
	}
	if !ps.active || (!ps.lastRelease.IsZero() && now.Sub(ps.lastRelease) < pressureSettle) {
		return
	}

	type victim struct {
		name  string
		inner medialink.Sink
		mb    float64
		idle  time.Duration
	}
	m.mu.Lock()
	busy := map[string]bool{}
	for _, r := range m.receives {
		if r.Occupied(now) {
			busy[linkPrefix+r.Name] = true
		}
	}
	var cand []victim
	for n, p := range m.parked {
		if busy[n] || m.rememberedLocked(n) || now.Sub(p.since) < pressureMinIdle {
			continue
		}
		cand = append(cand, victim{n, p.inner, float64(p.w*p.h*4) / (1 << 20), now.Sub(p.since)})
	}
	sort.Slice(cand, func(i, j int) bool { return cand[i].idle > cand[j].idle }) // oldest first
	var out []victim
	est := float64(hr.FreeMB)
	for _, v := range cand {
		if est >= float64(high) {
			break
		}
		delete(m.parked, v.name)
		out = append(out, v)
		est += v.mb
	}
	m.mu.Unlock()
	if len(out) == 0 {
		return
	}
	ps.lastRelease = now
	before := hr.FreeMB
	for _, v := range out {
		closeOrDestroy(v.inner)
		after := m.headroom().FreeMB
		m.log.Info(source, "kept-alive sender released under VRAM pressure (no active route, oldest first)", map[string]any{
			"sender": v.name, "idle": v.idle.Round(time.Second).String(), "estTextureMB": int(v.mb + 0.5),
			"freeBeforeMB": before, "freeAfterMB": after, "floorMB": floor, "exitMB": high})
		before = after
	}
}

// zeroFrameCap bounds the shared transparent park frame (8K UHD RGBA, 126.6 MiB). Larger senders park
// on their last picture instead of growing it.
const zeroFrameCap = 7680 * 4320 * 4

// zeroFrame returns an all-zero buffer of n bytes from ONE shared allocation (grown to the largest n
// seen, never written), nil when n > zeroFrameCap. Read-only sharing is safe: sinks never write a
// Write payload (FrameSender contract: the caller's again after Send). Was a w*h*4 blank held per
// parked sender lineage: 8 parked 4K senders = 265 MB of RAM; now one 33 MB buffer.
func (m *Manager) zeroFrame(n int) []byte {
	if n <= 0 || n > zeroFrameCap {
		return nil
	}
	m.zeroMu.Lock()
	defer m.zeroMu.Unlock()
	if len(m.zero) < n {
		m.zero = make([]byte, n)
	}
	return m.zero[:n:n]
}
