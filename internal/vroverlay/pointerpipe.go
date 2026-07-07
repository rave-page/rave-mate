package vroverlay

import (
	"math"
	"time"
)

// pointerpipe.go - the pointer CAST pipeline, consolidated from the old scattered helpers into one
// coherent staged path (rewrite; see docs/dev/VR_OVERLAY_REWRITE.md). updatePointer (pointer.go) is
// the orchestrator; this file owns turning ONE hand's controller pose into a smoothed overlay hit:
//
//	pointerCastHand           per-frame entry for the active hand
//	  └ aimOrTip              stage 1: ray pose - /pose/tip aim, learned tip-correction, raw fallback
//	  └ castTouch→projectPoint stage 2: near-field tip ⟂-projection onto the owned quad (poke) OR
//	  └ castRay  →hitQuad     stage 2': arm's-length aim ray, EXACT analytic ray↔quad (geomquad)
//	smoothHit                 stage 3: 1€-filter the hit UV + world point (kill tremor, keep response)
//	(row mapping)             stage 4: applyHover maps the smoothed v directly to a row (no hysteresis)
//
// The hit UV and world point come from ONE geometric computation (the quad we own), so the cursor and
// the selected row cannot disagree - killing the center-scaled drift the old runtime-UV path had.
//
// Behavior is identical to the pre-rewrite code - this is the structural consolidation; feel-tuning
// (filter location, near-field blend, hysteresis) happens live in-headset on top of this base.

// aimOrTip returns the hand's laser-aim (tip) pose for raycasting. It prefers the bound /pose/tip aim
// action and, while that's live, learns the device→tip correction for the hand. If the aim action goes
// inactive (e.g. a stale SteamVR binding saved before /pose/tip was bound), it RECONSTRUCTS the tip pose
// from the raw device pose via that learned correction - so the ray keeps landing where the controller
// points instead of ~40° off (the raw device −Z). Only if no live aim was ever seen does it fall back to
// the raw device pose (usable but offset), and it warns so the binding can be reset.
func (e *editor) aimOrTip(hand Hand, idx int) (Mat34, bool) {
	dev, devOK := e.ed.DevicePose(idx)
	if aim, ok := e.frameAim, e.frameAimOK; ok && hand == e.activeHand { // frame-cached AimPose (read once in updatePointer)
		if devOK && int(hand) < len(e.tipCorr) {
			e.tipCorr[hand] = MulMat(InvMat(dev), aim) // device-local transform to the tip frame
			e.tipCorrOK[hand] = true
		}
		return aim, true
	}
	if hand != e.activeHand { // non-active hand (rare path) → direct read
		if aim, ok := e.ed.AimPose(hand); ok {
			if devOK && int(hand) < len(e.tipCorr) {
				e.tipCorr[hand] = MulMat(InvMat(dev), aim)
				e.tipCorrOK[hand] = true
			}
			return aim, true
		}
	}
	if !devOK {
		return Mat34{}, false
	}
	if int(hand) < len(e.tipCorrOK) && e.tipCorrOK[hand] {
		return MulMat(dev, e.tipCorr[hand]), true // reconstructed tip pose (accurate)
	}
	if time.Since(e.aimWarnAt) > 10*time.Second {
		e.aimWarnAt = time.Now()
		e.evt("WARN aim/tip pose unbound for %s hand - reset the rave-mate SteamVR binding to default for accurate pointing", handName(hand))
	}
	return dev, true
}

