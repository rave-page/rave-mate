package vroverlay

import (
	"fmt"
	"image/color"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/obscontrol"
)

// Special overlay keys for the in-VR editor chrome (excluded from the content destroy sweep).
const (
	wristKey      = "page.rave.mate.__wrist"
	menuKey       = "page.rave.mate.__menu"
	menuShadowKey = "page.rave.mate.__menushadow" // ghost preview while editing the menu's own transform
	helpKey       = "page.rave.mate.__help"       // controls / keybind guide panel
	tipKey        = "page.rave.mate.__tip"        // hover tooltip: full text of a truncated menu row
	posKey        = "page.rave.mate.__pos"        // edit-mode positioning menu (nudge the selected overlay)
	hoverHlKey    = "page.rave.mate.__hoverhl"    // row-highlight accent floating over the hovered menu row (hover is NOT baked into the menu texture)
	selOutlineKey = "page.rave.mate.__seloutline" // brand frame on the SELECTED content overlay (edit mode)
	hovOutlineKey = "page.rave.mate.__hovoutline" // mint frame on the pointer-hovered content overlay (edit mode "selectable" cue)
	dashKey       = "rave.page.mate.dash"         // SteamVR dashboard tab (fallback menu)
)

// pendMenu is a previewed menu transform (shown as a shadow clone) until the user hits Apply.
type pendMenu struct {
	active                     bool
	snap                       string
	x, y, z, yaw, pitch, width float64
}

func contentKey(id string) string { return "page.rave.mate." + id }

// grab holds an in-progress drag: the firing controller + the overlay's offset in that controller's
// frame at grab time, so the overlay rigidly follows the hand.
type grab struct {
	key     string
	idx     int
	offset  Mat34
	last    Mat34
	snap    string    // overlay's snap mode at grab time (drop converts world→this anchor's local frame)
	startAt time.Time // for the drop grace period
}

// grabClickDebounce ignores laser clicks for a moment after pick-up so the same trigger pull that
// grabbed a surface can't immediately drop it. grabSafetyTimeout frees a grab if the drop click is
// somehow lost (e.g. the laser slid off the surface), so nothing gets stuck attached to the hand.
const (
	grabClickDebounce = 350 * time.Millisecond
	grabSafetyTimeout = 25 * time.Second
	grabHoldDelay     = 400 * time.Millisecond // deliberate-grab hold: grip must be held (past the force threshold) this long before a grab engages (kills accidental light-squeeze grabs)
)

// cachedBool / cachedTf skip redundant OpenVR calls: changed() reports + records a new value only
// when it differs from the last (or was never set). Re-sending Show/SetTransform every tick adds
// compositor load that drops the host VR app's framerate.
type cachedBool struct {
	v, set bool
}

func (c *cachedBool) changed(n bool) bool {
	if !c.set || c.v != n {
		c.v, c.set = n, true
		return true
	}
	return false
}

type cachedTf struct {
	v        Transform
	set      bool
	attached bool // last apply happened while the snap anchor was TRACKED
}

func (c *cachedTf) changed(n Transform) bool {
	if !c.set || c.v != n {
		c.v, c.set = n, true
		return true
	}
	return false
}

// needsApply is changed() plus the untracked-anchor rule: a transform applied while its
// snap device is untracked anchors at the playspace origin (transformWorld world fallback)
// and a pure value cache never re-sends - an invisible surface (the "menu button
// disappeared" bug; same class the wrist badge + strip hit). Re-apply once tracked.
func (c *cachedTf) needsApply(n Transform, tracked bool) bool {
	if !c.set || c.v != n || (tracked && !c.attached) {
		c.v, c.set, c.attached = n, true, tracked
		return true
	}
	return false
}

// editor implements in-VR editing over the Editor interface: a wrist toggle button, a floating
// menu, grab-to-move + scroll-to-resize, and live config persistence. Inert on default builds (the
// Manager only creates it when the runtime implements Editor).
type editor struct {
	m          *Manager
	ed         Editor
	on         bool // editor session active (summon opens it)
	menuHidden bool // hide the MAIN menu but keep the session + edit mode + positioning menu (edit-mode "hide menu")
	editMode   bool // edit mode: content overlays outlined + grabbable by pointing (see part-split from `on`)
	selected   string
	grab       *grab

	evtMu       sync.Mutex // guards evtRing + dbg + menuDiagStr (written on the editor goroutine, read via InputDiag)
	evtRing     []string   // recent interaction events (summon/click/grab/pointer) for remote debugging
	dbg         ptrDebug   // last-frame pointer/editor snapshot (menu/hands/hit geometry) for remote debugging
	menuDiagStr string     // per-menu alignment diag: shown rows + uploaded WxH + GPU-reported size/bounds (updateMenuDiag)
	lastPtrLog  time.Time  // throttle for the continuous pointer-position trace (so drift shows over time)
	ptrUVDeltaM float32    // last |UVWorld(uv) − ray pt| (m) - divergence of runtime UV vs ray reconstruction

	// SteamVR Input action grab (hold-to-move on the laser-hovered surface). hoverKey tracks the
	// surface the laser is currently on; grabViaAction marks a grab started by the held action (so it
	// drops on release, vs. a click-to-grab that drops on the next click).
	hoverKey      string
	hoverIdx      int
	hoverAt       time.Time
	grabViaAction bool
	grabHeldPrev  bool            // grip-held last tick (log the down edge once, not at 90Hz)
	grabArmKey    string          // surface the grip is arming a grab on ("" = none); engages after grabHoldDelay
	grabArmAt     time.Time       // when the current grab-arm hold began (deliberate-grab delay)
	contentInter  map[string]bool // content overlay key → laser interactivity on (hybrid: laser grabs content in edit mode)

	// Per-surface caches (skip redundant per-tick OpenVR calls - the big VR-framerate lever).
	wristOn       bool                  // last-rendered wrist highlight state
	wristPainted  bool                  // wrist texture uploaded at least once
	wristEnsured  bool                  // wrist overlay created
	wristAttached bool                  // wrist transform bound to a TRACKED controller (not the world fallback)
	wristTf       cachedTf              // wrist transform
	wristShow     cachedBool            // wrist visibility
	menuEnsured   bool                  // menu overlay created (floating menu; laser-free, driven by the ray pointer)
	menuTf        cachedTf              // menu transform
	menuShow      cachedBool            // menu visibility
	shadowEnsured bool                  // shadow overlay created
	shadowShow    cachedBool            // shadow visibility
	shadowTf      cachedTf              // shadow transform
	shadowSig     string                // shadow content signature (re-upload on change)
	menuInter     map[string]bool       // dashboard key → interactivity configured (floating menu is laser-free)
	menuMh        map[string]int        // menu/dash key → height the laser hit-area (mouse scale) is set to
	menuItems     map[string][]MenuItem // per-menu-key cached items (rebuilt at most every menuRebuild)
	menuBuiltAt   map[string]time.Time  // per-menu-key last rebuild time
	menuShown     map[string]menuSnap   // per-menu-key snapshot of the list the DISPLAYED texture was rendered from - ALL hover/click row mapping uses this, never the live rebuilt list (see uploadMenu)
	menuTexWH     map[string][2]int     // per-menu-key last-uploaded texture dims (diag: compare vs GPU-reported size)
	page          string                // current menu sub-page ("" = home); drill-down nav
	dashInit      bool                  // dashboard tab created
	menuSig       map[string]string     // per-menu-key last-UPLOADED label signature (committed only on SetTexture success; skip re-upload → no flicker)
	texFailAt     time.Time             // throttle for texture-upload failure events (don't flood the ring at tick rate)

	// Hover-highlight overlay (driveHover): a tiny accent floating over the ray-hovered menu row so
	// hover moves never re-upload the big menu texture (flicker + GPU churn).
	hoverEnsured bool       // hover overlay created + textured
	hoverShow    cachedBool // hover overlay visibility
	hoverSig     string     // last-applied {key,row,rows,tf} placement signature
	hoverW       float64    // last-applied hover overlay width (re-send WidthM only on change)

	// Edit-mode overlay outlines (driveEditOutlines): a brand frame on the SELECTED content overlay + a
	// mint frame on the pointer-hovered one, so what's selectable / what's selected is visible in VR.
	selOutlineEnsured bool
	selOutlineShow    cachedBool
	selOutlineW       float64 // last-applied width (re-send WidthM only on change)
	selOutlineCol     int     // last-rendered outline tint state: 0 selected(pink) / 1 arming(amber) / 2 grabbing(mint)
	hovOutlineEnsured bool
	hovOutlineShow    cachedBool
	hovOutlineW       float64

	// Positioning menu (edit mode): a second floating overlay, beside the main menu, that nudges the
	// selected overlay (move/rotate/tilt/depth buttons) + hosts the edit-mode-safe controls (exit edit,
	// show/hide main menu). Driven by the SAME ray pointer as the main menu (reuses menuItems[posKey]).
	posEnsured bool
	posTf      cachedTf
	posShow    cachedBool

	// Wrist icon strip (strip.go): XSOverlay-style fast-path buttons above the wrist badge. The
	// summon flow opens the strip; its MNU button opens the full paged menu (fullMenu).
	stripEnsured   bool
	stripAttached  bool      // transform bound to a TRACKED controller (see driveStrip re-apply)
	stripFailAt    time.Time // throttle for strip failure events
	stripShow      cachedBool
	stripTf        cachedTf
	stripSig       string        // last-UPLOADED strip signature (committed with the texture)
	stripShown     []StripButton // DISPLAYED buttons - clicks/hover map against these (menusnap discipline)
	stripHlEnsured bool
	stripHlShow    cachedBool
	stripHlSig     string
	stripHlW       float64
	ptrStripCell   int  // strip cell under the pointer (meaningful only while ptrKey == stripKey)
	fullMenu       bool // paged menu open (strip is the fast path; a strip button toggles this)

	worldPathOn      bool        // 3D camera-path preview panel active
	worldPathGeom    CamPathGeom // the path being previewed (world positions + speed + duration)
	worldPathInit    bool        // preview overlay created
	worldPathPlaying bool        // marker flying the path (play/pause)
	worldPathT       float64     // playback head (seconds)
	worldPathYaw     float32     // orbit yaw (auto-rotates until the user drags)
	worldPathPitch   float32     // orbit pitch (laser-drag)
	worldPathZoom    float32     // orbit zoom (scroll; 0 = uninit → defaults, 1 = auto-fit)
	worldPathManual  bool        // user took manual orbit control → stop auto-spin
	worldPathInter   bool        // preview overlay laser-interactivity enabled
	worldPathDrag    bool        // laser button held on the preview (orbiting)
	worldPathDragX   float32     // last laser x while dragging
	worldPathDragY   float32     // last laser y while dragging
	worldPathLast    time.Time   // last tick time (for dt)
	worldPathSig     string      // last-rendered frame signature (skip re-upload)
	worldPathShow    cachedBool  // preview visibility
	worldPathTf      cachedTf    // preview transform

	helpOn      bool       // controls/keybind guide panel visible
	helpEnsured bool       // help overlay created
	helpPainted bool       // help texture uploaded
	helpTf      cachedTf   // help transform
	helpShow    cachedBool // help visibility

	// Hover tooltip: shows a menu row's full text when the laser is over a row whose label was
	// truncated to fit. Re-rendered only when the hovered text changes.
	tipEnsured bool
	tipShow    cachedBool
	tipTf      cachedTf
	tipSig     string
	hoverRow   int       // last laser-hovered menu row (-1 = none/title)
	hoverRowAt time.Time // when the row hover was last seen (clears the tooltip on leave)

	// Ray pointer (XSOverlay-style, pointer.go): point a controller at any visible rave-mate overlay →
	// a cursor dot tracks the aim, the hovered element highlights + tooltips, trigger activates it. All
	// manual ray→overlay intersection + our own trigger action - never the global interactive laser (so
	// it coexists with VRChat). Works with the editor CLOSED (open it by pointing at the wrist badge).
	wristHover        bool       // wrist badge under the pointer (mint ring)
	wristHoverPainted bool       // last-rendered wrist hover state
	cursorEnsured     bool       // cursor-dot overlay created + textured
	cursorShow        cachedBool // cursor visibility
	ptrTipEnsured     bool       // pointer tooltip overlay created
	ptrTipShow        cachedBool // pointer tooltip visibility
	ptrTipSig         string     // last tooltip label (repaint on change)
	ptrKey            string     // overlay key currently under the pointer ("" = none)
	ptrRow            int        // hovered menu row for highlight (-1 = none/title)
	ptrDragActive     bool       // trigger held on a slider row → dragging its value (pull to 0/100%)
	activeHand        Hand       // hand that drives the ray pointer (set by whichever pulls the trigger; not both → no hand-jitter)

	// Pointer 1€-smoothing (pointer_smooth.go): filter the hit UV + world point so tremor (summed from
	// both hands when pointing at a hand-held menu) doesn't wobble the cursor / bounce the hovered row.
	ptrUV, ptrPt  oneEuro3
	ptrSmoothKey  string    // overlay the filters currently track (reset on change → no cross-overlay lerp)
	ptrSmoothHand Hand      // active hand the filters track (reset on hand switch)
	ptrSmoothAt   time.Time // last smoothed-frame time (for dt)

	// 90Hz-loop cost control (perf review 2026-07-02): one AimPose read/frame shared by cast + debug;
	// touch sweep probed at 1/3 rate while out of touch range; scratch slice kills per-frame allocs;
	// cursor/tooltip compositor pushes delta-gated (see placeCursor).
	ptrFrame     uint32     // frame counter (touch-probe cadence)
	frameAim     Mat34      // this frame's AimPose(activeHand)
	frameAimOK   bool       //
	touchLive    bool       // near-field touch was active last frame → keep sweeping every frame
	candScratch  []string   // pointerCandidates reuse
	cursorSentAt [3]float32 // last cursor world pos pushed to the compositor
	tipSentAt    [3]float32 // last tooltip world pos pushed
	hmdSentAt    [3]float32 // HMD pos at last cursor/tip push (billboard orientation staleness gate)

	// Device→tip(aim) correction learned per hand while the /pose/tip action is live, so the ray still
	// lands where the controller points if that action goes inactive (stale SteamVR binding). Index=Hand.
	tipCorr   [4]Mat34
	tipCorrOK [4]bool
	aimWarnAt time.Time // throttle the "aim pose unbound" diagnostic

	thumbAt time.Time // last edit-mode thumbstick-nudge frame (for dt)
	navAt   time.Time // last menu page change (navClickGuard: rows reflow under the cursor - swallow the immediate next click)

	btnPressed   bool      // summon button currently held
	btnDownAt    time.Time // when the hold began
	btnLongFired bool      // long-press already triggered this hold

	pend pendMenu // pending menu transform (shadow preview until Apply)
}

