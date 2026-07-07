package vroverlay

import (
	"fmt"
	"math"
	"strings"
	"time"

	"rave.page/mate/internal/config"
)

// XSOverlay-style ray pointer. Point a controller at a visible rave-mate overlay → a cursor dot
// tracks the ray→overlay hit, the hovered element highlights + tooltips, and the pointer_click
// (trigger) action activates it. It's all manual ray→overlay intersection (Editor.Intersect) + our
// own IVRInput action: it NEVER flips MakeOverlaysInteractiveIfVisible, so SteamVR never puts the
// controller into overlay-laser mode - the pointer coexists with VRChat (aim/click our overlay, keep
// playing). Runs at 90 Hz from handleActions BEFORE the editor-open gate, so pointing at the wrist
// badge can OPEN the editor while it's closed.

const (
	cursorKey     = "page.rave.mate.cursor"
	ptrTipKey     = "page.rave.mate.__ptrtip"
	pointerMaxDst = float32(5) // ignore hits farther than 5 m
	cursorSizeM   = 0.015      // cursor dot width (m)
	ptrTipSizeM   = 0.14       // pointer tooltip width (m)

	// Near-field touch: within touchNearM of a surface the cursor is driven by the tip's POSITION
	// (perpendicular-projected onto the quad - castTouch/projectPoint), not the aim angle. Point-blank
	// ray-casting made tiny wrist tilts sweep whole rows (poking a hand-held menu puts the tip 3–20 cm
	// away - angular tremor there is row-scale, position tremor is mm-scale).
	touchNearM = float32(0.28)
)

// pointerHit is the nearest analytic hit (ray↔quad or touch projection) across candidates this frame.
type pointerHit struct {
	key   string
	u, v  float32    // hit UV (0..1, v bottom-origin) - exact, from geomquad
	dist  float32    // hit distance (m): ray param, or ⟂ distance for a touch hit
	idx   int        // firing controller device index (grab targeting)
	pt    [3]float32 // world hit point ON the quad (analytic; cursor sits exactly here)
	touch bool       // near-field touch hit (tip position projected, not the aim ray)
}

// updatePointer casts a ray from each controller at the candidate overlays, moves the cursor dot to
// the nearest hit, updates hover feedback, and activates on a trigger edge. 90 Hz.
//
// The ray pointer is the ONE cursor in every state - the SteamVR interactive laser is no longer used
// for the wrist / floating menu / content (that double-cursored + fought the game). Closed: point at
// the wrist badge to open, or hover a visible overlay. Open: click menu rows; in edit mode also
// select/grab content overlays. A trigger HELD over a slider row drags it (part 5).
func (e *editor) updatePointer(feat config.VROverlayFeature) {
	// Active hand: whichever hand pulls the trigger becomes the ONE pointer hand (casting from both +
	// picking the nearest made the cursor jitter between hands). The hand holding the menu is excluded.
	lHeld, lEdge := e.ed.PointerClickState(HandLeft)
	rHeld, rEdge := e.ed.PointerClickState(HandRight)
	if lEdge {
		e.activeHand = HandLeft
	}
	if rEdge {
		e.activeHand = HandRight
	}
	if !e.handTracked(e.activeHand) { // never set / lost tracking → the free hand (not the menu's)
		e.activeHand = e.defaultPointerHand(feat)
	}
	// The hand HOLDING the menu can never drive the pointer: an in-game trigger pull with that hand
	// (VRChat UI etc.) used to steal activeHand to it - and since it can't point at its own menu, the
	// pointer went dead until the other hand clicked ("stops registering input"). Live diag confirmed:
	// active=R while menuHand=R, pointer hit=(none).
	if e.on && e.activeHand == e.menuHand(feat) {
		e.activeHand = e.defaultPointerHand(feat)
	}
	held := (e.activeHand == HandLeft && lHeld) || (e.activeHand == HandRight && rHeld)
	edge := (e.activeHand == HandLeft && lEdge) || (e.activeHand == HandRight && rEdge)

	// One AimPose read per frame, shared by the cast + the debug snapshot (was 2 cgo calls/frame).
	e.ptrFrame++
	e.frameAim, e.frameAimOK = e.ed.AimPose(e.activeHand)

	// The hand WEARING the badge can't click its own badge: its tip is permanently centimeters from
	// it, so any in-game trigger pull (VRChat menu etc.) while the hand was in view toggled the editor.
	// Opening stays on the OTHER hand's pointer, the summon bind, or the dashboard tab.
	exclWrist := e.activeHand == HandFromString(feat.ResolvedEditHand())
	t0 := time.Now()
	hit, ok := e.pointerCastHand(e.activeHand, e.pointerCandidates(feat, exclWrist))
	e.m.castStat.observe(time.Since(t0)) // perf probe: full cast cost (touch pre-pass + ray)
	e.m.perfC.ptrFrames.Add(1)
	if ok && hit.touch {
		e.m.perfC.touchFrames.Add(1)
	}
	if ok {
		// Cursor + selection now share ONE exact computation: hit.u/v and hit.pt both come from the
		// analytic quad hit (geomquad), so the dot sits exactly on the row that highlights/fires - no
		// runtime UV round-trip, no center-scaled divergence, no edge-clamp guard. The old UVWorld
		// mapping is kept ONLY as a validation trace (ptrUVDeltaM, logged in captureDbg) - it should
		// now read ≈0 and can be deleted once confirmed in-headset.
		hit = e.smoothHit(hit) // 1€-filter residual hand tremor (analytic hit is exact but hands shake)
	}
	e.captureDbg(feat, lHeld, rHeld, hit, ok)
	editHand := HandFromString(feat.ResolvedEditHand())
	if !ok {
		e.clearPointer()
		e.ptrDragActive = false
		e.driveHover(feat)
		e.driveStripHover(editHand)
		return
	}
	e.placeCursor(hit.pt)
	e.applyHover(hit)
	e.driveHover(feat)
	e.driveStripHover(editHand)
	// Slider drag: while the active-hand trigger is held over a MISlider row, set the value from the hit
	// u every frame - lets the user pull to 0%/100% (edges a single click can't reach). Consumes the
	// frame so the edge-click below can't ALSO fire a neighbouring row.
	if e.on && e.isMenuKey(hit.key) && e.pointerSliderDrag(hit, held) {
		return
	}
	e.ptrDragActive = false
	if edge {
		e.evt("pointer CLICK on %s (u=%.2f v=%.2f) hand=%d", strings.TrimPrefix(hit.key, "page.rave.mate."), hit.u, hit.v, e.activeHand)
		e.pointerClick(hit)
	}
}

