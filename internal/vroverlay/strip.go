package vroverlay

import (
	"fmt"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/vrbind"
)

// XSOverlay-style wrist strip: a compact row of square icon buttons anchored above the wrist
// badge - the fast path (edit mode, overlay pages, show/hide, user quick buttons). The badge
// stays the summon anchor; a strip button opens the full paged menu for deep tasks. Driven by
// the same ray pointer + shown-snapshot click mapping as the menus (never the global laser).

const (
	stripKey   = "page.rave.mate.__strip"
	stripHlKey = "page.rave.mate.__striphl" // hover highlight floating over the pointed cell
)

// StripButton is one wrist-strip icon button.
type StripButton struct {
	Glyph   string // 1-3 chars drawn on the cell
	Label   string // hover tooltip
	Active  bool   // accent state (edit mode on, overlays hidden, menu open, …)
	OnClick func()
}

// stripCellAt maps a hit u (0..1) to a cell index (-1 = no cells).
func stripCellAt(u float32, n int) int {
	if n <= 0 {
		return -1
	}
	c := int(u * float32(n))
	if c < 0 {
		return 0
	}
	if c >= n {
		return n - 1
	}
	return c
}

// stripCellOffset returns cell's center offset in the STRIP's local frame (metres): x along the
// strip, z toward the viewer (+Z faces the HMD) so the highlight draws over the cell.
func stripCellOffset(cell, n int, widthM float64) (x, z float64) {
	return widthM * ((float64(cell)+0.5)/float64(n) - 0.5), 0.002
}

// stripWidthM sizes the strip quad from its cell count (square ~4.5cm cells, clamped).
func stripWidthM(n int) float64 {
	return clampF(0.045*float64(max(n, 1)), 0.09, 0.55)
}

// stripSignature is the cheap content key: re-upload only when glyphs/active states change.
func stripSignature(btns []StripButton) string {
	var b strings.Builder
	for _, s := range btns {
		fmt.Fprintf(&b, "\x00%s|%s|%v", s.Glyph, s.Label, s.Active)
	}
	return b.String()
}

// quickGlyph is a quick button's cell glyph: configured, else derived from the label/action.
func quickGlyph(q config.VRQuickButton) string {
	g := strings.TrimSpace(q.Glyph)
	if g == "" {
		g = strings.TrimSpace(q.Label)
	}
	if g == "" {
		g = q.Action
	}
	g = strings.ToUpper(g)
	if r := []rune(g); len(r) > 3 {
		return string(r[:3])
	}
	return g
}

// quickLabel is a quick button's tooltip (label, else action + target).
func quickLabel(q config.VRQuickButton) string {
	if q.Label != "" {
		return q.Label
	}
	if q.Target != "" {
		return q.Action + " " + q.Target
	}
	return q.Action
}

// buildStrip builds the wrist-strip buttons for the current state (fixed set + user quick buttons).
func (e *editor) buildStrip(feat config.VROverlayFeature) []StripButton {
	editLbl := "Edit mode: off"
	if e.editMode {
		editLbl = "Edit mode: on"
	}
	visLbl, visActive := "Hide all overlays", false
	if e.m.contentHidden {
		visLbl, visActive = "Show all overlays (hidden)", true
	}
	menuLbl := "Open full menu"
	if e.fullMenu {
		menuLbl = "Close full menu"
	}
	btns := []StripButton{
		{Glyph: "ED", Label: editLbl, Active: e.editMode, OnClick: e.toggleEditMode},
		{Glyph: "OV", Label: "Overlays", OnClick: func() { e.openFullMenu(pageOverlays) }},
		{Glyph: "+", Label: "Add overlay", OnClick: func() { e.openFullMenu(pageAdd) }},
	}
	if e.m.camPaths != nil {
		btns = append(btns, StripButton{Glyph: "CAM", Label: "Camera paths", OnClick: func() { e.openFullMenu(pageCamPaths) }})
	}
	btns = append(btns,
		StripButton{Glyph: "OBS", Label: "OBS control", OnClick: func() { e.openFullMenu(pageOBS) }},
		StripButton{Glyph: "VIS", Label: visLbl, Active: visActive, OnClick: func() { e.m.contentHidden = !e.m.contentHidden }},
		StripButton{Glyph: "MNU", Label: menuLbl, Active: e.fullMenu, OnClick: e.toggleFullMenu},
	)
	for _, q := range feat.QuickButtons {
		if q.Action == "" {
			continue
		}
		action, target := q.Action, q.Target
		btns = append(btns, StripButton{Glyph: quickGlyph(q), Label: quickLabel(q), OnClick: func() { e.fireQuickAction(action, target) }})
	}
	return btns
}

// openFullMenu opens the paged menu at page p (strip nav buttons).
func (e *editor) openFullMenu(p string) {
	e.fullMenu = true
	e.menuHidden = false
	e.gotoPage(p)
}

// toggleFullMenu opens/closes the paged menu (strip MNU button); opening lands on home.
func (e *editor) toggleFullMenu() {
	e.fullMenu = !e.fullMenu
	if e.fullMenu {
		e.menuHidden = false
		e.gotoPage("")
	}
}

// fireQuickAction routes a quick button: layout/camera-path loads run in-editor; everything else
// goes through the app's bind dispatcher (same handlers the VR slots / MIDI binds fire).
func (e *editor) fireQuickAction(action, target string) {
	e.evt("quick button %q target=%q", action, target)
	switch action {
	case "layout.load":
		e.loadLayout(target)
	case "campath.load":
		if e.m.loadCamPath != nil {
			_ = e.m.loadCamPath(target)
		}
	default:
		if e.m.bindDisp != nil {
			e.m.bindDisp.Fire(vrbind.Bind{Action: vrbind.ActionID(action), Target: target})
		}
	}
}