// resetSession clears all per-connection editor state so a SteamVR reconnect recreates + repaints
// every surface (stale "already painted" flags otherwise leave overlays blank after a reconnect).
func (e *editor) resetSession() {
	*e = editor{m: e.m, ed: e.ed, menuSig: map[string]string{},
		menuInter: map[string]bool{}, menuMh: map[string]int{}, menuItems: map[string][]MenuItem{}, menuBuiltAt: map[string]time.Time{},
		menuShown: map[string]menuSnap{}, menuTexWH: map[string][2]int{}, contentInter: map[string]bool{}}
}

// menuSnap is the immutable {items, row count} snapshot of the list a menu overlay's CURRENTLY
// DISPLAYED texture was rendered from. Committed only at successful texture upload (uploadMenu);
// the input loop maps hover/click rows against it, so what is clicked is always what is shown -
// even when the live menuItems already rebuilt for a new page whose upload is pending or failed.
type menuSnap struct {
	items []MenuItem
	rows  int
}

// shownMenu returns key's displayed-texture snapshot (zero snapshot = nothing uploaded yet → no
// row can map, no click can fire).
func (e *editor) shownMenu(key string) menuSnap { return e.menuShown[key] }

// uploadMenu renders items + uploads the menu texture; on success commits the signature AND the
// shown snapshot together. On failure neither moves: the old sig forces a retry next tick, and
// clicks keep mapping to the OLD list - matching the old texture still on the compositor. This is
// what keeps pointer mapping correct even if OpenVR rejects an upload (e.g. a raw-texture resize).
// Hover is NOT rendered into the texture (see driveHover) - uploads happen on CONTENT change only.
func (e *editor) uploadMenu(key, title string, items []MenuItem, bg float64, sig string) bool {
	img := e.m.rend.RenderMenu(title, items, bg)
	if err := e.m.rt.SetTexture(key, img); err != nil {
		if time.Since(e.texFailAt) > 2*time.Second {
			e.texFailAt = time.Now()
			e.evt("menu texture upload FAILED %s rows=%d: %v", key, len(items), err)
		}
		return false
	}
	if e.menuShown[key].rows != len(items) {
		// Texture height changed → the quad's aspect changes. Re-send the transform (incl. WidthM) so
		// SteamVR re-derives the quad from the NEW texture, and drop row state indexed to the OLD one:
		// the smoothRow hysteresis band + the hover accent would otherwise pin to stale pixel rows.
		switch key {
		case menuKey:
			e.menuTf = cachedTf{}
			e.hoverRow = -1
		case posKey:
			e.posTf = cachedTf{}
		}
		if e.ptrKey == key {
			e.ptrRow = -1
		}
		e.hoverSig = ""
	}
	e.menuSig[key] = sig
	e.menuShown[key] = menuSnap{items: items, rows: len(items)}
	e.menuTexWH[key] = [2]int{img.Bounds().Dx(), img.Bounds().Dy()}
	return true
}

// Menu drill-down page names (buildMenu pages; the wrist strip jumps straight to them).
const (
	pageOverlays  = "OVERLAYS"
	pageAdd       = "ADD OVERLAY"
	pageCamPaths  = "CAMERA PATHS"
	pageSelected  = "SELECTED OVERLAY"
	pageOBS       = "OBS CONTROL"
	pageLayouts   = "LAYOUTS"
	pageMenuPlace = "MENU PLACEMENT"
	pageMotion    = "MOTION (VRChat OSC)"
	pageControls  = "CONTROLS / KEYBINDS"
)

// gotoPage switches the menu's drill-down page ("" = home) and invalidates the cached menu so both the
// floating menu + dashboard rebuild on the next tick.
func (e *editor) gotoPage(p string) {
	e.page = p
	e.navAt = time.Now()
	e.menuBuiltAt[menuKey] = time.Time{}
	e.menuBuiltAt[dashKey] = time.Time{}
}

// longPress is how long to hold the summon button before it opens the editor (a short tap toggles
// overlay visibility instead).
const longPress = 450 * time.Millisecond

func (e *editor) isGrabbing(key string) bool { return e.grab != nil && e.grab.key == key }

func (e *editor) toggle() {
	e.on = !e.on
	e.evt("editor %s", map[bool]string{true: "OPENED", false: "CLOSED"}[e.on])
	if !e.on {
		e.grab = nil
		e.pend = pendMenu{} // drop any menu-placement preview so no ghost lingers
		e.shadowSig = ""
		e.helpOn = false
		e.fullMenu = false   // next summon opens just the wrist strip (fast path)
		e.menuHidden = false // reopen the main menu next session
		if e.editMode {      // closing the menu also leaves edit mode (drops outlines/grab)
			e.editMode = false
			e.evt("edit mode false (menu closed)")
		}
	}
}

// isMenuKey reports whether key is a ray-driven floating menu (main menu or positioning menu) - both
// route hover/click through the same menuItems[key] path.
func (e *editor) isMenuKey(key string) bool { return key == menuKey || key == posKey }

// setEditMode toggles edit mode (content outlines + point-to-grab); logged for remote verify.
func (e *editor) setEditMode(on bool) {
	if e.editMode == on {
		return
	}
	e.editMode = on
	if !on {
		e.menuHidden = false // leaving edit mode restores the main menu (hide is an edit-mode convenience)
	}
	e.evt("edit mode %v", on)
}

// toggleEditMode flips edit mode. Plain on/off: the accurate analytic pointer removed the need for the
// old two-tap drift guard (one click = active, click again = off, as the user expects).
func (e *editor) toggleEditMode() { e.setEditMode(!e.editMode) }