// captureDbg snapshots the current pointer/editor state (menu/hands/hit geometry) for remote debugging.
func (e *editor) captureDbg(feat config.VROverlayFeature, lHeld, rHeld bool, hit pointerHit, ok bool) {
	mh := e.menuHand(feat)
	excl := HandNone
	if e.on {
		excl = mh
	}
	aimPose, aimOK := e.frameAim, e.frameAimOK // this frame's AimPose read (shared with the cast - no 2nd cgo call)
	d := ptrDebug{
		on: e.on, editMode: e.editMode, active: e.activeHand, menuHand: mh, exclHand: excl,
		lTracked: e.handTracked(HandLeft), rTracked: e.handTracked(HandRight), lHeld: lHeld, rHeld: rHeld,
		aimActive: aimOK, hitOK: ok, hitRow: -99,
	}
	if ok {
		d.hitKey, d.hitU, d.hitV, d.hitDist = hit.key, hit.u, hit.v, hit.dist
		if hit.key == menuKey {
			m := MenuRowH * (e.shownMenu(menuKey).rows + 1) // DISPLAYED texture's row count (see menuSnap)
			d.hitRow = menuRowAt((1-hit.v)*float32(m), m)   // v bottom-origin (see smoothRow)
		}
	}
	e.setDbg(d)
	// Continuous throttled trace so DRIFT over time is visible in the ring (not just at clicks): the hit
	// u/v/row + the active hand's aim-pose position - if the aim pos holds still but u/v drifts, the
	// target (a hand-attached menu) is moving; if the aim pos itself drifts, it's the pose.
	if ok && time.Since(e.lastPtrLog) > 150*time.Millisecond {
		e.lastPtrLog = time.Now()
		aimPos := "?"
		if aimOK {
			ax, ay, az := MatPos(aimPose)
			aimPos = fmt.Sprintf("[%.2f %.2f %.2f]", ax, ay, az)
		}
		// Validation (throttled, delete once confirmed): distance between our analytic cursor point and
		// the runtime's old UVWorld mapping for the SAME UV. The center-scaled drift bug made these
		// diverge up to a row; with the analytic hit it should read ≈0 across the whole menu.
		if wp, wok := e.ed.UVWorld(hit.key, hit.u, hit.v); wok {
			e.ptrUVDeltaM = dist3(wp, hit.pt)
		}
		mode := "ray"
		if hit.touch {
			mode = "touch"
		}
		e.evt("ptr %s %s hit=%s u=%.3f v=%.3f row=%d dist=%.2f uvd=%.0fmm aim=%v aimPos=%s",
			handName(e.activeHand), mode, strings.TrimPrefix(d.hitKey, "page.rave.mate."), d.hitU, d.hitV, d.hitRow, d.hitDist, e.ptrUVDeltaM*1000, aimOK, aimPos)
	}
}