// pointerCastHand casts from ONE hand against the candidate overlays using EXACT analytic geometry
// (each overlay's owned world quad - pose × width × aspect), NOT the runtime's intersection: a
// near-field TOUCH projection first (tip position ⟂-projected onto the quad - stable when poking a
// hand-held menu, since wrist ANGLE can't sweep rows), then the arm's-length aim ray. The hit's world
// point comes from the same geometry, so the cursor and the selected row can never disagree.
func (e *editor) pointerCastHand(hand Hand, cands []string) (pointerHit, bool) {
	idx, ok := e.ed.ControllerIndex(hand)
	if !ok {
		return pointerHit{}, false
	}
	pose, ok := e.aimOrTip(hand, idx)
	if !ok {
		return pointerHit{}, false
	}
	sx, sy, sz := MatPos(pose)
	fx, fy, fz := MatForward(pose) // unit −Z col = where the controller points
	src := [3]float32{float32(sx), float32(sy), float32(sz)}
	dir := [3]float32{float32(fx), float32(fy), float32(fz)}
	// Near-field touch: probe every 3rd frame while out of range (cheap - imperceptible for the "bring
	// hand to menu" transition), every frame once touching.
	if e.touchLive || e.ptrFrame%3 == 0 {
		if h, ok := e.castTouch(src, cands, idx); ok {
			e.touchLive = true
			return h, true
		}
		e.touchLive = false
	}
	return e.castRay(src, dir, cands, pointerMaxDst, idx)
}

// castRay returns the nearest analytic ray↔quad hit within maxDst across cands. Exact: no runtime
// ComputeOverlayIntersection, no mouse-scale round-trip; a miss is a point outside the real edges.
func (e *editor) castRay(src, dir [3]float32, cands []string, maxDst float32, idx int) (pointerHit, bool) {
	e.m.perfC.castRayNs.Add(1) // perf probe: cast passes per pointer frame
	best := pointerHit{dist: math.MaxFloat32}
	found := false
	for _, key := range cands {
		center, w, hgt, ok := e.ed.OverlayQuad(key)
		if !ok {
			continue
		}
		q := hitQuad(src, dir, center, w, hgt)
		if !q.inside || q.dist < 0 || q.dist > maxDst || q.dist >= best.dist {
			continue
		}
		best = pointerHit{key: key, u: q.u, v: q.v, dist: q.dist, idx: idx, pt: q.pt}
		found = true
	}
	return best, found
}

// castTouch returns the nearest near-field TOUCH hit: the controller tip perpendicular-projected onto
// a quad within touchNearM. Position-based, so wrist-angle tremor at point-blank can't sweep rows.
func (e *editor) castTouch(tip [3]float32, cands []string, idx int) (pointerHit, bool) {
	best := pointerHit{dist: math.MaxFloat32}
	found := false
	for _, key := range cands {
		center, w, hgt, ok := e.ed.OverlayQuad(key)
		if !ok {
			continue
		}
		q := projectPoint(tip, center, w, hgt)
		if !q.inside || q.dist > touchNearM || q.dist >= best.dist {
			continue
		}
		best = pointerHit{key: key, u: q.u, v: q.v, dist: q.dist, idx: idx, touch: true, pt: q.pt}
		found = true
	}
	return best, found
}

// smoothHit filters a raw ray→overlay hit: the UV (in the overlay's own space, cancelling tremor from
// either hand) + the world hit point. Filters reset when the ray moves to a different overlay, the
// active hand switches, or the pointer was off-target long enough that resuming would fling the cursor
// - so smoothing never lerps a jump. Returns the smoothed hit used for cursor + hover + click alike.
func (e *editor) smoothHit(h pointerHit) pointerHit {
	now := time.Now()
	dt := now.Sub(e.ptrSmoothAt).Seconds()
	e.ptrSmoothAt = now
	if h.key != e.ptrSmoothKey || e.activeHand != e.ptrSmoothHand || dt <= 0 || dt > 0.2 {
		e.ptrUV.reset()
		e.ptrPt.reset()
		e.ptrSmoothKey, e.ptrSmoothHand = h.key, e.activeHand
		dt = 1.0 / 90
	}
	uv := e.ptrUV.filter([3]float32{h.u, h.v, 0}, dt)
	h.u, h.v = clamp01(uv[0]), clamp01(uv[1])
	h.pt = e.ptrPt.filter(h.pt, dt)
	return h
}

// Row hysteresis was REMOVED here (click-where-you-point-now): the hovered/clicked row is now mapped
// directly from the 1€-smoothed hit v in applyHover (menuRowAt((1-v)*mh, mh)), so dot = highlight =
// click. The old smoothRow held a stale row within a ¼-row margin - the felt "clicks a different row
// than I point at". The pose/UV 1€ filter already kills the tremor the hysteresis was compensating.