// tick runs the editor each Manager frame. Every OpenVR call is cached (changed-only) - re-sending
// transform/visibility/interactivity each tick adds compositor load that drops the host VR app's fps.
func (e *editor) tick(feat config.VROverlayFeature) {
	hand := HandFromString(feat.ResolvedEditHand())

	// 3D camera-path preview panel (orbit view beside the menu; gated to the editor being open).
	e.driveWorldPath(feat, hand)

	// NB: SteamVR Input actions are pumped from the Manager's fast input ticker (handleActions @ ~90Hz),
	// NOT here - a click/double-click pulse is far shorter than the 100ms overlay tick, so polling input
	// at 10fps misses the rising edge (held actions like grab survive, quick toggles don't).

	// 1. Wrist badge - an on/off indicator on the wrist (gaze-gated visibility). It's interactive ONLY
	// while the editor is open (clicking it closes), because the interactivity flag is global and would
	// otherwise steal the controller from the game whenever the badge is in view. Open the editor via a
	// controller bind (toggle_editor) or the SteamVR dashboard tab.
	if !e.wristEnsured {
		_ = e.m.rt.EnsureOverlay(wristKey, "rave-mate edit")
		e.wristEnsured = true
	}
	// Re-apply until it binds to a TRACKED controller: right after a SteamVR restart the hand isn't
	// tracked yet, so the first SetTransform falls back to a world pose (wrong spot). Keep re-applying
	// each tick until ControllerIndex is valid, then cache. Without this the badge sticks in world space
	// after an in-session SteamVR restart (a fresh rave-mate launch avoids it - controllers are up).
	tf := e.wristTransform(feat, hand)
	_, tracked := e.ed.ControllerIndex(hand)
	if e.wristTf.changed(tf) || (tracked && !e.wristAttached) {
		_ = e.m.rt.SetTransform(wristKey, tf)
		e.wristAttached = tracked
	}
	if !e.wristPainted || e.wristOn != e.on || e.wristHoverPainted != e.wristHover {
		_ = e.m.rt.SetTexture(wristKey, e.m.rend.RenderWrist(e.on, e.wristHover))
		e.wristOn, e.wristHoverPainted, e.wristPainted = e.on, e.wristHover, true
	}
	wristVis := e.wristGating(hand) || e.on
	if e.wristShow.changed(wristVis) {
		_ = e.m.rt.Show(wristKey, wristVis)
	}
	// The wrist badge is NEVER made interactive (that GLOBAL laser mode captured the controller from
	// the game). The ray pointer (pointer.go) is the sole cursor: point at the badge + trigger to
	// open/close. It's a passive on/off + aim-target indicator here.

	// 2. Summon (open/tap-hide) is handled in handleActions at 90 Hz (IVRInput), not here.

	// 2b. Wrist icon strip - the session's fast path (strip.go); the badge summons it, the strip's
	// MNU button opens the full paged menu below.
	e.driveStrip(feat, hand)

	// 3. Centralized grab-follow (content or menu): the grabbed surface is parented to the controller
	// it was picked up with (SetTransformMatrixDevice, set once in startGrab) so SteamVR tracks it to
	// the hand at full framerate - smooth, with zero per-tick pose math. Recomputing a world matrix at
	// the 10fps tick used to make the carry jittery and add needless load. CLICK-TO-GRAB /
	// CLICK-TO-DROP: a laser click picks up, a second drops (legacy GetControllerState returns nothing
	// on Index/Touch, so the grab is driven from the laser events). A long safety timeout frees a stuck
	// grab. Per tick we only poll for the drop click + nudge depth via push/pull (re-attaches on change).
	if e.grab != nil {
		drop := false
		for _, ev := range e.ed.PollEvents(e.grab.key) {
			e.noteHover(e.grab.key, ev.Device)
			// Click-to-drop applies only to click-grabs; an action-grab (grip held) drops on release,
			// handled in handleActions, so a trigger click while carrying doesn't drop it.
			if !e.grabViaAction && ev.Type == EvMouseDown && time.Since(e.grab.startAt) > grabClickDebounce {
				drop = true
			}
		}
		if drop || time.Since(e.grab.startAt) > grabSafetyTimeout {
			e.endGrab()
			e.grabViaAction = false
		} else {
			pp := e.ed.ActPushPull()
			if math.Abs(float64(pp)) < 0.15 {
				pp = e.ed.ThumbY(e.grab.idx) // fall back to the legacy axis if push/pull isn't bound
			}
			if math.Abs(float64(pp)) > 0.15 {
				z := e.grab.offset[11] - pp*0.03 // −Z is forward in the controller frame (push = away)
				e.grab.offset[11] = clampF32(z, -6, -0.05)
				e.ed.SetTransformMatrixDevice(e.grab.key, e.grab.idx, e.grab.offset) // re-attach with the new depth
			}
		}
	}

	// 4. Floating menu - the DEEP-TASK surface, opened from the wrist strip (fullMenu). Hidden while
	// menuHidden (edit-mode "hide menu"), but the session/edit mode/positioning menu stay live.
	showMain := e.on && e.fullMenu && !e.menuHidden
	if showMain {
		if !e.menuEnsured {
			_ = e.m.rt.EnsureOverlay(menuKey, "rave-mate menu")
			e.menuEnsured = true
		}
		if !e.isGrabbing(menuKey) {
			if tf := e.menuTransform(feat, hand); e.menuTf.needsApply(tf, e.snapTracked(tf)) {
				_ = e.m.rt.SetTransform(menuKey, tf)
			}
		}
		if e.menuShow.changed(true) {
			_ = e.m.rt.Show(menuKey, true)
		}
		e.driveMenu(menuKey, "rave-mate", feat, hand, true)
		e.tickShadow()
		e.tickHelp(feat, hand)
		e.tickTooltip(feat, hand)
	} else {
		if e.menuShow.changed(false) {
			_ = e.m.rt.Show(menuKey, false)
		}
		e.setMenuLaser(false) // closed → release the laser so it never captures the controller during play
		if e.shadowShow.changed(false) {
			_ = e.m.rt.Show(menuShadowKey, false)
		}
		if e.helpShow.changed(false) {
			_ = e.m.rt.Show(helpKey, false)
		}
		if e.tipShow.changed(false) {
			_ = e.m.rt.Show(tipKey, false)
		}
	}
	e.tickPosMenu(feat, hand) // edit-mode positioning menu (own overlay; shown even when the main menu is hidden)

	// 5. SteamVR dashboard tab - same menu, fallback (not draggable; SteamVR positions it).
	if ok, _ := e.ed.EnsureDashboard(dashKey, "rave-mate"); ok {
		if !e.dashInit {
			_ = e.m.rt.SetTexture(dashKey+".thumb", e.m.rend.RenderWrist(true, false))
			e.dashInit = true
		}
		e.driveMenu(dashKey, "rave-mate (dashboard)", feat, hand, false)
	}

	// 6. Content overlays: SteamVR laser-grabbable while the menu is open + in edit mode (hybrid - the
	// laser does select/grab, the custom pointer is wrist-only). Disabled otherwise so nothing captures
	// the controller during play.
	e.driveContentLaser(feat)
	e.driveEditOutlines(feat) // edit-mode selectable/selected frames on content overlays

	// 7. Menu-alignment diag (remote): shown rows + uploaded WxH vs GPU-reported size/bounds.
	e.updateMenuDiag()
}

// setMenuLaser enables/disables the floating menu's SteamVR laser interactivity (open → laser handles
// rows/sliders/grab; closed → released so the controller is free for the game). driveMenu enables it
// while open; this disables it on close.
func (e *editor) setMenuLaser(on bool) {
	if e.menuInter[menuKey] == on {
		return
	}
	e.ed.SetInteractive(menuKey, MenuW, e.menuMh[menuKey], on)
	e.menuInter[menuKey] = on
}

// driveContentLaser releases any leftover SteamVR laser interactivity on content overlays. Content is
// now driven by the custom ray pointer (edit mode): hover/select via pointerClick, grip-grab via the
// ray's noteHover - all of which coexist with the game. SteamVR's interactive laser is never enabled on
// content, because MakeOverlaysInteractiveIfVisible would globally capture the controller from VRChat.
func (e *editor) driveContentLaser(feat config.VROverlayFeature) {
	for _, o := range feat.Overlays {
		key := contentKey(o.ID)
		if e.contentInter[key] {
			e.ed.SetInteractive(key, panelW, panelH, false)
			e.contentInter[key] = false
		}
	}
}

// driveEditOutlines frames content overlays in edit mode so "selectable / selected" is visible: a brand
// frame on the SELECTED overlay + a mint frame on the pointer-hovered one. Hidden outside edit mode, when
// overlays are hidden, or while that overlay is grabbed (its live hand pose diverges from the stored
// transform). The frame is placed on the overlay's own quad (same anchor), nudged 3mm toward its face.
func (e *editor) driveEditOutlines(feat config.VROverlayFeature) {
	editing := e.on && e.editMode && !e.m.contentHidden
	selOn := false
	if editing {
		if o := findOvByID(feat, e.selected); o != nil && o.Enabled {
			selKey := contentKey(o.ID)
			grabbing := e.isGrabbing(selKey)
			// Tint tells you the grab state at a glance: pink = selected, amber = grip arming a grab,
			// mint = grabbing (moving with the hand).
			col, cstate := colName, 0
			switch {
			case grabbing:
				col, cstate = colMint, 2
			case e.grabArmKey == selKey:
				col, cstate = colAmber, 1
			}
			if e.ensureOutline(&e.selOutlineEnsured, selOutlineKey, col) {
				if e.selOutlineCol != cstate {
					_ = e.m.rt.SetTexture(selOutlineKey, e.m.rend.RenderOutline(col))
					e.selOutlineCol = cstate
				}
				if grabbing { // ride the grab (config transform is stale while carried at full fps)
					e.placeGrabOutline(selKey, o.ResolvedWidthM())
				} else {
					e.placeEditOutline(selOutlineKey, *o, &e.selOutlineW)
				}
				if e.selOutlineShow.changed(true) {
					_ = e.m.rt.Show(selOutlineKey, true)
				}
				selOn = true
			}
		}
	}
	if !selOn && e.selOutlineShow.changed(false) {
		_ = e.m.rt.Show(selOutlineKey, false)
	}
	hovOn := false
	if editing {
		if id, ok := e.contentKeyID(feat, e.ptrKey); ok && id != e.selected {
			if o := findOvByID(feat, id); o != nil && o.Enabled && !e.isGrabbing(e.ptrKey) {
				if e.ensureOutline(&e.hovOutlineEnsured, hovOutlineKey, colMint) {
					e.placeEditOutline(hovOutlineKey, *o, &e.hovOutlineW)
					if e.hovOutlineShow.changed(true) {
						_ = e.m.rt.Show(hovOutlineKey, true)
					}
					hovOn = true
				}
			}
		}
	}
	if !hovOn && e.hovOutlineShow.changed(false) {
		_ = e.m.rt.Show(hovOutlineKey, false)
	}
}

// ensureOutline lazily creates + textures an outline overlay in col. Returns false if creation failed.
func (e *editor) ensureOutline(ensured *bool, key string, col color.Color) bool {
	if *ensured {
		return true
	}
	if e.m.rt.EnsureOverlay(key, "rave-mate outline") != nil {
		return false
	}
	_ = e.m.rt.SetTexture(key, e.m.rend.RenderOutline(col))
	*ensured = true
	return true
}

// placeEditOutline sets an outline overlay onto a content overlay's quad: WidthM = the overlay's width
// (re-sent only on change), the matrix = the overlay's own local transform nudged 3mm along its +Z face
// so the frame draws in front. Attaches device-relative for hand/head-snapped overlays (full-fps follow),
// else world - mirroring the content overlay's own anchoring so the frame tracks it exactly.
func (e *editor) placeEditOutline(key string, o config.VROverlay, w *float64) {
	if wantW := o.ResolvedWidthM(); *w != wantW {
		_ = e.m.rt.SetTransform(key, Transform{WidthM: wantW, Opacity: 1})
		*w = wantW
	}
	local := MulMat(EulerToMat(o.Yaw, o.Pitch, o.Roll, o.X, o.Y, o.Z), EulerToMat(0, 0, 0, 0, 0, 0.003))
	if idx, ok := e.anchorIdx(o.SnapTo); ok {
		e.ed.SetTransformMatrixDevice(key, idx, local)
	} else {
		e.ed.SetTransformMatrixWorld(key, local)
	}
}

// placeGrabOutline rides the selected outline on a grabbed overlay: the grab tracks the overlay to the
// hand at full fps, so the outline shares the SAME device + offset (the stored config transform is stale
// mid-carry). Nudged 3mm along the quad face so it frames the panel.
func (e *editor) placeGrabOutline(key string, widthM float64) {
	if e.grab == nil {
		return
	}
	if e.selOutlineW != widthM {
		_ = e.m.rt.SetTransform(key, Transform{WidthM: widthM, Opacity: 1})
		e.selOutlineW = widthM
	}
	e.ed.SetTransformMatrixDevice(key, e.grab.idx, MulMat(e.grab.offset, EulerToMat(0, 0, 0, 0, 0, 0.003)))
}

// contentKeyID returns the overlay ID if key is a content overlay (not editor chrome), else ok=false.
// Chrome keys (__menu, cursor, …) never match an overlay ID, so the findOvByID lookup is the filter.
func (e *editor) contentKeyID(feat config.VROverlayFeature, key string) (string, bool) {
	if !strings.HasPrefix(key, "page.rave.mate.") {
		return "", false
	}
	id := strings.TrimPrefix(key, "page.rave.mate.")
	if findOvByID(feat, id) == nil {
		return "", false
	}
	return id, true
}