// stripTransform floats the strip above the wrist badge on the same hand (controller frame),
// tilted toward the eyes like the badge. Width scales with the DISPLAYED cell count.
func (e *editor) stripTransform(hand Hand, n int) Transform {
	return Transform{Snap: hand, Y: 0.12, Z: -0.04, Pitch: -55, WidthM: stripWidthM(n), Opacity: 0.96}
}

// driveStrip reconciles the wrist strip each tick: rebuild → upload on content change (shown
// snapshot commits WITH the texture, so clicks always map to the displayed cells) → transform/
// visibility on change only.
func (e *editor) driveStrip(feat config.VROverlayFeature, hand Hand) {
	if !e.on {
		if e.stripShow.changed(false) {
			_ = e.m.rt.Show(stripKey, false)
		}
		return
	}
	if !e.stripEnsured {
		if err := e.m.rt.EnsureOverlay(stripKey, "rave-mate wrist strip"); err != nil {
			e.stripFailLog("strip overlay create FAILED: %v", err)
			return
		}
		e.stripEnsured = true
	}
	btns := e.buildStrip(feat)
	if sig := stripSignature(btns); e.stripSig != sig {
		if err := e.m.rt.SetTexture(stripKey, e.m.rend.RenderStrip(btns)); err == nil {
			e.stripSig = sig
			e.stripShown = btns // click/hover map against what's displayed (menusnap discipline)
			e.stripHlSig = ""   // cell count/size may have changed → re-place the highlight
		} else {
			e.stripFailLog("strip texture upload FAILED (%d cells): %v", len(btns), err)
		}
	}
	if len(e.stripShown) == 0 {
		return // nothing displayed yet - no show, no hits
	}
	// Re-apply until the transform binds to a TRACKED controller (same fix as the wrist badge):
	// applied while the hand is untracked, Snap falls back to a world pose at the playspace
	// origin and the cached transform never re-sends - an invisible strip.
	tracked := false
	if e.ed != nil {
		_, tracked = e.ed.ControllerIndex(hand)
	}
	if tf := e.stripTransform(hand, len(e.stripShown)); e.stripTf.changed(tf) || (tracked && !e.stripAttached) {
		_ = e.m.rt.SetTransform(stripKey, tf)
		e.stripAttached = tracked
	}
	if e.stripShow.changed(true) {
		_ = e.m.rt.Show(stripKey, true)
	}
}

// stripFailLog rate-limits strip failure events (one per 5s) so a persistent failure is visible
// in the remote diag ring without flooding it.
func (e *editor) stripFailLog(format string, args ...any) {
	if time.Since(e.stripFailAt) < 5*time.Second {
		return
	}
	e.stripFailAt = time.Now()
	e.evt(format, args...)
}

// driveStripHover floats the cell highlight over the ray-pointed strip cell (transform-only -
// mirrors driveHover so hover never re-uploads the strip texture).
func (e *editor) driveStripHover(hand Hand) {
	n := len(e.stripShown)
	cell := e.ptrStripCell
	show := e.on && e.ptrKey == stripKey && cell >= 0 && cell < n
	if !show {
		if e.stripHlShow.changed(false) {
			_ = e.m.rt.Show(stripHlKey, false)
		}
		return
	}
	if !e.stripHlEnsured {
		if e.m.rt.EnsureOverlay(stripHlKey, "rave-mate strip hover") != nil {
			return
		}
		_ = e.m.rt.SetTexture(stripHlKey, e.m.rend.RenderStripHover())
		e.stripHlEnsured = true
	}
	tf := e.stripTransform(hand, n)
	sig := fmt.Sprintf("%d|%d|%v", cell, n, tf)
	if e.stripHlSig != sig {
		e.stripHlSig = sig
		cellW := tf.WidthM / float64(n)
		if e.stripHlW != cellW { // width/alpha channel (the matrix pushes carry no size)
			_ = e.m.rt.SetTransform(stripHlKey, Transform{WidthM: cellW, Opacity: 1})
			e.stripHlW = cellW
		}
		xOff, zOff := stripCellOffset(cell, n, tf.WidthM)
		local := MulMat(EulerToMat(tf.Yaw, tf.Pitch, tf.Roll, tf.X, tf.Y, tf.Z), EulerToMat(0, 0, 0, xOff, 0, zOff))
		if idx, ok := e.anchorIdx(handToSnap(tf.Snap)); ok {
			e.ed.SetTransformMatrixDevice(stripHlKey, idx, local)
		} else {
			e.ed.SetTransformMatrixWorld(stripHlKey, local)
		}
	}
	if e.stripHlShow.changed(true) {
		_ = e.m.rt.Show(stripHlKey, true)
	}
}

// stripClick fires the clicked cell against the DISPLAYED snapshot. No edit-mode gating - every strip
// button stays live while editing (the accurate pointer made the old drift-guard gating unnecessary).
func (e *editor) stripClick(h pointerHit) {
	btns := e.stripShown
	cell := stripCellAt(h.u, len(btns))
	if cell < 0 || cell >= len(btns) {
		return
	}
	b := btns[cell]
	e.evt("strip CLICK %d %q", cell, b.Label)
	if b.OnClick != nil {
		b.OnClick()
		e.stripSig = "" // state likely changed → re-render next tick
	}
}