// handTracked reports whether a hand's controller is currently tracked.
func (e *editor) handTracked(hand Hand) bool {
	if hand == HandNone {
		return false
	}
	_, ok := e.ed.ControllerIndex(hand)
	return ok
}

// menuHand is the hand the floating menu is attached to (HandNone when world-anchored).
func (e *editor) menuHand(feat config.VROverlayFeature) Hand {
	return e.menuTransform(feat, HandFromString(feat.ResolvedEditHand())).Snap
}

// defaultPointerHand picks the pointer hand before any trigger pull: the hand NOT holding the menu,
// else a tracked hand (right preferred).
func (e *editor) defaultPointerHand(feat config.VROverlayFeature) Hand {
	switch e.menuHand(feat) {
	case HandLeft:
		if e.handTracked(HandRight) {
			return HandRight
		}
	case HandRight:
		if e.handTracked(HandLeft) {
			return HandLeft
		}
	}
	if e.handTracked(HandRight) {
		return HandRight
	}
	return HandLeft
}

// pointerCandidates lists overlays the custom ray can hit. The custom ray is the SOLE cursor in every
// state - the SteamVR native interactive laser is never used for the menu/content, because that laser
// (MakeOverlaysInteractiveIfVisible) globally captures the controller from the running game (VRChat) and
// suppresses our IVRInput binds. Wrist is a candidate (open/close) unless excluded (the badge's own
// hand); the menu when open; enabled content overlays when editing. All coexist with the game (own
// intersection, own click action). Reuses a scratch slice - this runs at 90 Hz (no per-frame alloc).
func (e *editor) pointerCandidates(feat config.VROverlayFeature, exclWrist bool) []string {
	cands := e.candScratch[:0]
	if !exclWrist {
		cands = append(cands, wristKey)
	}
	if e.on {
		if !exclWrist { // strip rides the badge hand - same self-click exclusion as the badge
			cands = append(cands, stripKey)
		}
		if e.fullMenu && !e.menuHidden { // paged menu is a target only while opened from the strip
			cands = append(cands, menuKey)
		}
		if e.editMode {
			cands = append(cands, posKey) // positioning menu (own overlay; live even when the main menu is hidden)
			if !e.m.contentHidden {
				for _, o := range feat.Overlays {
					if o.Enabled {
						cands = append(cands, contentKey(o.ID))
					}
				}
			}
		}
	}
	e.candScratch = cands
	return cands
}

// pointerSliderDrag drags the hovered MISlider row from the hit u while the trigger is held. Returns
// true when the frame is a slider (drag active OR the row is a slider but not held → still "owns" the
// row so the caller skips the edge click). false = not over a slider → normal click routing.
func (e *editor) pointerSliderDrag(h pointerHit, held bool) bool {
	items := e.shownMenu(h.key).items // the list the displayed texture shows (ptrRow maps into it)
	row := e.ptrRow                   // highlighted row (hysteresis-stable), set by applyHover this frame
	if row < 0 || row >= len(items) || items[row].Kind != MISlider {
		return false
	}
	if !held {
		e.ptrDragActive = false
		return false // over a slider but not pulling → let the edge-click set it
	}
	if !e.ptrDragActive {
		e.evt("slider DRAG start row %d %q", row, items[row].Label)
		e.ptrDragActive = true
	}
	if e.menuActionAt(items, row, float64(h.u)) {
		e.menuBuiltAt[h.key] = time.Time{} // reflect the new value on the next rebuild
	}
	return true
}