// tickShadow renders the menu-placement preview: a dim full ghost of the menu where it'll snap on
// Apply. Re-uploaded only when the pending transform changes.
func (e *editor) tickShadow() {
	if !e.pend.active {
		if e.shadowShow.changed(false) {
			_ = e.m.rt.Show(menuShadowKey, false)
		}
		return
	}
	if !e.shadowEnsured {
		_ = e.m.rt.EnsureOverlay(menuShadowKey, "rave-mate menu preview")
		e.shadowEnsured = true
	}
	w := e.pend.width
	if w <= 0 {
		w = 0.36
	}
	// Ghost sized to the DISPLAYED menu (same rows → same footprint at the same WidthM), but
	// translucent and UI-less so it reads as a placeholder, not a second menu.
	rows := e.shownMenu(menuKey).rows
	if sig := fmt.Sprintf("ghost%d", rows); e.shadowSig != sig {
		_ = e.m.rt.SetTexture(menuShadowKey, e.m.rend.RenderGhost(rows))
		e.shadowSig = sig
	}
	if tf := (Transform{Snap: HandFromString(e.pend.snap), X: e.pend.x, Y: e.pend.y, Z: e.pend.z, Yaw: e.pend.yaw, Pitch: e.pend.pitch, WidthM: w, Opacity: 0.5}); e.shadowTf.needsApply(tf, e.snapTracked(tf)) {
		_ = e.m.rt.SetTransform(menuShadowKey, tf)
	}
	if e.shadowShow.changed(true) {
		_ = e.m.rt.Show(menuShadowKey, true)
	}
}

// tickHelp shows the controls/keybind guide panel beside the menu when toggled on.
func (e *editor) tickHelp(feat config.VROverlayFeature, hand Hand) {
	if !e.helpOn {
		if e.helpShow.changed(false) {
			_ = e.m.rt.Show(helpKey, false)
		}
		return
	}
	if !e.helpEnsured {
		_ = e.m.rt.EnsureOverlay(helpKey, "rave-mate help")
		e.helpEnsured = true
	}
	if !e.helpPainted {
		_ = e.m.rt.SetTexture(helpKey, e.m.rend.Panel(helpLines(feat), panelW, panelH, 0.92))
		e.helpPainted = true
	}
	// Float the help panel to the right of the menu (or beside the edit hand when menu is auto-placed).
	base := e.menuTransform(feat, hand)
	base.X += 0.42
	base.WidthM = 0.42
	base.Opacity = 0.97
	if e.helpTf.needsApply(base, e.snapTracked(base)) {
		_ = e.m.rt.SetTransform(helpKey, base)
	}
	if e.helpShow.changed(true) {
		_ = e.m.rt.Show(helpKey, true)
	}
}

// tickPosMenu renders the edit-mode positioning menu beside the main menu: discrete nudge buttons for
// the selected overlay + the controls that must stay live while edit mode gates/hides the main menu
// (exit edit, show/hide main menu). Shown for the whole edit-mode session - even with nothing selected
// (the exit/show controls stay reachable) and even when the main menu is hidden. Driven by the same ray
// pointer as the main menu (reuses menuItems[posKey]); never touches the global interactive laser.
func (e *editor) tickPosMenu(feat config.VROverlayFeature, hand Hand) {
	if !(e.on && e.editMode) {
		if e.posShow.changed(false) {
			_ = e.m.rt.Show(posKey, false)
		}
		return
	}
	if !e.posEnsured {
		_ = e.m.rt.EnsureOverlay(posKey, "rave-mate position")
		e.posEnsured = true
	}
	now := time.Now()
	items, ok := e.menuItems[posKey]
	if !ok || now.Sub(e.menuBuiltAt[posKey]) >= menuRebuild {
		items = e.buildPosMenu(feat)
		e.menuItems[posKey] = items
		e.menuBuiltAt[posKey] = now
	}
	sig := "POSITION\x00" + menuSignature(items)
	if e.menuSig[posKey] != sig {
		e.uploadMenu(posKey, "POSITION", items, feat.ResolvedMenuBg(), sig)
	}
	base := e.menuTransform(feat, hand) // beside the main menu (its left), anchored to the same frame
	base.X -= base.WidthM*0.5 + 0.20
	base.WidthM = 0.34
	base.Opacity = 0.97
	if e.posTf.needsApply(base, e.snapTracked(base)) {
		_ = e.m.rt.SetTransform(posKey, base)
	}
	if e.posShow.changed(true) {
		_ = e.m.rt.Show(posKey, true)
	}
}

// buildPosMenu builds the positioning menu: paired nudge buttons for the selected overlay (an
// alternative to the thumbsticks) + the always-live edit controls. Steps are coarse taps; the sticks
// give fine/continuous control.
func (e *editor) buildPosMenu(feat config.VROverlayFeature) []MenuItem {
	var items []MenuItem
	hdr := func(l string) { items = append(items, MenuItem{Kind: MIHeader, Label: l}) }
	act := func(l, val string, fn func()) {
		items = append(items, MenuItem{Kind: MIAction, Label: l, Value: val, OnClick: fn})
	}
	const step, rstep, zstep = 0.05, 5.0, 0.1 // metres / degrees / metres per tap

	if sel := findOvByID(feat, e.selected); sel == nil {
		hdr("EDIT MODE")
		hdr("point at an overlay +")
		hdr("pull trigger to select")
	} else {
		hdr(sel.ID + " (" + sel.Type + ")")
		act("Move left", fmt.Sprintf("X %+.2fm", sel.X), func() { e.nudgeSel(func(t *config.VROverlay) { t.X -= step }) })
		act("Move right", "", func() { e.nudgeSel(func(t *config.VROverlay) { t.X += step }) })
		act("Move down", fmt.Sprintf("Y %+.2fm", sel.Y), func() { e.nudgeSel(func(t *config.VROverlay) { t.Y -= step }) })
		act("Move up", "", func() { e.nudgeSel(func(t *config.VROverlay) { t.Y += step }) })
		act("Move closer", fmt.Sprintf("Z %+.2fm", sel.Z), func() { e.nudgeSel(func(t *config.VROverlay) { t.Z = clampF64(t.Z+zstep, -6, 1) }) })
		act("Move farther", "", func() { e.nudgeSel(func(t *config.VROverlay) { t.Z = clampF64(t.Z-zstep, -6, 1) }) })
		act("Rotate left", fmt.Sprintf("yaw %.0f°", sel.Yaw), func() { e.nudgeSel(func(t *config.VROverlay) { t.Yaw -= rstep }) })
		act("Rotate right", "", func() { e.nudgeSel(func(t *config.VROverlay) { t.Yaw += rstep }) })
		act("Tilt back", fmt.Sprintf("pitch %.0f°", sel.Pitch), func() { e.nudgeSel(func(t *config.VROverlay) { t.Pitch = clampF64(t.Pitch+rstep, -90, 90) }) })
		act("Tilt forward", "", func() { e.nudgeSel(func(t *config.VROverlay) { t.Pitch = clampF64(t.Pitch-rstep, -90, 90) }) })
		act("Reset position", "", e.resetSelOverlay)
	}
	hdr("(thumbsticks also move it)")
	grabVal := "grip-grab ON"
	if feat.StickMoveOnly {
		grabVal = "grip-grab OFF"
	}
	act("Free-hand grab", grabVal, func() {
		e.m.mutate(func(ff *config.VROverlayFeature) { ff.StickMoveOnly = !ff.StickMoveOnly })
	})
	mhLabel := "Hide main menu"
	if e.menuHidden {
		mhLabel = "Show main menu"
	}
	act(mhLabel, "", func() { e.menuHidden = !e.menuHidden; e.gotoPage("") })
	act("Exit edit mode", "", func() { e.setEditMode(false) })
	return items
}

// nudgeSel mutates the selected overlay + invalidates every menu cache so the sliders/values refresh.
func (e *editor) nudgeSel(fn func(*config.VROverlay)) {
	e.adjust(fn)
	e.menuBuiltAt[menuKey] = time.Time{}
	e.menuBuiltAt[posKey] = time.Time{}
	e.menuBuiltAt[dashKey] = time.Time{}
}

// tickTooltip shows the full text of the laser-hovered menu row when its label was truncated to fit,
// in a small panel beside the menu. Hidden when not hovering a truncated row (or the laser left).
func (e *editor) tickTooltip(feat config.VROverlayFeature, hand Hand) {
	items := e.shownMenu(menuKey).items // hoverRow is snapshot-derived → look it up in the same list
	row := e.hoverRow
	var full string
	show := false
	if time.Since(e.hoverRowAt) < 2500*time.Millisecond && row >= 0 && row < len(items) {
		if t, over := e.menuRowOverflow(items[row]); over {
			full, show = t, true
		}
	}
	if !show {
		if e.tipShow.changed(false) {
			_ = e.m.rt.Show(tipKey, false)
		}
		return
	}
	if !e.tipEnsured {
		_ = e.m.rt.EnsureOverlay(tipKey, "rave-mate tooltip")
		e.tipEnsured = true
	}
	if e.tipSig != full {
		_ = e.m.rt.SetTexture(tipKey, e.m.rend.RenderTooltip(full))
		e.tipSig = full
	}
	base := e.menuTransform(feat, hand) // beside the menu, nudged forward so it reads over the row
	base.X += base.WidthM*0.5 + 0.18
	base.Z -= 0.02
	base.WidthM = 0.34
	base.Opacity = 0.98
	if e.tipTf.needsApply(base, e.snapTracked(base)) {
		_ = e.m.rt.SetTransform(tipKey, base)
	}
	if e.tipShow.changed(true) {
		_ = e.m.rt.Show(tipKey, true)
	}
}

// menuRowOverflow returns a row's full display text + whether RenderMenu truncated its label (so the
// tooltip only appears when there's actually hidden text). Mirrors RenderMenu's per-kind label widths.
func (e *editor) menuRowOverflow(it MenuItem) (string, bool) {
	r := e.m.rend
	switch it.Kind {
	case MIHeader:
		return it.Label, textWidth(r.name, it.Label) > MenuW-32
	case MIAction:
		vw := 0
		if it.Value != "" {
			vw = textWidth(r.body, truncText(r.body, it.Value, MenuW/2))
		}
		full := it.Label
		if it.Value != "" {
			full += "  (" + it.Value + ")"
		}
		return full, textWidth(r.body, it.Label) > MenuW-44-vw
	case MISlider:
		vw := textWidth(r.body, truncText(r.body, it.Value, MenuW/2))
		return it.Label, textWidth(r.body, it.Label) > MenuW-44-vw
	}
	return it.Label, false
}

// wristTransform places the edit badge on the wrist/hand per the WristPos preset (controller frame:
// −Z forward along the controller, +Y up, +X right). "out" mirrors X for the left hand so it always
// sits on the OUTSIDE (pinky side). Default "inner" = watch-style like XSOverlay's wrist button.
func (e *editor) wristTransform(feat config.VROverlayFeature, hand Hand) Transform {
	w := 0.065 // small (user: old 0.10 badge was oversized + easy to hit by accident)
	if feat.WristLarge {
		w = 0.10
	}
	tf := Transform{Snap: hand, WidthM: w, Opacity: 0.95}
	switch feat.ResolvedWristPos() {
	case "inner": // underside of the wrist, face toward the eyes when you turn it (watch check)
		tf.Y, tf.Z, tf.Pitch = -0.045, 0.02, 125
	case "back":
		tf.Y, tf.Z, tf.Pitch = 0.02, -0.07, -75
	case "above":
		tf.Y, tf.Z, tf.Pitch = 0.09, -0.02, -35
	case "out":
		x, roll := 0.055, -60.0
		if hand == HandLeft {
			x, roll = -x, -roll
		}
		tf.X, tf.Y, tf.Z, tf.Pitch, tf.Roll = x, 0.02, -0.03, -55, roll
	default: // "top" - the proven-visible spot (an "inner" default shipped facing away → invisible badge)
		tf.Y, tf.Z, tf.Pitch = 0.04, -0.02, -55
	}
	return tf
}

// wristGating reports whether the wrist indicator should show (HMD gaze toward the edit hand). Fails
// open when poses are unavailable.
func (e *editor) wristGating(hand Hand) bool {
	hi, hok := e.ed.ControllerIndex(hand)
	hmd, mok := e.ed.DevicePose(0)
	wp, wok := e.ed.DevicePose(hi)
	if !hok || !mok || !wok {
		return true
	}
	wx, wy, wz := MatPos(wp)
	hx, hy, hz := MatPos(hmd)
	fx, fy, fz := MatForward(hmd)
	dx, dy, dz := wx-hx, wy-hy, wz-hz
	dl := math.Sqrt(dx*dx + dy*dy + dz*dz)
	return dl > 1e-3 && (fx*dx+fy*dy+fz*dz)/dl > 0.66 // ~48° cone toward the hand
}

// SummonButtonLabel is the friendly name for a summon-button choice ("ax"|"by"|"custom").
func SummonButtonLabel(btn string) string {
	switch btn {
	case "by":
		return "B / Y button"
	case "custom":
		return "Custom (bind in SteamVR)"
	default:
		return "A / X button"
	}
}

// helpLines is the in-VR controls/keybind guide (ASCII - Orbitron has no emoji glyphs).
func helpLines(feat config.VROverlayFeature) []Line {
	open := "OPEN: hold the " + SummonButtonLabel(feat.ResolvedSummonButton()) + ", or the SteamVR dashboard tab"
	if !feat.SummonOn {
		open = "OPEN: the SteamVR dashboard rave-mate tab (summon button is off)"
	}
	lines := []Line{
		{Text: "RAVE-MATE OVERLAY CONTROLS", Color: colName},
		{Text: open, Color: colText},
		{Text: "SELECT: point at an overlay + pull the TRIGGER (it outlines pink).", Color: colMint},
		{Text: "MOVE: with one selected, HOLD GRIP to carry it (mint), drop to release.", Color: colText},
		{Text: "PUSH/PULL: stick up/down moves a grabbed panel near/far", Color: colText},
		{Text: "These work out of the box; rebind any input in SteamVR bindings", Color: colMuted},
		{Text: "  or via 'Open rave-mate controller bindings' in the menu.", Color: colMuted},
	}
	return append(lines,
		Line{Text: "SLIDERS: aim + click along the bar to set the value", Color: colText},
		Line{Text: "WALK: the thumbstick still moves you in-game while editing", Color: colMint},
		Line{Text: "MENU PLACEMENT: drag the Menu sliders -> ghost shows the new", Color: colText},
		Line{Text: "  spot -> press Apply to commit; Reset transforms to undo", Color: colText},
	)
}

// handleActions drives the editor from SteamVR Input actions (rebindable in SteamVR's binding UI):
// open/close, show/hide overlays, and hold-to-grab the laser-hovered surface. No-op when the action
// manifest didn't load (then the wrist badge + menu actions are the controls).
func (e *editor) handleActions(feat config.VROverlayFeature, hand Hand) {
	if !e.ed.InputReady() {
		return
	}
	e.ed.InputUpdate()
	if edges := e.ed.ActSlotEdges(); edges != 0 {
		e.m.fireSlots(edges) // user-mapped controller slots → app actions (OBS, overlays, …)
	}
	if e.ed.ActToggleEditorEdge() || e.m.takeEditToggle() {
		e.evt("toggle_editor action fired")
		e.toggle()
	}
	if e.ed.ActToggleOverlaysEdge() {
		e.evt("toggle_overlays action fired → hidden=%v", !e.m.contentHidden)
		e.m.contentHidden = !e.m.contentHidden
	}
	e.handleSummon(feat)  // summon action (open/tap-hide) - 90 Hz so quick taps register
	e.updatePointer(feat) // ray cursor + hover + trigger-activate; BEFORE the open-gate so the wrist opens it
	if !e.on {
		return
	}
	held := e.ed.ActGrabHeld()
	target := e.grabTargetKey(feat) // what a grip would grab: the SELECTED overlay (or the menu if pointed at)
	if held && !e.grabHeldPrev {    // log the down edge once (why grab does/doesn't start)
		e.evt("grip DOWN target=%q selected=%q", target, e.selected)
	}
	e.grabHeldPrev = held
	// Grab is LOCKED to the deliberately-selected overlay (point + trigger to select first), never
	// whatever the pointer grazes - a resting Index grip can no longer yank an unintended overlay onto
	// the hand. A deliberate HOLD (grabHoldDelay) still gates it; the selected outline goes AMBER while
	// arming and MINT while grabbing (visual indicator, driveEditOutlines); a haptic fires on engage.
	// StickMoveOnly (opt-in) disables grip-grab entirely. Pointing AT the menu grabs the menu instead
	// (menu-move), unchanged.
	canArm := e.editMode && !feat.StickMoveOnly && e.grab == nil && target != ""
	switch {
	case held && canArm:
		if e.grabArmKey != target {
			e.grabArmKey, e.grabArmAt = target, time.Now()
			e.evt("grab ARM %q (hold %dms)", target, grabHoldDelay.Milliseconds())
		} else if time.Since(e.grabArmAt) >= grabHoldDelay {
			e.startGrabTarget(feat, hand, target)
			if e.grab != nil {
				gh := e.grabHand()
				e.ed.Haptic(gh, 0.06, 160, 0.7) // engage buzz
				e.evt("grab ENGAGE %q hand=%s", e.grab.key, handName(gh))
			}
			e.grabArmKey = ""
		}
	case !held:
		if e.grabArmKey != "" {
			e.evt("grab arm released early (no grab)")
			e.grabArmKey = ""
		}
		if e.grab != nil && e.grabViaAction { // release always drops (even if editMode was toggled off mid-carry)
			e.evt("grip UP → drop")
			e.ed.Haptic(e.grabHand(), 0.04, 150, 0.5) // drop tick
			e.endGrab()
			e.grabViaAction = false
		}
	}

	// Edit-mode thumbstick nudge of the selected overlay (skipped while grabbing - grab owns push_pull).
	if e.editMode && e.selected != "" && e.grab == nil {
		e.thumbNudge()
	}
}

// thumbNudge moves/rotates/tilts the SELECTED overlay from the thumbsticks (edit mode, not grabbing):
// right stick = X/Y position; left stick Y = depth (closer/further, Z), left stick X = yaw (rotate);
// hold the LEFT trigger + left stick Y = pitch (tilt back/forward). Reuses the already-bound push_pull
// analog, so it needs no new SteamVR bindings. Rates are conservative - tune in-headset.
func (e *editor) thumbNudge() {
	now := time.Now()
	dt := now.Sub(e.thumbAt).Seconds()
	e.thumbAt = now
	if dt <= 0 || dt > 0.2 {
		dt = 1.0 / 90
	}
	rx, ry := deadzone2(e.ed.ThumbVec(HandRight))
	lx, ly := deadzone2(e.ed.ThumbVec(HandLeft))
	if rx == 0 && ry == 0 && lx == 0 && ly == 0 {
		return
	}
	pos := 0.5 * dt // metres/s at full deflection
	rot := 60 * dt  // degrees/s
	lHeld, _ := e.ed.PointerClickState(HandLeft)
	e.adjust(func(t *config.VROverlay) {
		t.X += float64(rx) * pos
		t.Y += float64(ry) * pos
		if lHeld { // left trigger held → tilt
			t.Pitch = clampF64(t.Pitch+float64(ly)*rot, -90, 90)
		} else {
			t.Z = clampF64(t.Z-float64(ly)*pos, -6, 1) // stick up = push further (−Z is forward)
			t.Yaw += float64(lx) * rot                 // rotate (yaw wraps freely)
		}
	})
	e.menuBuiltAt[menuKey] = time.Time{} // reflect the new transform in the SELECTED OVERLAY sliders
	e.menuBuiltAt[posKey] = time.Time{}
	e.menuBuiltAt[dashKey] = time.Time{}
}

// deadzone2 zeroes a thumbstick axis within ±0.15 so a resting stick doesn't creep the overlay.
func deadzone2(x, y float32) (float32, float32) {
	const dz = 0.15
	if x > -dz && x < dz {
		x = 0
	}
	if y > -dz && y < dz {
		y = 0
	}
	return x, y
}