// applyHover maps the hit to an element: highlights it, updates grab targeting (noteHover), and shows
// a tooltip. Menu rows reuse the editor's existing beside-menu tooltip; wrist + content get a small
// label tooltip at the cursor.
func (e *editor) applyHover(h pointerHit) {
	if e.ptrKey != h.key {
		e.ptrRow = -1 // smoothRow's hysteresis band indexed ANOTHER overlay's texture - never gate the new one with it
	}
	e.ptrKey = h.key
	e.ptrStripCell = -1
	switch {
	case h.key == wristKey:
		e.ptrRow = -1
		e.wristHover = true
		e.noteHover(wristKey, h.idx)
		e.showPtrTip(wristTipLabel(e.on), h.pt)
	case h.key == stripKey:
		e.ptrRow = -1
		e.wristHover = false
		// Cell math against the DISPLAYED snapshot (stripShown) - hover/click always hit the cell seen.
		cell := stripCellAt(h.u, len(e.stripShown))
		e.ptrStripCell = cell
		e.noteHover(stripKey, h.idx)
		if cell >= 0 && cell < len(e.stripShown) {
			e.showPtrTip(e.stripShown[cell].Label, h.pt)
		} else {
			e.hidePtrTip()
		}
	case e.isMenuKey(h.key):
		e.wristHover = false
		// Row math against the DISPLAYED texture's snapshot (menuSnap), never the live rebuilt list -
		// what highlights (and later clicks) is always the row the user sees under the cursor.
		mh := MenuRowH * (e.shownMenu(h.key).rows + 1)
		// Click-where-you-point-now: the row under the (1€-smoothed) cursor drives highlight AND click,
		// so the dot, the highlight, and what fires are always the SAME row. The old ¼-row hysteresis
		// held a STALE row until aim cleared a margin - you could point visibly at row N (dot on it) yet
		// it highlighted+clicked N−1: the felt "clicks a different row than I point at". The 1€ filter on
		// the hit UV already kills the tremor hysteresis was compensating for.
		row := menuRowAt((1-h.v)*float32(mh), mh) // v is bottom-origin (GL vUVs) → (1-v) top-Y flip
		e.ptrRow = row
		if h.key == menuKey {
			e.hoverRow, e.hoverRowAt = row, time.Now() // feed the truncated-row tooltip (main menu only)
		}
		e.noteHover(h.key, h.idx)
		e.hidePtrTip()
	default: // content overlay or the camera-path preview
		e.ptrRow = -1
		e.wristHover = false
		e.noteHover(h.key, h.idx)
		if h.key == worldPathKey {
			e.hidePtrTip() // orbit-drag hints live in the preview panel
		} else {
			e.showPtrTip(strings.TrimPrefix(h.key, "page.rave.mate."), h.pt)
		}
	}
}

// driveHover floats the row-highlight accent overlay (RenderHoverRow) over the ray-hovered menu row.
// Baking hover into the menu texture re-rendered + re-uploaded the ENTIRE ~2.5MB texture on every
// row change (visible flicker + GPU churn); this repositions ONE MenuW×MenuRowH overlay instead.
// It rides the SAME parent as the menu (device or world, via the menuTransform math the shadow/pos
// overlays use), nudged 2mm toward the viewer so it draws over the row. Hidden off-menu / on headers
// / while the menu is grabbed (a carried menu's live pose diverges from its stored transform).
func (e *editor) driveHover(feat config.VROverlayFeature) {
	key, row := e.ptrKey, e.ptrRow
	shown := menuSnap{}
	if e.isMenuKey(key) {
		shown = e.shownMenu(key)
	}
	show := e.on && row >= 0 && row < len(shown.items) && shown.items[row].Kind != MIHeader &&
		!e.isGrabbing(key) && !(key == menuKey && e.menuHidden)
	if !show {
		if e.hoverShow.changed(false) {
			_ = e.m.rt.Show(hoverHlKey, false)
		}
		return
	}
	if !e.hoverEnsured {
		if e.m.rt.EnsureOverlay(hoverHlKey, "rave-mate hover") != nil {
			return
		}
		_ = e.m.rt.SetTexture(hoverHlKey, e.m.rend.RenderHoverRow())
		e.hoverEnsured = true
	}
	tf := e.menuTransform(feat, HandFromString(feat.ResolvedEditHand()))
	if key == posKey { // mirror tickPosMenu's beside-the-menu placement (X uses the MAIN width, then shrink)
		tf.X -= tf.WidthM*0.5 + 0.20
		tf.WidthM = 0.34
	}
	sig := fmt.Sprintf("%s|%d|%d|%v", key, row, shown.rows, tf)
	if e.hoverSig != sig {
		e.hoverSig = sig
		if e.hoverW != tf.WidthM { // width/alpha channel (the matrix pushes below carry no size)
			_ = e.m.rt.SetTransform(hoverHlKey, Transform{WidthM: tf.WidthM, Opacity: 1})
			e.hoverW = tf.WidthM
		}
		yOff, zOff := hoverRowOffset(row, shown.rows, tf.WidthM)
		local := MulMat(EulerToMat(tf.Yaw, tf.Pitch, tf.Roll, tf.X, tf.Y, tf.Z), EulerToMat(0, 0, 0, 0, yOff, zOff))
		if idx, ok := e.anchorIdx(handToSnap(tf.Snap)); ok {
			e.ed.SetTransformMatrixDevice(hoverHlKey, idx, local)
		} else {
			e.ed.SetTransformMatrixWorld(hoverHlKey, local)
		}
	}
	if e.hoverShow.changed(true) {
		_ = e.m.rt.Show(hoverHlKey, true)
	}
}