func clampF64(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// grabHand maps the grabbing controller index (the carried grab's device, else the current laser hover)
// to a Hand, so the rumble pulse buzzes the hand actually doing the grab.
func (e *editor) grabHand() Hand {
	idx := e.hoverIdx
	if e.grab != nil {
		idx = e.grab.idx
	}
	if li, ok := e.ed.ControllerIndex(HandLeft); ok && li == idx {
		return HandLeft
	}
	if ri, ok := e.ed.ControllerIndex(HandRight); ok && ri == idx {
		return HandRight
	}
	return e.activeHand
}

// grabTargetKey resolves what a grip-grab moves in edit mode: the menu when the pointer is on it
// (menu-move, unchanged), else the SELECTED content overlay. Locking content grab to the SELECTION -
// not the pointer-hovered surface - means a grip only ever moves what you deliberately selected
// (point + trigger), so a stray squeeze can't yank a random overlay. "" when nothing is grabbable.
func (e *editor) grabTargetKey(feat config.VROverlayFeature) string {
	if e.hoverKey == menuKey && time.Since(e.hoverAt) < 500*time.Millisecond {
		return menuKey
	}
	if e.selected != "" {
		if o := findOvByID(feat, e.selected); o != nil && o.Enabled {
			return contentKey(e.selected)
		}
	}
	return ""
}

// grabIdx is the controller a grab attaches to: the active pointer hand (the hand you aim with), with
// the last laser-hover device as a fallback.
func (e *editor) grabIdx() int {
	if idx, ok := e.ed.ControllerIndex(e.activeHand); ok {
		return idx
	}
	return e.hoverIdx
}

// startGrabTarget begins an action-grab of a resolved target (the menu or the selected content
// overlay), capturing its world pose so it rigidly follows the grabbing hand until the grip releases.
func (e *editor) startGrabTarget(feat config.VROverlayFeature, hand Hand, target string) {
	idx := e.grabIdx()
	if target == menuKey {
		t := e.menuTransform(feat, hand)
		e.startGrab(menuKey, handToSnap(t.Snap), idx, e.transformWorld(t))
	} else {
		id := strings.TrimPrefix(target, "page.rave.mate.")
		o := findOvByID(feat, id)
		if o == nil {
			return
		}
		e.selected = id
		e.startGrab(target, o.SnapTo, idx, e.worldMatrixOf(*o))
	}
	e.grabViaAction = e.grab != nil
}

// noteHover records the surface the laser is on (for action-grab targeting).
func (e *editor) noteHover(key string, device int) {
	e.hoverKey, e.hoverIdx, e.hoverAt = key, device, time.Now()
}

// handleSummon reads the SUMMON action (bound to a face button in the manifest; rebindable in
// SteamVR) when SummonOn: a long hold opens/closes the editor; a short tap toggles overlay
// visibility if SummonTapHides. Uses IVRInput - legacy GetControllerState returns nothing for face
// buttons on Index/Touch, which is why the old button-mask poll never fired. Called at 90 Hz from
// handleActions so quick taps aren't missed.
func (e *editor) handleSummon(feat config.VROverlayFeature) {
	if !feat.SummonOn {
		e.btnPressed = false
		return
	}
	held := e.ed.ActSummonHeld()
	switch {
	case held && !e.btnPressed:
		e.btnPressed, e.btnDownAt, e.btnLongFired = true, time.Now(), false
		e.evt("summon DOWN")
	case held && e.btnPressed && !e.btnLongFired && time.Since(e.btnDownAt) >= longPress:
		e.btnLongFired = true
		e.evt("summon HOLD → toggle editor (now on=%v)", !e.on)
		e.toggle() // long hold → open/close editor
	case !held && e.btnPressed:
		if !e.btnLongFired && feat.SummonTapHides {
			e.m.contentHidden = !e.m.contentHidden // short tap → overlay visibility
			e.evt("summon TAP → overlays hidden=%v", e.m.contentHidden)
		} else if !e.btnLongFired {
			e.evt("summon TAP (SummonTapHides off → no-op)")
		}
		e.btnPressed = false
	}
}

func clampF32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// menuRebuild throttles menu re-layout: items (with live values) are rebuilt at most this often, not
// every 100ms tick - cuts allocations + obs-instance locks while keeping clicks responsive (events
// are polled every tick against the cached items, and a click forces an immediate rebuild).
const menuRebuild = 250 * time.Millisecond

// driveMenu renders the menu into an overlay + dispatches laser clicks (actions fire; sliders set a
// value from the click's X). Used by both the floating menu and the SteamVR dashboard tab. Items are
// rebuilt at most every menuRebuild; the texture re-uploads only when content changes (no flicker).
func (e *editor) driveMenu(key, title string, feat config.VROverlayFeature, hand Hand, draggable bool) {
	now := time.Now()
	items, ok := e.menuItems[key]
	if !ok || now.Sub(e.menuBuiltAt[key]) >= menuRebuild {
		items = e.buildMenu(feat)
		e.menuItems[key] = items
		e.menuBuiltAt[key] = now
	}
	// Texture re-uploads on CONTENT change only (cheap signature compare per tick). The hovered-row
	// highlight is a separate tiny overlay (driveHover) - hover moves no longer re-upload this whole
	// texture (that was a full re-render + ~2.5MB GPU push per row change = visible flicker + load).
	// Sig + shown snapshot commit ONLY on upload success - a failed upload retries next tick while
	// clicks keep mapping to the texture still on the compositor.
	sig := fmt.Sprintf("%s\x00%s", title, menuSignature(items))
	if e.menuSig[key] != sig {
		e.uploadMenu(key, title, items, feat.ResolvedMenuBg(), sig)
	}
	if draggable {
		// Floating menu → the custom ray pointer (pointer.go) drives hover + clicks + slider drags, so
		// the controller is NEVER captured from the game. Release any SteamVR laser interactivity a prior
		// build left on; the ray's row math uses the same shown snapshot (pointer.go).
		e.setMenuLaser(false)
		return
	}
	// Row mapping below uses the SHOWN snapshot - the list the displayed texture was rendered from -
	// so a rebuilt-but-not-yet-uploaded list can never shift which row a click lands on.
	shown := e.shownMenu(key)
	mh := MenuRowH * (shown.rows + 1)
	// The laser hit-area (mouse scale) MUST track the texture height: the menu grows/shrinks as
	// sections collapse/expand, and a stale scale maps clicks to the wrong rows. Re-set on change.
	if !e.menuInter[key] || e.menuMh[key] != mh {
		e.ed.SetInteractive(key, MenuW, mh, true)
		e.menuInter[key], e.menuMh[key] = true, mh
	}
	if e.isGrabbing(key) {
		return // the grab block owns events while the menu is being dragged (next click drops it)
	}
	for _, ev := range e.ed.PollEvents(key) {
		if key == menuKey {
			e.noteHover(key, ev.Device) // for action-grab targeting (grip moves the menu)
			// Track the hovered row (0 = title) so a truncated row's full text can tooltip.
			e.hoverRow, e.hoverRowAt = menuRowAt(float32(mh)-ev.Y, mh), time.Now()
		}
		if ev.Type != EvMouseDown {
			continue
		}
		row := menuRowAt(float32(mh)-ev.Y, mh) // flip Y (laser origin bottom-left); -1 = title bar
		if row < 0 {
			if draggable && !e.ed.InputReady() { // click the title bar to drag (fallback; else grip-grab)
				t := e.menuTransform(feat, hand)
				e.startGrab(menuKey, handToSnap(t.Snap), ev.Device, e.transformWorld(t))
			}
			continue
		}
		if e.menuActionAt(shown.items, row, float64(ev.X)/float64(MenuW)) {
			e.menuBuiltAt[key] = time.Time{} // a click changed state → rebuild on the next tick
		}
	}
}

// menuRowAt maps a top-left-origin pixel Y in [0,mh) to an item index (-1 = title bar / above). Shared
// by the laser-event path (which flips its bottom-left Y first) + the ray pointer, so both agree.
func menuRowAt(topY float32, mh int) int {
	if topY < 0 || int(topY) >= mh {
		return -1
	}
	return int(topY)/MenuRowH - 1
}

// menuActionAt fires item row's action/slider (xFrac 0..1 sets a slider from the hit X). Returns true
// if it changed state (caller invalidates the menu cache). The single row→action map both the laser
// EvMouseDown path and the ray pointer route through.
func (e *editor) menuActionAt(items []MenuItem, row int, xFrac float64) bool {
	if row < 0 || row >= len(items) {
		return false
	}
	switch it := items[row]; it.Kind {
	case MIAction:
		if it.OnClick != nil {
			e.evt("menu CLICK row %d %q", row, it.Label)
			it.OnClick()
			return true
		}
	case MISlider:
		if it.OnSet != nil {
			e.evt("menu SLIDER row %d %q → %.2f", row, it.Label, clampFrac(xFrac))
			it.OnSet(clampFrac(xFrac))
			return true
		}
	}
	return false
}

// menuTransform is the menu's placement: auto (floats above the edit hand, tilted toward you) until
// the user drags it, then the stored snap+offset.
func (e *editor) menuTransform(feat config.VROverlayFeature, hand Hand) Transform {
	if feat.MenuSnap == "" {
		return Transform{Snap: hand, Y: 0.12, Z: -0.05, Pitch: -45, WidthM: 0.36, Opacity: 0.97}
	}
	w := feat.MenuWidth
	if w <= 0 {
		w = 0.36
	}
	return Transform{Snap: HandFromString(feat.MenuSnap), X: feat.MenuX, Y: feat.MenuY, Z: feat.MenuZ, Yaw: feat.MenuYaw, Pitch: feat.MenuPitch, WidthM: w, Opacity: 0.97}
}

// transformWorld resolves a Transform (snap + local offset) to a world matrix.
func (e *editor) transformWorld(t Transform) Mat34 {
	local := EulerToMat(t.Yaw, t.Pitch, t.Roll, t.X, t.Y, t.Z)
	if idx, ok := e.anchorIdx(handToSnap(t.Snap)); ok {
		if dp, ok2 := e.ed.DevicePose(idx); ok2 {
			return MulMat(dp, local)
		}
	}
	return local
}

// nearestHandSnap returns "left"/"right" for the tracked hand within ~0.45 m of pos, else "world".
func (e *editor) nearestHandSnap(pos [3]float32) string {
	best, bestD := "world", float32(0.45)
	for _, h := range [2]Hand{HandLeft, HandRight} {
		idx, ok := e.ed.ControllerIndex(h)
		if !ok {
			continue
		}
		dp, ok := e.ed.DevicePose(idx)
		if !ok {
			continue
		}
		hx, hy, hz := MatPos(dp)
		dx, dy, dz := float32(hx)-pos[0], float32(hy)-pos[1], float32(hz)-pos[2]
		if d := float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz))); d < bestD {
			best, bestD = handToSnap(h), d
		}
	}
	return best
}

func handToSnap(h Hand) string {
	switch h {
	case HandLeft:
		return "left"
	case HandRight:
		return "right"
	case HandHead:
		return "head"
	}
	return ""
}

// startGrab begins a drag of key (content or menu) by the given controller, capturing the rigid
// offset from the controller to the overlay's current world pose.
func (e *editor) startGrab(key, snap string, idx int, oworld Mat34) {
	pose, ok := e.ed.DevicePose(idx)
	if !ok { // firing device pose unavailable - fall back to a tracked controller
		if ci, ok2 := e.ed.ControllerIndex(HandRight); ok2 {
			idx, pose, ok = ci, mustPose(e.ed, ci), true
		} else if ci, ok2 := e.ed.ControllerIndex(HandLeft); ok2 {
			idx, pose, ok = ci, mustPose(e.ed, ci), true
		}
	}
	e.m.log.Info(logTag, "grab start", map[string]any{"key": key, "device": idx, "ok": ok})
	e.evt("grab START key=%s device=%d ok=%v", key, idx, ok)
	if ok {
		offset := MulMat(InvMat(pose), oworld)
		e.grab = &grab{key: key, idx: idx, offset: offset, last: oworld, snap: snap, startAt: time.Now()}
		e.ed.SetTransformMatrixDevice(key, idx, offset) // parent to the hand → SteamVR tracks it at full fps
	}
}

func mustPose(ed Editor, idx int) Mat34 { m, _ := ed.DevicePose(idx); return m }

// endGrab stores the dropped pose: content → overlay offset (in its snap frame); menu → menu offset.
func (e *editor) endGrab() {
	g := e.grab
	e.grab = nil
	if g == nil {
		return
	}
	// SteamVR was tracking the surface to the hand; resolve where it actually ended up (live device
	// pose × the carried offset) so the stored drop position matches what the user sees.
	if dp, ok := e.ed.DevicePose(g.idx); ok {
		g.last = MulMat(dp, g.offset)
	}
	local := g.last
	if idx, ok := e.anchorIdx(g.snap); ok {
		if dp, ok2 := e.ed.DevicePose(idx); ok2 {
			local = MulMat(InvMat(dp), g.last)
		}
	}
	yaw, pitch, roll, x, y, z := MatToEuler(local)
	if g.key == menuKey {
		// Re-anchor to the hand the menu was dropped NEAREST (so grabbing it toward the other hand moves
		// it there - and the pointer's menu-hand exclusion follows it). World if far from both hands.
		hx, hy, hz := MatPos(g.last)
		snap := e.nearestHandSnap([3]float32{float32(hx), float32(hy), float32(hz)})
		mlocal := g.last
		if idx, ok := e.anchorIdx(snap); ok {
			if dp, ok2 := e.ed.DevicePose(idx); ok2 {
				mlocal = MulMat(InvMat(dp), g.last)
			}
		}
		myaw, _, _, mx, my, mz := MatToEuler(mlocal)
		e.m.mutate(func(f *config.VROverlayFeature) {
			f.MenuSnap, f.MenuX, f.MenuY, f.MenuZ, f.MenuYaw = snap, mx, my, mz, myaw
		})
		return
	}
	id := strings.TrimPrefix(g.key, "page.rave.mate.")
	e.m.mutate(func(f *config.VROverlayFeature) {
		if t := findOv(f, id); t != nil {
			t.X, t.Y, t.Z = x, y, z
			t.Yaw, t.Pitch, t.Roll = yaw, pitch, roll
		}
	})
}

// menuSignature is a cheap change key (labels + values + slider fills) so the texture re-uploads
// only when something visible changed.
func menuSignature(items []MenuItem) string {
	var b strings.Builder
	for _, it := range items {
		fmt.Fprintf(&b, "\x00%d|%s|%s|%.3f", it.Kind, it.Label, it.Value, it.Frac)
	}
	return b.String()
}

// buildMenu builds the structured menu for the current selection (headers / actions / sliders) in
// priority order: add/delete, move(drag)/resize, opacity/snap, count/duration.
func (e *editor) buildMenu(feat config.VROverlayFeature) []MenuItem {
	var items []MenuItem
	hdr := func(l string) { items = append(items, MenuItem{Kind: MIHeader, Label: l}) }
	act := func(l, val string, fn func()) {
		items = append(items, MenuItem{Kind: MIAction, Label: l, Value: val, OnClick: fn})
	}
	sld := func(l, val string, frac float64, set func(float64)) {
		items = append(items, MenuItem{Kind: MISlider, Label: l, Value: val, Frac: frac, OnSet: set})
	}
	onoff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}

	// Drill-down navigation: the home page shows the top actions + the overlay list + one entry per
	// sub-menu; a sub-menu opens as its own short page with a "Back" row. Keeps the menu navigable
	// instead of one giant scroll (the whole thing was hilariously long).
	home := e.page == ""
	page := func(name string, avail bool, body func()) {
		if !avail {
			return
		}
		switch {
		case e.page == name:
			body()
		case home:
			act(name, ">", func() { e.gotoPage(name) })
		}
	}

	if !home {
		act("< Back", "", func() { e.gotoPage("") })
	}

	sel := findOvByID(feat, e.selected)

	if home {
		editVal := "open"
		if e.on {
			editVal = "close"
		}
		act("In-world editor", editVal, e.toggle)
		act("Edit mode", onoff(e.editMode), e.toggleEditMode) // plain on/off toggle
		if e.editMode {                                       // edit-mode-only: hide the main menu but keep editing + the positioning menu
			mhVal := "hide"
			if e.menuHidden {
				mhVal = "show"
			}
			act("Main menu", mhVal, func() { e.menuHidden = !e.menuHidden; e.gotoPage("") })
		}
		helpVal := "show"
		if e.helpOn {
			helpVal = "hide"
		}
		act("Help / controls guide", helpVal, func() { e.helpOn = !e.helpOn })
		visVal := "visible"
		if e.m.contentHidden {
			visVal = "HIDDEN"
		}
		act("All overlays", visVal, func() { e.m.contentHidden = !e.m.contentHidden })
		if s := e.m.suggest; s != nil { // notify-mode world-layout hint (worldlayout.go)
			act("Apply layout: "+s.layout, "this world", e.m.applySuggest)
		}

		hdr("- menus -")
	}

	// Overlay select is its own page (the wrist strip's OV button jumps here) - home stays short.
	page(pageOverlays, true, func() {
		hdr("OVERLAYS (tap to edit)")
		for i := range feat.Overlays {
			o := feat.Overlays[i]
			id := o.ID
			mark := ""
			if id == e.selected {
				mark = "● "
			}
			lock := ""
			if o.AlwaysShow {
				lock = "LOCKED"
			}
			// Tap selects AND jumps straight to its editor - no Back-then-open drill-down.
			act(mark+o.ID+" ("+o.Type+")", lock, func() { e.selected = id; e.gotoPage(pageSelected) })
		}
		if len(feat.Overlays) == 0 {
			hdr("(no overlays - add one)")
		}
	})

	page(pageAdd, true, func() {
		act("Add chat overlay", "", func() { e.addOverlay("chat") })
		act("Add alerts overlay", "", func() { e.addOverlay("alerts") })
		act("Add OBS stats overlay", "", func() { e.addOverlay("obs") })
		act("Add viewer count overlay", "", func() { e.addOverlay("viewers") })
		act("Add viewer list overlay", "", func() { e.addOverlay("viewerlist") })
		hdr("- live stats -")
		act("Add performance overlay", "CPU/RAM/VR", func() { e.addOverlay(typePerf) })
		act("Add network overlay", "rates", func() { e.addOverlay(typeNetwork) })
		act("Add timing overlay", "peer RTT", func() { e.addOverlay(typeTiming) })
	})

	// Camera paths: tapping one loads it into VRChat over OSC (the real camera flies) AND opens a 3D
	// orbit preview panel beside the menu (marker flies the path at its real speed; play/pause below).
	page(pageCamPaths, e.m.camPaths != nil, func() {
		act("3D preview panel", onoff(e.worldPathOn), func() { e.worldPathOn = !e.worldPathOn })
		if e.worldPathOn {
			act("Play / pause preview", onoff(e.worldPathPlaying), func() { e.worldPathPlaying = !e.worldPathPlaying })
			act("Restart preview", "", func() { e.worldPathT = 0 })
			act("Reset view", "", func() { e.worldPathYaw, e.worldPathPitch, e.worldPathZoom, e.worldPathManual = 0, 0.38, 1, false })
			hdr("drag panel = orbit · scroll = zoom")
		}
		items := e.m.camPaths()
		if len(items) == 0 {
			hdr("(no camera paths)")
			return
		}
		for _, it := range items {
			file := it.File
			act(it.Label, "load + preview", func() {
				if e.m.loadCamPath != nil {
					_ = e.m.loadCamPath(file) // /dolly/Import + /dolly/Play → plays in VRChat
				}
				if e.m.camPathGeom != nil {
					e.worldPathGeom = e.m.camPathGeom(file)
					e.worldPathOn, e.worldPathPlaying, e.worldPathT, e.worldPathManual = true, true, 0, false
				}
			})
		}
	})

	page(pageSelected, sel != nil, func() {
		hdr(sel.ID + " (" + sel.Type + ")")
		act("Anchor", snapName(sel.SnapTo), func() {
			e.adjust(func(t *config.VROverlay) {
				t.SnapTo = nextSnap(t.SnapTo)
				t.X, t.Y, t.Z = defaultOffset(t.SnapTo)
				t.Yaw, t.Pitch, t.Roll = 0, 0, 0
			})
		})
		act("Lock (always visible)", onoff(sel.AlwaysShow), func() {
			e.adjust(func(t *config.VROverlay) { t.AlwaysShow = !t.AlwaysShow })
		})
		if sel.Type == "chat" || sel.Type == "alerts" {
			act("Placeholder when empty", onoff(!sel.HidePlaceholder), func() {
				e.adjust(func(t *config.VROverlay) { t.HidePlaceholder = !t.HidePlaceholder })
			})
		}
		sld("Move X", fmt.Sprintf("%+.2fm", sel.X), (sel.X+2)/4, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.X = -2 + f*4 })
		})
		sld("Move Y", fmt.Sprintf("%+.2fm", sel.Y), (sel.Y+2)/4, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.Y = -2 + f*4 })
		})
		sld("Depth Z", fmt.Sprintf("%+.2fm", sel.Z), (sel.Z+3)/4, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.Z = -3 + f*4 }) // −3 (far) … +1 (behind)
		})
		sld("Rotate (yaw)", fmt.Sprintf("%.0f°", sel.Yaw), (sel.Yaw+180)/360, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.Yaw = -180 + f*360 })
		})
		sld("Tilt (pitch)", fmt.Sprintf("%.0f°", sel.Pitch), (sel.Pitch+90)/180, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.Pitch = -90 + f*180 })
		})
		op := sel.ResolvedOpacity()
		sld("Opacity", fmt.Sprintf("%.0f%%", op*100), (op-0.1)/0.9, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.Opacity = 0.1 + f*0.9 })
		})
		bg := sel.ResolvedBgOpacity()
		sld("Background", fmt.Sprintf("%.0f%%", bg*100), bg, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.BgOpacity = clampF(f, 0.02, 1) })
		})
		w := sel.ResolvedWidthM()
		sld("Width", fmt.Sprintf("%.2f m", w), (w-0.1)/1.9, func(f float64) {
			e.adjust(func(t *config.VROverlay) { t.WidthM = clampWidth(0.1 + f*1.9) })
		})
		if !isStatsType(sel.Type) { // message count / fade are meaningless for live-stats panels
			n := sel.ResolvedMaxMessages()
			sld("Messages", fmt.Sprintf("%d", n), float64(n-1)/19, func(f float64) {
				e.adjust(func(t *config.VROverlay) { t.MaxMessages = 1 + int(f*19+0.5) })
			})
			fade := sel.DisplaySeconds
			fv := "off"
			if fade > 0 {
				fv = fmt.Sprintf("%.0fs", fade)
			}
			sld("Fade after", fv, fade/30, func(f float64) {
				e.adjust(func(t *config.VROverlay) { t.DisplaySeconds = f * 30 })
			})
		}
		act("Reset this overlay", "", e.resetSelOverlay)
		act("Delete overlay", "", e.deleteSel)
	})

	page(pageOBS, true, func() {
		insts := e.m.obsInstances()
		if len(insts) == 0 {
			hdr("(no OBS instance connected)")
		}
		for _, in := range insts {
			target := in.ID
			label := in.Label
			if label == "" {
				label = in.Node
			}
			if in.Local {
				label += " (this PC)"
			}
			streamVal := "start"
			if in.Streaming {
				streamVal = "LIVE - stop"
			}
			act(label+" · stream", streamVal, func() {
				e.m.SendObsCmd(obscontrol.Cmd{Target: target, Action: obscontrol.ActStreamToggle})
			})
			recVal := "start"
			if in.Recording {
				recVal = "REC - stop"
			}
			act(label+" · record", recVal, func() {
				e.m.SendObsCmd(obscontrol.Cmd{Target: target, Action: obscontrol.ActRecordToggle})
			})
		}
	})

	page(pageLayouts, true, func() {
		act("Save current as new", "", e.saveLayoutNew)
		for i := range feat.Layouts {
			name := feat.Layouts[i].Name
			act("Load: "+name, "", func() { e.loadLayout(name) })
		}
		// Per-world bindings (worldlayout.go): save/bind for the world you're in + auto-apply mode.
		hdr("- this world -")
		if wid, wname, wok := e.currentWorld(); !wok {
			hdr("(world unknown - VRChat not seen yet)")
		} else {
			display := wname
			if display == "" {
				display = wid
			}
			hdr(display)
			act("Save layout for this world", "", func() { e.saveLayoutForWorld(wid, wname) })
			if b, ok := findWorldBinding(feat, wid); ok {
				bVal := "enabled"
				if !b.Enabled {
					bVal = "disabled"
				}
				act("Bound: "+b.Layout, bVal, func() { e.toggleWorldBinding(wid) })
			}
		}
		act("Auto-apply on world join", feat.ResolvedWorldLayoutMode(), e.cycleWorldLayoutMode)
	})

	page(pageMenuPlace, true, func() {
		cur := e.menuTransform(feat, HandFromString(feat.ResolvedEditHand()))
		csnap := handToSnap(cur.Snap)
		get := func(pv, cv float64) float64 {
			if e.pend.active {
				return pv
			}
			return cv
		}
		ens := func() {
			if !e.pend.active {
				e.pend = pendMenu{active: true, snap: csnap, x: cur.X, y: cur.Y, z: cur.Z, yaw: cur.Yaw, pitch: cur.Pitch, width: cur.WidthM}
			}
		}
		mx, my, mz := get(e.pend.x, cur.X), get(e.pend.y, cur.Y), get(e.pend.z, cur.Z)
		myaw, mpitch, mw := get(e.pend.yaw, cur.Yaw), get(e.pend.pitch, cur.Pitch), get(e.pend.width, cur.WidthM)
		sld("Menu X", fmt.Sprintf("%+.2fm", mx), (mx+1)/2, func(f float64) { ens(); e.pend.x = -1 + f*2 })
		sld("Menu Y", fmt.Sprintf("%+.2fm", my), (my+1)/2, func(f float64) { ens(); e.pend.y = -1 + f*2 })
		sld("Menu Z", fmt.Sprintf("%+.2fm", mz), (mz+2)/2.5, func(f float64) { ens(); e.pend.z = -2 + f*2.5 })
		sld("Menu yaw", fmt.Sprintf("%.0f°", myaw), (myaw+180)/360, func(f float64) { ens(); e.pend.yaw = -180 + f*360 })
		sld("Menu tilt", fmt.Sprintf("%.0f°", mpitch), (mpitch+90)/180, func(f float64) { ens(); e.pend.pitch = -90 + f*180 })
		sld("Menu size", fmt.Sprintf("%.2fm", mw), (mw-0.15)/0.65, func(f float64) { ens(); e.pend.width = 0.15 + f*0.65 })
		mbg := feat.ResolvedMenuBg()
		sld("Menu background", fmt.Sprintf("%.0f%%", mbg*100), mbg, func(f float64) {
			e.m.mutate(func(ff *config.VROverlayFeature) { ff.MenuBg = clampF(f, 0.05, 1) })
		})
		if e.pend.active {
			act("Apply menu position", "", e.applyMenuPend)
		}
		act("Reset transforms", "", e.resetTransforms)
	})

	page(pageMotion, e.m.motion != nil, func() {
		mo := e.m.motion
		hdr("Status: " + mo.status())
		hdr("OSC -> " + feat.ResolvedOSCAddr())
		if mo.isRecording() {
			act("Stop + save recording", "REC", func() { mo.StopRecord() })
		} else {
			act("Record my movement", "", mo.StartRecord)
		}
		if mo.isPlaying() {
			act("Stop playback", "PLAYING", mo.Stop)
		}
		act("Loop playback", onoff(mo.looping()), mo.ToggleLoop)
		names := mo.list()
		if len(names) == 0 {
			hdr("(no saved takes - record one above)")
		}
		for _, n := range names {
			name := n
			act("Play: "+name, "", func() { mo.Play(name) })
		}
	})

	page(pageControls, true, func() {
		hdr("Open: hold the summon button, or the dashboard tab")
		hdr("Grip = grab/move · stick = push/pull (edit mode)")
		summonState := SummonButtonLabel(feat.ResolvedSummonButton())
		if !feat.SummonOn {
			summonState = "off"
		} else if !e.ed.InputReady() {
			summonState += " (restart VR to load)"
		}
		act("Open editor: hold", summonState, func() { // cycle A/X → B/Y → off
			e.m.mutate(func(ff *config.VROverlayFeature) {
				switch {
				case !ff.SummonOn:
					ff.SummonOn, ff.SummonButton = true, "ax"
				case ff.ResolvedSummonButton() == "ax":
					ff.SummonButton = "by"
				default:
					ff.SummonOn = false
				}
			})
		})
		tap := "off"
		if feat.SummonTapHides {
			tap = "on (tap = show/hide)"
		}
		act("Tap summon to show/hide", tap, func() {
			e.m.mutate(func(ff *config.VROverlayFeature) { ff.SummonTapHides = !ff.SummonTapHides })
		})
		grabVal := "on (grip to move)"
		if feat.StickMoveOnly {
			grabVal = "off (sticks/buttons only)"
		}
		act("Free-hand grab", grabVal, func() {
			e.m.mutate(func(ff *config.VROverlayFeature) { ff.StickMoveOnly = !ff.StickMoveOnly })
		})
		hdr("Rebind any input in SteamVR bindings, or:")
		act("Open rave-mate controller bindings", "open", func() { _ = e.m.OpenBindingUI() })
		act("Edit-button wrist", feat.ResolvedEditHand(), func() {
			e.m.mutate(func(ff *config.VROverlayFeature) {
				if ff.ResolvedEditHand() == "left" {
					ff.EditHand = "right"
				} else {
					ff.EditHand = "left"
				}
			})
		})
		act("Edit-button spot", feat.ResolvedWristPos(), func() { // cycle placement presets around the wrist/hand
			order := []string{"inner", "top", "back", "above", "out"}
			cur := slices.Index(order, feat.ResolvedWristPos())
			e.m.mutate(func(ff *config.VROverlayFeature) { ff.WristPos = order[(cur+1)%len(order)] })
		})
		sizeVal := "small"
		if feat.WristLarge {
			sizeVal = "large"
		}
		act("Edit-button size", sizeVal, func() {
			e.m.mutate(func(ff *config.VROverlayFeature) { ff.WristLarge = !ff.WristLarge })
		})
	})

	if home {
		hdr("")
		act("Close editor", editVal2(e.on), e.toggle)
	}
	return items
}