// hoverRowOffset returns the hover accent's offset in the MENU's local frame (metres): y up/down to
// the hovered row's center on the quad, z toward the viewer (+Z faces the HMD) so it draws in front.
// Pure row→offset math: the quad is widthM wide and widthM*mh/MenuW tall; row i's pixel band is
// [(i+1)*MenuRowH, (i+2)*MenuRowH) top-down; overlay origin = quad center, +Y up.
func hoverRowOffset(row, rows int, widthM float64) (y, z float64) {
	mh := float64(MenuRowH * (rows + 1))
	quadH := widthM * mh / float64(MenuW)
	centerPx := (float64(row) + 1.5) * float64(MenuRowH)
	return quadH * (0.5 - centerPx/mh), 0.002
}

// navClickGuard swallows menu clicks briefly after a page change: navigation reflows the rows under
// a stationary cursor, so the user's next click at the same spot would fire whatever NEW row landed
// there (live trace: "< Back" → immediate second click hit "In-world editor" and closed the editor).
const navClickGuard = 450 * time.Millisecond

// pointerClick activates the hovered element: the wrist toggles the editor (works while CLOSED); a
// menu row fires its action/slider (identical to an EvMouseDown); a content overlay selects it.
func (e *editor) pointerClick(h pointerHit) {
	switch {
	case h.key == wristKey:
		e.toggle()
	case h.key == stripKey:
		e.stripClick(h)
	case e.isMenuKey(h.key):
		if time.Since(e.navAt) < navClickGuard {
			e.evt("menu click SWALLOWED (page just changed - rows moved under the cursor)")
			return
		}
		items := e.shownMenu(h.key).items                  // fire from the DISPLAYED list - clicked row == seen row
		if e.menuActionAt(items, e.ptrRow, float64(h.u)) { // fire the HIGHLIGHTED row (hysteresis-stable)
			e.menuBuiltAt[h.key] = time.Time{}
		}
	case h.key == worldPathKey:
		// orbit drag stays on the laser path; a pointer click here is a no-op
	default:
		// Content overlay: point-and-trigger picks it as the edit target AND opens its editor page -
		// direct manipulation, no menu drill-down (the mint→pink outline confirms the selection in VR).
		id := strings.TrimPrefix(h.key, "page.rave.mate.")
		e.selected = id
		e.evt("pointer SELECT overlay %s", id)
		e.openFullMenu(pageSelected)
	}
}

// clearPointer hides the cursor + tooltip + clears hover feedback when the ray is on nothing.
func (e *editor) clearPointer() {
	e.ptrKey, e.ptrRow, e.wristHover = "", -1, false
	e.ptrStripCell = -1
	if e.cursorShow.changed(false) {
		_ = e.m.rt.Show(cursorKey, false)
	}
	e.hidePtrTip()
}

// placeCursor puts the cursor dot at the world hit point, billboarded toward the HMD. Width is set
// once (SetTransformMatrixWorld carries no size); the matrix is re-sent each frame it moves.
func (e *editor) placeCursor(pt [3]float32) {
	if !e.cursorEnsured {
		if e.m.rt.EnsureOverlay(cursorKey, "rave-mate cursor") != nil {
			return
		}
		_ = e.m.rt.SetTexture(cursorKey, e.m.rend.RenderDot())
		_ = e.m.rt.SetTransform(cursorKey, Transform{WidthM: cursorSizeM, Opacity: 1})
		e.cursorEnsured = true
	}
	// Delta gate (perf review): the compositor push is the expensive part - skip it while neither the
	// hit point nor the viewer moved noticeably (sub-2mm tremor is below the 1€ filter's visible floor).
	if moved3(pt, e.cursorSentAt, 0.002) || e.hmdMoved(0.005) {
		if m, ok := e.billboard(pt); ok {
			e.ed.SetTransformMatrixWorld(cursorKey, m)
			e.cursorSentAt = pt
			e.noteHMDSent()
		}
	}
	if e.cursorShow.changed(true) {
		_ = e.m.rt.Show(cursorKey, true)
	}
}