func editVal2(on bool) string {
	if on {
		return "close"
	}
	return "open"
}

// clampF clamps v to [lo,hi].
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// resetSelOverlay resets only the selected overlay's transform (position/rotation) to its snap default.
func (e *editor) resetSelOverlay() {
	id := e.selected
	e.m.mutate(func(f *config.VROverlayFeature) {
		if t := findOv(f, id); t != nil {
			t.X, t.Y, t.Z = defaultOffset(t.SnapTo)
			t.Yaw, t.Pitch, t.Roll = 0, 0, 0
		}
	})
}

// saveLayoutNew snapshots the current overlays + menu placement into a new auto-named layout.
func (e *editor) saveLayoutNew() {
	e.m.mutate(func(f *config.VROverlayFeature) {
		l := config.VRLayout{
			Name:     fmt.Sprintf("Layout %d", len(f.Layouts)+1),
			Overlays: append([]config.VROverlay(nil), f.Overlays...),
			MenuSnap: f.MenuSnap, MenuX: f.MenuX, MenuY: f.MenuY, MenuZ: f.MenuZ,
			MenuYaw: f.MenuYaw, MenuPitch: f.MenuPitch, MenuWidth: f.MenuWidth, MenuBg: f.MenuBg,
		}
		f.Layouts = append(f.Layouts, l)
	})
}

// loadLayout applies a saved layout's overlays + menu placement to the live config.
func (e *editor) loadLayout(name string) {
	e.m.mutate(func(f *config.VROverlayFeature) { applyLayoutTo(f, name) })
	e.selected = ""
}

// applyMenuPend commits the previewed menu transform to config + clears the shadow.
func (e *editor) applyMenuPend() {
	p := e.pend
	e.pend = pendMenu{}
	e.shadowSig = ""
	e.m.mutate(func(f *config.VROverlayFeature) {
		f.MenuSnap, f.MenuX, f.MenuY, f.MenuZ = p.snap, p.x, p.y, p.z
		f.MenuYaw, f.MenuPitch, f.MenuWidth = p.yaw, p.pitch, p.width
	})
}

// resetTransforms returns the menu to its auto placement + the selected overlay to its snap default.
func (e *editor) resetTransforms() {
	e.pend = pendMenu{}
	e.shadowSig = ""
	id := e.selected
	e.m.mutate(func(f *config.VROverlayFeature) {
		f.MenuSnap, f.MenuX, f.MenuY, f.MenuZ, f.MenuYaw, f.MenuPitch, f.MenuWidth = "", 0, 0, 0, 0, 0, 0
		if t := findOv(f, id); t != nil {
			t.X, t.Y, t.Z = defaultOffset(t.SnapTo)
			t.Yaw, t.Pitch, t.Roll = 0, 0, 0
		}
	})
}

// defaultOffset returns a sensible local offset (metres) for a snap mode, so switching mode doesn't
// reuse another mode's coordinates (which put hand-snapped overlays a metre away).
func defaultOffset(snap string) (x, y, z float64) {
	switch snap {
	case "left", "right":
		return 0, 0, -0.25 // 25 cm in front of the controller
	case "head":
		return 0, 0, -0.7 // 70 cm in front of the face (visor)
	default:
		return 0, 1.4, -1.0 // world: head height, 1 m forward
	}
}

// anchorIdx returns the tracked-device index an overlay's snap mode rides (false = world-anchored).
// snapTracked reports whether t's snap anchor currently has a valid pose (feeds needsApply).
func (e *editor) snapTracked(t Transform) bool {
	if e.ed == nil {
		return false
	}
	idx, ok := e.anchorIdx(handToSnap(t.Snap))
	if !ok {
		return false
	}
	_, ok = e.ed.DevicePose(idx)
	return ok
}

func (e *editor) anchorIdx(snap string) (int, bool) {
	switch snap {
	case "left":
		return e.ed.ControllerIndex(HandLeft)
	case "right":
		return e.ed.ControllerIndex(HandRight)
	case "head":
		return 0, true // HMD
	default:
		return 0, false
	}
}

// worldMatrixOf returns an overlay's current world transform (resolving controller/head anchoring).
func (e *editor) worldMatrixOf(o config.VROverlay) Mat34 {
	local := EulerToMat(o.Yaw, o.Pitch, o.Roll, o.X, o.Y, o.Z)
	if idx, ok := e.anchorIdx(o.SnapTo); ok {
		if dp, ok2 := e.ed.DevicePose(idx); ok2 {
			return MulMat(dp, local)
		}
	}
	return local
}

func (e *editor) addOverlay(typ string) {
	id := fmt.Sprintf("ov%d", time.Now().UnixMilli()%100000)
	x, y, z, yaw := e.spawnInFront()
	e.m.mutate(func(f *config.VROverlayFeature) {
		f.Overlays = append(f.Overlays, config.VROverlay{
			ID: id, Type: typ, Enabled: true, X: x, Y: y, Z: z, Yaw: yaw, WidthM: 0.5, Opacity: 0.9, MaxMessages: 8,
		})
	})
	e.selected = id
}

// spawnInFront places a new world-anchored overlay 1 m in front of the viewer at eye height, facing
// them. The old fixed (0, 1.4, −1) spawned at the PLAYSPACE ORIGIN - out of sight unless the user
// happened to stand there looking +Z. Falls back to those defaults without an HMD pose.
func (e *editor) spawnInFront() (x, y, z, yaw float64) {
	hmd, ok := e.ed.DevicePose(0)
	if !ok {
		return 0, 1.4, -1.0, 0
	}
	hx, hy, hz := MatPos(hmd)
	fx, _, fz := MatForward(hmd)
	l := math.Hypot(fx, fz) // flatten gaze to horizontal: looking down must not spawn in the floor
	if l < 1e-3 {
		return hx, hy, hz - 1.0, 0
	}
	fx, fz = fx/l, fz/l
	// Face normal is the overlay's local +Z (see worldMatrixOf); point it back at the viewer.
	return hx + fx, hy, hz + fz, math.Atan2(-fx, -fz) / deg2rad
}

func (e *editor) deleteSel() {
	id := e.selected
	e.m.mutate(func(f *config.VROverlayFeature) {
		for i := range f.Overlays {
			if f.Overlays[i].ID == id {
				f.Overlays = append(f.Overlays[:i], f.Overlays[i+1:]...)
				return
			}
		}
	})
	e.selected = ""
}

func (e *editor) adjust(fn func(*config.VROverlay)) {
	id := e.selected
	e.m.mutate(func(f *config.VROverlayFeature) {
		if t := findOv(f, id); t != nil {
			fn(t)
		}
	})
}

func findOv(f *config.VROverlayFeature, id string) *config.VROverlay {
	for i := range f.Overlays {
		if f.Overlays[i].ID == id {
			return &f.Overlays[i]
		}
	}
	return nil
}

func findOvByID(f config.VROverlayFeature, id string) *config.VROverlay {
	for i := range f.Overlays {
		if f.Overlays[i].ID == id {
			return &f.Overlays[i]
		}
	}
	return nil
}

func clampWidth(v float64) float64 {
	if v < 0.1 {
		return 0.1
	}
	if v > 4 {
		return 4
	}
	return v
}

func snapName(s string) string {
	switch s {
	case "left":
		return "left hand"
	case "right":
		return "right hand"
	case "head":
		return "visor (head)"
	default:
		return "world"
	}
}

func nextSnap(s string) string {
	switch s {
	case "":
		return "left"
	case "left":
		return "right"
	case "right":
		return "head"
	default:
		return ""
	}
}