// moved3 reports whether a moved more than eps meters from b (component max - cheap, no sqrt).
func moved3(a, b [3]float32, eps float32) bool {
	d := func(x, y float32) float32 {
		if x > y {
			return x - y
		}
		return y - x
	}
	return d(a[0], b[0]) > eps || d(a[1], b[1]) > eps || d(a[2], b[2]) > eps
}

// hmdMoved reports whether the viewer moved past eps since the last cursor/tip push (billboard
// orientation staleness); uses the frame-shared pose cache, so it's one cached read, not a new fetch.
func (e *editor) hmdMoved(eps float32) bool {
	hmd, ok := e.ed.DevicePose(0)
	if !ok {
		return false
	}
	hx, hy, hz := MatPos(hmd)
	return moved3([3]float32{float32(hx), float32(hy), float32(hz)}, e.hmdSentAt, eps)
}

// noteHMDSent records the viewer position at push time (pairs with hmdMoved).
func (e *editor) noteHMDSent() {
	if hmd, ok := e.ed.DevicePose(0); ok {
		hx, hy, hz := MatPos(hmd)
		e.hmdSentAt = [3]float32{float32(hx), float32(hy), float32(hz)}
	}
}

// showPtrTip renders a small label tooltip just below the cursor (wrist + content elements). Repaints
// only when the label changes; billboards to the HMD.
func (e *editor) showPtrTip(label string, pt [3]float32) {
	if label == "" {
		e.hidePtrTip()
		return
	}
	if !e.ptrTipEnsured {
		if e.m.rt.EnsureOverlay(ptrTipKey, "rave-mate pointer tip") != nil {
			return
		}
		_ = e.m.rt.SetTransform(ptrTipKey, Transform{WidthM: ptrTipSizeM, Opacity: 1})
		e.ptrTipEnsured = true
	}
	if e.ptrTipSig != label {
		_ = e.m.rt.SetTexture(ptrTipKey, e.m.rend.RenderTooltip(label))
		e.ptrTipSig = label
	}
	// Same delta gate as the cursor: skip the compositor push while neither the anchor nor the viewer moved.
	if moved3(pt, e.tipSentAt, 0.002) || e.hmdMoved(0.005) {
		if m, ok := e.billboard([3]float32{pt[0], pt[1] - 0.06, pt[2]}); ok {
			e.ed.SetTransformMatrixWorld(ptrTipKey, m)
			e.tipSentAt = pt
			e.noteHMDSent()
		}
	}
	if e.ptrTipShow.changed(true) {
		_ = e.m.rt.Show(ptrTipKey, true)
	}
}

func (e *editor) hidePtrTip() {
	if e.ptrTipShow.changed(false) {
		_ = e.m.rt.Show(ptrTipKey, false)
	}
}

// billboard builds a world Mat34 placing a flat overlay at pos with its +Z (visible face normal)
// pointing at the HMD, so the dot/label always faces the viewer. ok=false if the HMD pose is missing.
func (e *editor) billboard(pos [3]float32) (Mat34, bool) {
	hmd, ok := e.ed.DevicePose(0)
	if !ok {
		return Mat34{}, false
	}
	hx, hy, hz := MatPos(hmd)
	z := norm3([3]float32{float32(hx) - pos[0], float32(hy) - pos[1], float32(hz) - pos[2]})
	x := norm3(cross3([3]float32{0, 1, 0}, z))
	if x == ([3]float32{}) { // aimed straight up/down → any perpendicular
		x = [3]float32{1, 0, 0}
	}
	y := cross3(z, x)
	return Mat34{
		x[0], y[0], z[0], pos[0],
		x[1], y[1], z[1], pos[1],
		x[2], y[2], z[2], pos[2],
	}, true
}

func wristTipLabel(on bool) string {
	if on {
		return "Close editor"
	}
	return "Open editor"
}

// dist3 is the euclidean distance between two points (m).
func dist3(a, b [3]float32) float32 {
	dx, dy, dz := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

func norm3(v [3]float32) [3]float32 {
	l := float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])))
	if l < 1e-6 {
		return [3]float32{}
	}
	return [3]float32{v[0] / l, v[1] / l, v[2] / l}
}

func cross3(a, b [3]float32) [3]float32 {
	return [3]float32{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
