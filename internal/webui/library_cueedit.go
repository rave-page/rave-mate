package webui

// Cue editor (cue-prepare mode): beat-precise hotcue/memory-cue editing on the library
// player waveform. Workflow: open a collection track -> Prepare cues -> beatgrid renders
// on the waveform, arrow keys walk beats (Shift = beat-jump, size via Shift+Up/Down),
// T/Enter drop a DROP marker (Shift removes) - drops persist to libdb AND the file tag.
// Mouse: click = move the beat cursor, drag = rubber-band select cues; a selection can be
// saved as a named cue PATTERN (beat offsets vs the cursor drop). Patterns then apply to
// prepared tracks around each drop (different pattern per drop; cues outside the drop's
// span are cut - the drop is always the anchor).

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagwrite"
)

// ceJumpSizes are the Shift+Up/Down beat-jump steps.
var ceJumpSizes = []float64{1, 2, 4, 8, 16, 32, 64}

type ceSt struct {
	mu           sync.Mutex
	active       bool
	path         string
	track        musiclib.Track
	grid         *cuepattern.Grid
	drops        []float64
	cursorMs     float64
	jump         float64        // Shift+arrow beat-jump size
	sel          map[int]bool   // DERIVED index view of selMs (into track.Cues) - rebuilt by syncSel
	dsel         map[int]bool   // DERIVED index view of dselMs (into drops)
	selMs        map[int64]bool // persistent cue selection by rounded ms (survives reload + track nav)
	dselMs       map[int64]bool // persistent drop selection by rounded ms
	dragA        float64        // rubber band anchor (axis ms; <0 idle)
	dragB        float64
	dragMods     string         // modifiers at left-down ("c"/"s"; Ctrl+click = toggle selection)
	assign       map[int]string // drop index -> pattern id (apply flow)
	patName      string         // save-pattern name input
	toMem        bool           // last apply wrote memory cues (render hint only)
	report       *cuepattern.ApplyReport
	lastErr      string
	fileTag      bool        // drops also written to the file tag (format supported)
	tagTimer     *time.Timer // debounced file-tag drop write during grid nudges
	idleTimer    *time.Timer // stops the engine after idling paused in cue-edit (releases the file)
	prewarmTimer *time.Timer // debounced paused-decoder reposition on cursor moves (press = unpause-only)
	// cue write-back router (library_cuewrite.go)
	wbApplied   map[string]int // per-software tracks written (key absent = not written)
	wbBusy      bool           // a write is in flight (serialize)
	wbErr       string         // last write error ("" = none)
	wbTargets   []gfTarget     // cached DJ-software discovery (fs probes must not run per repaint)
	wbTargetsAt time.Time      // last discovery (zero = never)
}

// ceOverlay is the render snapshot mpWaveSVG draws (nil = mode off).
type ceOverlay struct {
	grid     *cuepattern.Grid
	cues     []musiclib.CuePoint
	drops    []float64
	cursorMs float64
	sel      map[int]bool
	dsel     map[int]bool
	dragA    float64 // rubber band (axis ms; dragA<0 = none)
	dragB    float64
}

func (u *UI) ce() *ceSt {
	u.ceMu.Lock()
	defer u.ceMu.Unlock()
	if u.ceState == nil {
		u.ceState = &ceSt{jump: 4, dragA: -1, dragB: -1,
			sel: map[int]bool{}, dsel: map[int]bool{}, selMs: map[int64]bool{}, dselMs: map[int64]bool{},
			assign: map[int]string{}}
	}
	return u.ceState
}

// ceKeyMs rounds a position to a stable ms key for the persistent selection sets.
func ceKeyMs(ms float64) int64 { return int64(math.Round(ms)) }

// syncSel rebuilds the derived index selection (sel/dsel) from the persistent ms sets against
// the current track - so a selection survives reload, drop add/remove and track nav. c LOCKED.
func (c *ceSt) syncSel() {
	sel := map[int]bool{}
	for i, q := range c.track.Cues {
		if q.Kind != musiclib.CueGrid && c.selMs[ceKeyMs(q.StartMs)] {
			sel[i] = true
		}
	}
	dsel := map[int]bool{}
	for i, d := range c.drops {
		if c.dselMs[ceKeyMs(d)] {
			dsel[i] = true
		}
	}
	c.sel, c.dsel = sel, dsel
}

// selectOnly replaces the persistent selection with one marker (cueMs OR dropMs; pass <0 for the
// unused kind). c LOCKED.
func (c *ceSt) selectOnly(cueMs, dropMs float64) {
	c.selMs, c.dselMs = map[int64]bool{}, map[int64]bool{}
	if cueMs >= 0 {
		c.selMs[ceKeyMs(cueMs)] = true
	}
	if dropMs >= 0 {
		c.dselMs[ceKeyMs(dropMs)] = true
	}
	c.syncSel()
}

// ceToggleKey flips a key in a selection set (delete when turning off, so stale keys don't linger).
func ceToggleKey(m map[int64]bool, k int64) {
	if m[k] {
		delete(m, k)
	} else {
		m[k] = true
	}
}

// ceActiveFor reports whether the cue editor owns the host's pointer surface.
func (u *UI) ceActiveFor(host string) bool {
	if host != "library" {
		return false
	}
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// ceSnapOverlay returns the waveform overlay data (nil when off / other track).
func (u *UI) ceSnapOverlay(host, mediaPath string) *ceOverlay {
	if host != "library" {
		return nil
	}
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active || c.path != mediaPath {
		return nil
	}
	sel := make(map[int]bool, len(c.sel))
	for k, v := range c.sel {
		sel[k] = v
	}
	dsel := make(map[int]bool, len(c.dsel))
	for k, v := range c.dsel {
		dsel[k] = v
	}
	return &ceOverlay{grid: c.grid, cues: c.track.Cues, drops: append([]float64(nil), c.drops...),
		cursorMs: c.cursorMs, sel: sel, dsel: dsel, dragA: c.dragA, dragB: c.dragB}
}

// cePatterns lazily opens the pattern store (nil on error - saving disabled).
func (u *UI) cePatterns() *cuepattern.Store {
	u.ceMu.Lock()
	defer u.ceMu.Unlock()
	if u.ceStore != nil {
		return u.ceStore
	}
	dir, err := config.DataPath("cuepatterns")
	if err != nil {
		return nil
	}
	st, err := cuepattern.OpenStore(dir)
	if err != nil {
		u.logErr("cue patterns", err)
		return nil
	}
	u.ceStore = st
	return st
}

// ── lifecycle ──

// ceEnter opens the cue editor for a collection track (needs a beatgrid).
func (u *UI) ceEnter(path string) {
	s := u.lib()
	s.mu.Lock()
	tr, ok := s.byPath[path]
	s.mu.Unlock()
	if !ok {
		u.toast(i18n.T("library.ce.notInCollection"))
		return
	}
	grid, err := cuepattern.NewGrid(tr.Beatgrid, tr.DurationSec*1000)
	if err != nil {
		u.toast(i18n.T("library.ce.noGrid"))
		return
	}
	drops, _ := u.svc.Lib.Drops(path)
	if len(drops) == 0 {
		if fd, err := tagwrite.ReadDrops(path); err == nil && len(fd) > 0 {
			drops = fd // file tag survives a fresh DB - adopt it
		}
	}
	cursor := grid.SnapMs(0)
	if len(drops) > 0 {
		cursor = drops[0]
	}
	c := u.ce()
	c.mu.Lock()
	c.active, c.path, c.track, c.grid = true, path, tr, grid
	c.drops, c.cursorMs = drops, cursor
	c.dragA, c.dragB = -1, -1
	if c.assign == nil {
		c.assign = map[int]string{}
	}
	c.report, c.lastErr = nil, ""
	c.wbApplied, c.wbBusy, c.wbErr = nil, false, ""
	c.fileTag = tagwrite.Supported(path)
	c.syncSel() // keep the persistent selection + drop→pattern assignment; re-derive vs the new track
	c.mu.Unlock()
	// the editing surface (full-width wave + batch bar) mounts in Collection
	u.mu.Lock()
	u.libSection = "collection"
	u.mu.Unlock()
	u.mpEnsureFile("library", path, tr)
	// keep the collection selection in sync with the editor target: the row
	// highlights and cue-edit ↑/↓ (ceNav) anchor on s.sel.
	s.mu.Lock()
	s.sel = &libSel{path: path, kind: "audio", track: tr, inColl: true}
	s.mu.Unlock()
	u.patchMain()
}

// ceEnterSet enters cue-prep for a whole set of paths (playlist / folder): eligible
// tracks (in collection + beatgrid) become the mass-apply selection, plID (if not 0)
// focuses the collection's playlist facet, and the editor opens on the first track.
// The editing surface lives in the Collection section, so entry switches there.
func (u *UI) ceEnterSet(paths []string, plID int64) {
	s := u.lib()
	s.mu.Lock()
	sel, first, skipped := map[string]bool{}, "", 0
	for _, p := range paths {
		tr, ok := s.byPath[p]
		if !ok || len(tr.Beatgrid) == 0 {
			skipped++
			continue
		}
		sel[p] = true
		if first == "" {
			first = p
		}
	}
	if first != "" {
		s.collSel = sel
		if plID != 0 {
			s.collPl = map[int64]bool{plID: true}
		}
	}
	s.mu.Unlock()
	if first == "" {
		u.toast(i18n.T("library.ce.setNone"))
		return
	}
	if plID != 0 {
		u.libRebuildPlFilter()
	}
	u.ceEnter(first)
	u.toast(i18n.T("library.ce.setToast", i18n.A{"n": fmt.Sprint(len(sel)), "skipped": fmt.Sprint(skipped)}))
}

func (u *UI) ceClose() {
	c := u.ce()
	c.mu.Lock()
	c.active = false
	c.mu.Unlock()
	u.patchMain()
}

// ceReloadTrack re-reads the track from the collection after a cue write. Cues changed,
// so earlier per-software cue writes are stale - the write-back router re-arms.
func (u *UI) ceReloadTrack() {
	c := u.ce()
	c.mu.Lock()
	path, active := c.path, c.active
	c.wbApplied, c.wbErr = nil, ""
	c.mu.Unlock()
	if !active {
		return
	}
	s := u.lib()
	s.mu.Lock()
	tr, ok := s.byPath[path]
	s.mu.Unlock()
	if !ok {
		return
	}
	c.mu.Lock()
	c.track = tr
	c.syncSel()
	c.mu.Unlock()
}

// ── mutations ──

// ceSetCursor snaps + moves the beat cursor.
func (u *UI) ceSetCursor(ms float64) {
	c := u.ce()
	c.mu.Lock()
	if c.grid != nil {
		c.cursorMs = c.grid.SnapMs(ms)
	}
	c.mu.Unlock()
	u.ceEnsureCursorVisible()
	u.cePrewarmSeek()
	u.cePatchWave()
	u.cePatchRail()
}

// ceStep moves the cursor by beats (keyboard).
func (u *UI) ceStep(beats float64) {
	c := u.ce()
	c.mu.Lock()
	if c.grid != nil {
		c.cursorMs = c.grid.StepMs(c.cursorMs, beats)
	}
	c.mu.Unlock()
	u.ceEnsureCursorVisible()
	u.cePrewarmSeek()
	u.cePatchWave()
	u.cePatchRail()
}

// ceEnsureCursorVisible pans a zoomed wave view so the beat cursor stays within
// ~[10%,90%] of the visible span (arrow-walking must not run the cursor off-screen).
func (u *UI) ceEnsureCursorVisible() {
	c := u.ce()
	c.mu.Lock()
	ms, active := c.cursorMs, c.active
	c.mu.Unlock()
	if !active {
		return
	}
	t := u.mpSnap("library")
	lo, ln := t.axis()
	if ln <= 0 || t.viewSpan >= 1 {
		return
	}
	f := (ms/1000 - lo) / ln // cursor as axis fraction
	var ns float64
	switch {
	case f < t.viewStart+0.1*t.viewSpan:
		ns = f - 0.1*t.viewSpan
	case f > t.viewStart+0.9*t.viewSpan:
		ns = f - 0.9*t.viewSpan
	default:
		return
	}
	u.mpMut("library", func(v *mpSt) { v.viewStart = clampF(ns, 0, 1-v.viewSpan) })
}

// ceJumpAdjust bumps the beat-jump size up/down the ceJumpSizes ladder.
func (u *UI) ceJumpAdjust(up bool) {
	c := u.ce()
	c.mu.Lock()
	idx := 0
	for i, v := range ceJumpSizes {
		if v == c.jump {
			idx = i
			break
		}
	}
	if up && idx < len(ceJumpSizes)-1 {
		idx++
	} else if !up && idx > 0 {
		idx--
	}
	c.jump = ceJumpSizes[idx]
	c.mu.Unlock()
	u.cePatchRail()
}

// ceToggleDrop adds (or with remove=true deletes) a drop at the cursor.
func (u *UI) ceToggleDrop(remove bool) {
	c := u.ce()
	c.mu.Lock()
	ms := c.cursorMs
	c.mu.Unlock()
	u.ceDropAt(ms, remove)
}

// ceDropAt adds/removes a drop at ms (grid-snapped) and persists it to libdb + the
// file tag.
func (u *UI) ceDropAt(ms float64, remove bool) {
	c := u.ce()
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	if c.grid != nil {
		ms = c.grid.SnapMs(ms)
	}
	if remove {
		c.drops = cuepattern.RemoveDrop(c.drops, ms)
		delete(c.dselMs, ceKeyMs(ms)) // drop gone from the persistent selection
	} else {
		c.drops = cuepattern.AddDrop(c.drops, ms)
	}
	c.syncSel() // drop indexes shifted - re-derive from the ms sets
	path, tr := c.path, c.track
	drops := append([]float64(nil), c.drops...)
	fileTag := c.fileTag
	c.mu.Unlock()
	u.bg(func() {
		if err := u.svc.Lib.SetDrops(path, tr.Artist, tr.Title, tr.DurationSec, drops); err != nil {
			u.logErr("save drops", err)
		}
		if fileTag {
			if err := tagwrite.WriteDrops(path, drops); err != nil {
				u.toast(i18n.T("library.ce.fileTagFailed") + err.Error())
			}
		}
	})
	u.libDropsChanged(path, drops)
	u.cePatchWave()
	u.cePatchRail()
	u.libPatchBody() // row census (◆n) follows the drop set
}

// ceSetCues persists a cue list for the open track and mirrors the collection state.
func (u *UI) ceSetCues(tr musiclib.Track, cues []musiclib.CuePoint) {
	u.bg(func() {
		if err := u.svc.Lib.UpdateTrackCues(tr, cues); err != nil {
			u.toast(i18n.T("library.ce.applyFailed") + err.Error())
			return
		}
		s := u.lib()
		s.mu.Lock()
		if t, ok := s.byPath[tr.Path]; ok {
			t.Cues = cues
			s.byPath[tr.Path] = t
			for i := range s.tracks {
				if s.tracks[i].Path == tr.Path {
					s.tracks[i].Cues = cues
				}
			}
		}
		s.mu.Unlock()
		u.ceReloadTrack()
		u.patchMain()
	})
}

// ceAddCueAt inserts a memory cue at ms (grid-snapped; 25ms dedup). Right-click.
func (u *UI) ceAddCueAt(ms float64) {
	c := u.ce()
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	if c.grid != nil {
		ms = c.grid.SnapMs(ms)
	}
	tr := c.track
	c.mu.Unlock()
	for _, q := range tr.Cues {
		if q.Kind != musiclib.CueGrid && math.Abs(q.StartMs-ms) < 25 {
			return // marker already here
		}
	}
	cues := append(append([]musiclib.CuePoint(nil), tr.Cues...),
		musiclib.CuePoint{Kind: musiclib.CuePlain, Hotcue: -1, StartMs: ms})
	sort.Slice(cues, func(i, j int) bool { return cues[i].StartMs < cues[j].StartMs })
	u.ceSetCues(tr, cues)
}

// ceRemoveAt deletes cue AND drop markers within eps of ms. Ctrl+right-click.
func (u *UI) ceRemoveAt(ms, eps float64) {
	c := u.ce()
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	tr, path, fileTag := c.track, c.path, c.fileTag
	dropsChanged := false
	kept := c.drops[:0:0]
	for _, d := range c.drops {
		if math.Abs(d-ms) < eps {
			dropsChanged = true
			continue
		}
		kept = append(kept, d)
	}
	if dropsChanged {
		c.drops = kept
		c.syncSel() // drop indexes shifted - re-derive from the ms sets
	}
	drops := append([]float64(nil), c.drops...)
	c.mu.Unlock()

	var cues []musiclib.CuePoint
	cuesChanged := false
	for _, q := range tr.Cues {
		if q.Kind != musiclib.CueGrid && math.Abs(q.StartMs-ms) < eps {
			cuesChanged = true
			continue
		}
		cues = append(cues, q)
	}
	if dropsChanged {
		u.bg(func() {
			if err := u.svc.Lib.SetDrops(path, tr.Artist, tr.Title, tr.DurationSec, drops); err != nil {
				u.logErr("save drops", err)
			}
			if fileTag {
				if err := tagwrite.WriteDrops(path, drops); err != nil {
					u.toast(i18n.T("library.ce.fileTagFailed") + err.Error())
				}
			}
		})
		u.libDropsChanged(path, drops)
	}
	switch {
	case cuesChanged:
		u.ceSetCues(tr, cues) // repaints everything
	case dropsChanged:
		u.cePatchWave()
		u.cePatchRail()
		u.libPatchBody()
	}
}

// ceSurf handles the waveform pointer stream while the editor owns it. Plain left-drag PANS
// the waveform (scroll only - never moves the cursor/playhead); a plain click positions the
// beat cursor or selects the marker under it (Ctrl+click toggles it in/out of the selection).
// SHIFT+drag rubber-band-selects cues + drops. Right button = memory cue / drop (Shift) /
// remove (Ctrl). Selection is held by ms (survives track nav + drop edits) via selMs/dselMs.
func (u *UI) ceSurf(host, val string) {
	phase, fx, ok := mpPos(val)
	if !ok {
		return
	}
	mods := "" // 3rd CSV field of the left-down value ("down:fx,fy,cs"; shell.go) - press only
	if p := strings.SplitN(val, ",", 3); len(p) == 3 {
		mods = p[2]
	}
	t := u.mpSnap(host)
	if len(t.media) == 0 {
		return
	}
	axisMs := t.mpAxisAt(fx) * 1000
	c := u.ce()
	// right-button one-shots (see shell.go pointer transport)
	switch phase {
	case "rdown": // right-click: memory cue at the beat
		u.ceAddCueAt(axisMs)
		return
	case "srdown": // Shift+right: drop marker
		u.ceDropAt(axisMs, false)
		return
	case "crdown": // Ctrl+right: remove cue + drop markers at the beat
		beatMs := 500.0
		c.mu.Lock()
		if c.grid != nil {
			beatMs = c.grid.BeatLenMs(axisMs)
		}
		c.mu.Unlock()
		u.ceRemoveAt(axisMs, beatMs/2)
		return
	}

	// classify the gesture: SHIFT at press = rubber-band select; anything else = pan.
	c.mu.Lock()
	selecting := c.dragA >= 0
	if phase == "down" {
		selecting = strings.Contains(mods, "s")
		c.dragMods = mods
		if !selecting {
			c.dragA, c.dragB = -1, -1 // plain press: clear any stale rubber-band
		}
	}
	c.mu.Unlock()

	if selecting { // SHIFT+drag: rubber-band select (cursor untouched)
		c.mu.Lock()
		switch phase {
		case "down":
			c.dragA, c.dragB = axisMs, axisMs
		case "move":
			if c.dragA >= 0 {
				c.dragB = axisMs
			}
		case "up":
			a, b := c.dragA, c.dragB
			c.dragA, c.dragB = -1, -1
			if a >= 0 {
				if b < a {
					a, b = b, a
				}
				c.selMs, c.dselMs = map[int64]bool{}, map[int64]bool{}
				for _, cue := range c.track.Cues {
					if cue.Kind != musiclib.CueGrid && cue.StartMs >= a && cue.StartMs <= b {
						c.selMs[ceKeyMs(cue.StartMs)] = true
					}
				}
				for _, d := range c.drops {
					if d >= a && d <= b {
						c.dselMs[ceKeyMs(d)] = true
					}
				}
				c.syncSel()
			}
		}
		c.mu.Unlock()
		u.cePatchWave()
		if phase == "up" {
			u.cePatchRail()
		}
		return
	}

	// plain: drag pans the view (cursor untouched); a click positions the cursor / selects a marker
	switch phase {
	case "move":
		u.mpMoveCoalesce(host, "pan", fx)
	case "down":
		mpMoveCancel(host)
		u.mpMut(host, func(v *mpSt) {
			v.dragGen++
			v.drag, v.dragAnchor, v.dragView, v.dragMoved = "pan", fx, v.viewStart, false
		})
	case "up":
		mpMoveCancel(host)
		moved := false
		nt := u.mpMut(host, func(v *mpSt) { moved = v.dragMoved; v.drag = "" })
		if moved { // it was a pan - leave the cursor where it was
			u.mpPatchWave(nt)
			return
		}
		// click: marker select / cursor move (Ctrl+click toggles the marker under the pointer)
		c.mu.Lock()
		ctrl := strings.Contains(c.dragMods, "c")
		beatMs := 500.0
		if c.grid != nil {
			beatMs = c.grid.BeatLenMs(axisMs)
		}
		ci, di, dist := ceNearestMarker(c, axisMs)
		hit := dist <= beatMs/2
		switch {
		case ctrl && hit && ci >= 0:
			ceToggleKey(c.selMs, ceKeyMs(c.track.Cues[ci].StartMs))
			c.syncSel()
		case ctrl && hit && di >= 0:
			ceToggleKey(c.dselMs, ceKeyMs(c.drops[di]))
			c.syncSel()
		case hit && ci >= 0:
			c.selectOnly(c.track.Cues[ci].StartMs, -1)
		case hit:
			c.selectOnly(-1, c.drops[di])
		default:
			if c.grid != nil {
				c.cursorMs = c.grid.SnapMs(axisMs)
			}
		}
		c.mu.Unlock()
		u.cePatchWave()
		u.cePatchRail()
	}
}

// ceNearestMarker finds the marker closest to ms: cue (ci≥0) or drop (di≥0), never
// both. dist=+Inf when the track has no markers. c is LOCKED by the caller.
func ceNearestMarker(c *ceSt, ms float64) (ci, di int, dist float64) {
	ci, di, dist = -1, -1, math.Inf(1)
	for i, q := range c.track.Cues {
		if q.Kind == musiclib.CueGrid {
			continue
		}
		if d := math.Abs(q.StartMs - ms); d < dist {
			ci, di, dist = i, -1, d
		}
	}
	for i, dm := range c.drops {
		if d := math.Abs(dm - ms); d < dist {
			ci, di, dist = -1, i, d
		}
	}
	return ci, di, dist
}

// ceDeleteSelected removes every selected cue AND drop in one action (Del/Backspace).
// Cues persist via UpdateTrackCues, drops via SetDrops (both journaled); the file-tag
// drop write is debounced so the keypress causes at most one tag rewrite. Empty
// selection = no-op (no toast spam).
func (u *UI) ceDeleteSelected() {
	c := u.ce()
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	nc, nd := 0, 0
	for _, on := range c.sel {
		if on {
			nc++
		}
	}
	for _, on := range c.dsel {
		if on {
			nd++
		}
	}
	if nc == 0 && nd == 0 {
		c.mu.Unlock()
		return
	}
	tr, path, fileTag := c.track, c.path, c.fileTag
	var cues []musiclib.CuePoint
	for i, q := range tr.Cues {
		if c.sel[i] {
			delete(c.selMs, ceKeyMs(q.StartMs))
			continue
		}
		cues = append(cues, q)
	}
	drops := c.drops[:0:0]
	for i, d := range c.drops {
		if c.dsel[i] {
			delete(c.dselMs, ceKeyMs(d))
			continue
		}
		drops = append(drops, d)
	}
	c.drops = drops
	c.syncSel()
	dropsCopy := append([]float64(nil), drops...)
	if nd > 0 && fileTag {
		if c.tagTimer != nil {
			c.tagTimer.Stop()
		}
		c.tagTimer = time.AfterFunc(800*time.Millisecond, func() {
			if err := tagwrite.WriteDrops(path, dropsCopy); err != nil {
				u.logErr("drops file tag", err)
			}
		})
	}
	c.mu.Unlock()
	if nd > 0 {
		u.bg(func() {
			if err := u.svc.Lib.SetDrops(path, tr.Artist, tr.Title, tr.DurationSec, dropsCopy); err != nil {
				u.logErr("save drops", err)
			}
		})
		u.libDropsChanged(path, dropsCopy)
	}
	if nc > 0 {
		u.ceSetCues(tr, cues) // repaints wave + rail + census
	} else {
		u.cePatchWave()
		u.cePatchRail()
		u.libPatchBody()
	}
	u.toast(i18n.T("library.ce.deletedToast", i18n.A{"cues": fmt.Sprint(nc), "drops": fmt.Sprint(nd)}))
}

// ceSavePattern exports the selected cues as a reusable pattern, anchored at the nearest drop
// (else the cursor). Frictionless: an empty name is auto-generated from the track, and the saved
// pattern is assigned to its anchor drop so it's immediately ready to apply.
func (u *UI) ceSavePattern(name string) {
	name = strings.TrimSpace(name)
	st := u.cePatterns()
	if st == nil {
		return
	}
	c := u.ce()
	c.mu.Lock()
	var idx []int
	for i, on := range c.sel {
		if on {
			idx = append(idx, i)
		}
	}
	sort.Ints(idx)
	if len(idx) == 0 {
		c.mu.Unlock()
		u.toast(i18n.T("library.ce.noCuesSel"))
		return
	}
	anchor := c.cursorMs
	dropIdx := cuepattern.NearestDrop(c.drops, anchor)
	if dropIdx >= 0 {
		anchor = c.drops[dropIdx] // prefer the nearest drop as the anchor
	}
	tr := c.track
	if name == "" {
		name = ceDefaultPatternName(tr, st)
	}
	c.mu.Unlock()
	p, err := cuepattern.Extract(tr, idx, anchor, name)
	if err != nil {
		u.toast(err.Error())
		return
	}
	saved, err := st.Save(p)
	if err != nil {
		u.toast(i18n.T("library.ce.saveFailed") + err.Error())
		return
	}
	// frictionless: assign the just-saved pattern to its anchor drop, ready to apply
	c.mu.Lock()
	if dropIdx >= 0 {
		if c.assign == nil {
			c.assign = map[int]string{}
		}
		c.assign[dropIdx] = saved.ID
	}
	c.patName = ""
	c.mu.Unlock()
	u.toast(i18n.T("library.ce.patternSaved", i18n.A{"name": name, "n": fmt.Sprint(len(saved.Cues))}))
	u.cePatchRail()
}

// ceDefaultPatternName auto-names a pattern from its source track, de-duped against existing
// names so re-saving from the same track stays distinct.
func ceDefaultPatternName(tr musiclib.Track, st *cuepattern.Store) string {
	base := strings.TrimSpace(trackTitle(tr))
	if base == "" {
		base = i18n.T("library.ce.patternFallback")
	}
	existing := map[string]bool{}
	for _, p := range st.List() {
		existing[p.Name] = true
	}
	name := base
	for i := 2; existing[name]; i++ {
		name = fmt.Sprintf("%s (%d)", base, i)
	}
	return name
}

// ceApply lays the assigned patterns around the drops and persists the cue list.
func (u *UI) ceApply(toMemory bool) {
	st := u.cePatterns()
	if st == nil {
		return
	}
	c := u.ce()
	c.mu.Lock()
	if !c.active || len(c.drops) == 0 {
		c.mu.Unlock()
		return
	}
	pats := map[int]cuepattern.Pattern{}
	for di, pid := range c.assign {
		if p, ok := st.Get(pid); ok && pid != "" {
			pats[di] = p
		}
	}
	tr, drops := c.track, append([]float64(nil), c.drops...)
	c.mu.Unlock()
	if len(pats) == 0 {
		u.toast(i18n.T("library.ce.noPatternPicked"))
		return
	}
	cues, rep, err := cuepattern.Apply(tr, drops, pats, cuepattern.ApplyOptions{ToMemory: toMemory, SnapDrop: true})
	if err != nil {
		u.toast(err.Error())
		return
	}
	u.bg(func() {
		if err := u.svc.Lib.UpdateTrackCues(tr, cues); err != nil {
			c.mu.Lock()
			c.lastErr = err.Error()
			c.mu.Unlock()
			u.toast(i18n.T("library.ce.applyFailed") + err.Error())
			return
		}
		s := u.lib()
		s.mu.Lock()
		if t, ok := s.byPath[tr.Path]; ok {
			t.Cues = cues
			s.byPath[tr.Path] = t
			for i := range s.tracks {
				if s.tracks[i].Path == tr.Path {
					s.tracks[i].Cues = cues
				}
			}
		}
		s.mu.Unlock()
		c.mu.Lock()
		c.report, c.toMem, c.lastErr = &rep, toMemory, ""
		c.mu.Unlock()
		u.ceReloadTrack()
		u.toast(i18n.T("library.ce.appliedToast", i18n.A{"n": fmt.Sprint(rep.Added)}))
		u.patchMain()
	})
}

// ceApplySelected mass-applies the editor's per-drop pattern assignment to every
// checked collection row: each track's own drops anchor the same pattern set; tracks
// without drops or a beatgrid are skipped and counted.
func (u *UI) ceApplySelected(toMemory bool) {
	st := u.cePatterns()
	if st == nil {
		return
	}
	c := u.ce()
	c.mu.Lock()
	pats := map[int]cuepattern.Pattern{}
	for di, pid := range c.assign {
		if p, ok := st.Get(pid); ok && pid != "" {
			pats[di] = p
		}
	}
	c.mu.Unlock()
	if len(pats) == 0 {
		u.toast(i18n.T("library.ce.noPatternPicked"))
		return
	}
	type job struct {
		tr    musiclib.Track
		drops []float64
	}
	s := u.lib()
	s.mu.Lock()
	jobs := make([]job, 0, len(s.collSel))
	for p := range s.collSel {
		if tr, ok := s.byPath[p]; ok {
			jobs = append(jobs, job{tr, append([]float64(nil), s.dropsIdx[p]...)})
		}
	}
	s.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].tr.Path < jobs[j].tr.Path })
	u.bg(func() {
		applied, skipped := 0, 0
		for _, j := range jobs {
			if len(j.drops) == 0 || len(j.tr.Beatgrid) == 0 {
				skipped++
				continue
			}
			cues, _, err := cuepattern.Apply(j.tr, j.drops, pats, cuepattern.ApplyOptions{ToMemory: toMemory, SnapDrop: true})
			if err != nil {
				skipped++
				continue
			}
			if err := u.svc.Lib.UpdateTrackCues(j.tr, cues); err != nil {
				skipped++
				continue
			}
			s.mu.Lock()
			if t, ok := s.byPath[j.tr.Path]; ok {
				t.Cues = cues
				s.byPath[j.tr.Path] = t
				for i := range s.tracks {
					if s.tracks[i].Path == j.tr.Path {
						s.tracks[i].Cues = cues
					}
				}
			}
			s.mu.Unlock()
			applied++
		}
		u.ceReloadTrack()
		u.toast(i18n.T("library.ce.batchToast", i18n.A{"applied": fmt.Sprint(applied), "skipped": fmt.Sprint(skipped)}))
		u.patchMain()
	})
}

// ceConvertAll demotes every hotcue on the track to a memory cue.
func (u *UI) ceConvertAll() {
	c := u.ce()
	c.mu.Lock()
	tr := c.track
	active := c.active
	c.mu.Unlock()
	if !active {
		return
	}
	cues := cuepattern.ConvertHotcuesToMemory(tr.Cues)
	u.bg(func() {
		if err := u.svc.Lib.UpdateTrackCues(tr, cues); err != nil {
			u.toast(i18n.T("library.ce.applyFailed") + err.Error())
			return
		}
		s := u.lib()
		s.mu.Lock()
		if t, ok := s.byPath[tr.Path]; ok {
			t.Cues = cues
			s.byPath[tr.Path] = t
			for i := range s.tracks {
				if s.tracks[i].Path == tr.Path {
					s.tracks[i].Cues = cues
				}
			}
		}
		s.mu.Unlock()
		u.ceReloadTrack()
		u.toast(i18n.T("library.ce.convertedToast"))
		u.patchMain()
	})
}

// ceKey handles the cue-edit keyboard scope (see shell.go keydown transport).
func (u *UI) ceKey(val string) {
	c := u.ce()
	c.mu.Lock()
	jump := c.jump
	active := c.active
	c.mu.Unlock()
	if !active {
		return
	}
	switch val {
	case "up": // move the editor to the prev/next collection track (list nav)
		u.ceNav(false)
	case "down":
		u.ceNav(true)
	case "left":
		u.ceStep(-1)
	case "right":
		u.ceStep(1)
	case "sleft":
		u.ceStep(-jump)
	case "sright":
		u.ceStep(jump)
	case "sup":
		u.ceJumpAdjust(true)
	case "sdown":
		u.ceJumpAdjust(false)
	case "enter", "t":
		u.ceToggleDrop(false)
	case "senter", "st":
		u.ceToggleDrop(true)
	case "del":
		u.ceDeleteSelected()
	case "space", "sspace":
		u.ceAudition(true)
	case "spaceup":
		u.ceAudition(false)
	case "cleft": // Ctrl: shift the whole beatgrid for manual alignment (10ms steps,
		u.ceGridShift(-10) // key-repeat gives continuous travel)
	case "cright":
		u.ceGridShift(10)
	case "csleft": // Ctrl+Shift: 1ms ultra-fine
		u.ceGridShift(-1)
	case "csright":
		u.ceGridShift(1)
	}
}

// ceGridShift nudges the whole grid AND every cue/drop marker by deltaMs - manual
// alignment must keep markers glued to their beats. Rebuilds the beat math and
// persists grid + cues + drops (journaled). The file-tag drop write is debounced:
// key-repeat would otherwise rewrite the tag dozens of times a second.
func (u *UI) ceGridShift(deltaMs float64) {
	c := u.ce()
	c.mu.Lock()
	if !c.active || len(c.track.Beatgrid) == 0 {
		c.mu.Unlock()
		return
	}
	grid := append([]musiclib.GridMarker(nil), c.track.Beatgrid...)
	for i := range grid {
		grid[i].PositionMs += deltaMs
	}
	cues := append([]musiclib.CuePoint(nil), c.track.Cues...)
	for i := range cues {
		cues[i].StartMs += deltaMs
	}
	drops := make([]float64, len(c.drops))
	for i, d := range c.drops {
		drops[i] = d + deltaMs
	}
	c.track.Beatgrid, c.track.Cues, c.drops = grid, cues, drops
	// the persistent selection is keyed by ms - shift it with the markers so it stays glued
	d := int64(math.Round(deltaMs))
	shiftKeys := func(m map[int64]bool) map[int64]bool {
		ns := make(map[int64]bool, len(m))
		for k := range m {
			ns[k+d] = true
		}
		return ns
	}
	c.selMs, c.dselMs = shiftKeys(c.selMs), shiftKeys(c.dselMs)
	c.wbApplied, c.wbErr = nil, "" // cue positions moved - earlier software writes are stale
	if g, err := cuepattern.NewGrid(grid, c.track.DurationSec*1000); err == nil {
		c.grid = g
		c.cursorMs = g.SnapMs(c.cursorMs + deltaMs)
	}
	c.syncSel()
	tr, path, fileTag := c.track, c.path, c.fileTag
	dropsCopy := append([]float64(nil), drops...)
	if c.tagTimer != nil {
		c.tagTimer.Stop()
	}
	if fileTag {
		c.tagTimer = time.AfterFunc(800*time.Millisecond, func() {
			if err := tagwrite.WriteDrops(path, dropsCopy); err != nil {
				u.logErr("drops file tag", err)
			}
		})
	}
	c.mu.Unlock()
	// mirror into the collection view + persist (one UPDATE per press on a
	// single-writer sqlite is cheap; only the file tag is debounced)
	s := u.lib()
	s.mu.Lock()
	if t, ok := s.byPath[path]; ok {
		t.Beatgrid, t.Cues = grid, cues
		s.byPath[path] = t
		for i := range s.tracks {
			if s.tracks[i].Path == path {
				s.tracks[i].Beatgrid, s.tracks[i].Cues = grid, cues
			}
		}
	}
	s.mu.Unlock()
	u.libDropsChanged(path, dropsCopy)
	u.bg(func() {
		if err := u.svc.Lib.UpdateTrackBeatgrid(tr, grid); err != nil {
			u.logErr("save beatgrid", err)
		}
		if err := u.svc.Lib.UpdateTrackCues(tr, cues); err != nil {
			u.logErr("save cues", err)
		}
		if err := u.svc.Lib.SetDrops(path, tr.Artist, tr.Title, tr.DurationSec, dropsCopy); err != nil {
			u.logErr("save drops", err)
		}
	})
	u.cePatchWave()
	u.cePatchRail()
}

// ceAudition: hold Space = play from the beat cursor, release = PAUSE + re-seek to the
// cursor (decoder stays positioned + read-ahead primed - the next press is unpause-only,
// no engine reload, no ffprobe). An idle timer stops the engine after a while paused so
// the child doesn't hold the file open forever (tag writes need the rename).
func (u *UI) ceAudition(down bool) {
	const host = "library"
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	c := u.ce()
	c.mu.Lock()
	cur := c.cursorMs / 1000
	c.mu.Unlock()
	local := clampF(cur-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
	if !down {
		tr := u.mpEngineState(&t, m)
		if m.kind != "video" && tr.loaded && !tr.paused && u.svc.Player != nil {
			path := m.path
			u.mpAudCall(host, "pause", func() {
				u.svc.Player.TogglePause()
				u.svc.Player.SeekExplicit(local) // pre-position for the next press
			})
			u.ceArmIdleStop(path)
			u.mpPatchTransport(u.mpSnap(host))
			return
		}
		u.mpStop(host)
		return
	}
	u.ceCancelIdleStop()
	if tr := u.mpEngineState(&t, m); tr.loaded {
		u.svc.Player.SeekExplicit(local) // beat-precise: bypass the 0.5s seek-noop guard
		if tr.paused {
			u.mpAudCall(host, "play", func() { u.svc.Player.TogglePause() })
		}
		u.mpPatchTransport(u.mpSnap(host))
		return
	}
	u.mpStartPlayback(host, *m, local)
}

// ceIdleStop: how long a paused cue-edit audition may hold the engine (and the open file).
const ceIdleStop = 90 * time.Second

// ceArmIdleStop schedules an engine stop if still paused on path when it fires.
func (u *UI) ceArmIdleStop(path string) {
	c := u.ce()
	c.mu.Lock()
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	c.idleTimer = time.AfterFunc(ceIdleStop, func() {
		if u.svc.Player == nil {
			return
		}
		if st := u.svc.Player.State(); st.Path == path && st.Playing && st.Paused {
			u.svc.Player.Stop()
			u.mpPatchTransport(u.mpSnap("library"))
		}
	})
	c.mu.Unlock()
}

func (u *UI) ceCancelIdleStop() {
	c := u.ce()
	c.mu.Lock()
	if c.idleTimer != nil {
		c.idleTimer.Stop()
		c.idleTimer = nil
	}
	c.mu.Unlock()
}

// cePrewarmSeek (debounced ~150ms after the last cursor move) repositions the PAUSED
// decoder at the beat cursor, so any audition press is unpause-only. No-op while playing.
func (u *UI) cePrewarmSeek() {
	c := u.ce()
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	path := c.path
	if c.prewarmTimer != nil {
		c.prewarmTimer.Stop()
	}
	c.prewarmTimer = time.AfterFunc(150*time.Millisecond, func() {
		if u.svc.Player == nil {
			return
		}
		if st := u.svc.Player.State(); st.Path != path || !st.Playing || !st.Paused {
			return
		}
		c.mu.Lock()
		cur, active, p := c.cursorMs/1000, c.active, c.path
		c.mu.Unlock()
		if !active || p != path {
			return
		}
		t := u.mpSnap("library")
		m := t.activeMedia()
		if m == nil || m.path != path {
			return
		}
		u.svc.Player.SeekExplicit(clampF(cur-t.mediaStart(t.active), 0, math.Max(m.dur, 0)))
	})
	c.mu.Unlock()
}

// libKeyNav moves the collection selection with the arrow keys (library scope).
func (u *UI) libKeyNav(down bool) {
	s := u.lib()
	s.mu.Lock()
	view := s.collView()
	cur := ""
	if s.sel != nil {
		cur = s.sel.path
	}
	next := ""
	if len(view) > 0 {
		idx := -1
		for vi, ti := range view {
			if s.tracks[ti].Path == cur {
				idx = vi
				break
			}
		}
		switch {
		case idx < 0 && down:
			next = s.tracks[view[0]].Path
		case idx < 0:
			next = s.tracks[view[len(view)-1]].Path
		case down && idx < len(view)-1:
			next = s.tracks[view[idx+1]].Path
		case !down && idx > 0:
			next = s.tracks[view[idx-1]].Path
		}
	}
	s.mu.Unlock()
	if next != "" {
		u.libSelect(next, nil)
	}
}

// ceNav (cue-edit ↑/↓) moves the collection selection to the prev/next track and
// re-targets the open editor to it. libKeyNav moves the selection + row highlight;
// ceFollow then swaps the waveform/grid/cursor to the newly selected track.
func (u *UI) ceNav(down bool) {
	u.libKeyNav(down)
	s := u.lib()
	s.mu.Lock()
	p := ""
	if s.sel != nil {
		p = s.sel.path
	}
	s.mu.Unlock()
	u.ceFollow(p)
}

// ceFollow re-targets the OPEN cue editor to path when a different collection track
// is selected (row click or ↑/↓). Silent no-op if the editor is closed, already on
// this track, or the track has no beatgrid - list nav must not toast-spam across
// gridless rows (explicit ce-open still toasts the "no grid" hint via ceEnter).
func (u *UI) ceFollow(path string) {
	if path == "" {
		return
	}
	c := u.ce()
	c.mu.Lock()
	active, same := c.active, c.path == path
	c.mu.Unlock()
	if !active || same {
		return
	}
	s := u.lib()
	s.mu.Lock()
	tr, ok := s.byPath[path]
	s.mu.Unlock()
	if !ok || len(tr.Beatgrid) == 0 {
		return
	}
	u.ceEnter(path)
}

// libDropsChanged updates the collection's drops index (the "no drops" facet).
func (u *UI) libDropsChanged(path string, drops []float64) {
	s := u.lib()
	s.mu.Lock()
	if s.dropsIdx == nil {
		s.dropsIdx = map[string][]float64{}
	}
	if len(drops) == 0 {
		delete(s.dropsIdx, path)
	} else {
		s.dropsIdx[path] = drops
	}
	s.mu.Unlock()
}

// ── rendering ──

// cePatchWave re-renders just the waveform fragment (cursor/drop/selection moved).
func (u *UI) cePatchWave() {
	t := u.mpSnap("library")
	if len(t.media) > 0 {
		u.mpPatchWave(t)
	}
}

// cePatchRail re-renders the detail rail + the topbar readout above the waveform.
func (u *UI) cePatchRail() {
	u.libPatchDetail()
	u.eval("window.__patch('ce-topbar'," + jsQuote(u.ceTopbarHTML()) + ")")
}

// ceBarBeat formats a cursor position as "bar.beat" from the first grid anchor.
func ceBarBeat(g *cuepattern.Grid, ms float64) string {
	if g == nil {
		return ""
	}
	b := math.Round(g.BeatsBetween(g.SnapMs(0), ms))
	bar := int(math.Floor(b/4)) + 1
	beat := int(math.Mod(b, 4))
	if beat < 0 {
		beat += 4
	}
	return fmt.Sprintf("%d.%d", bar, beat+1)
}

// ceDetailHTML is the detail rail while the editor is active: controls only - the
// waveform renders full-tab-width above the list (ceWaveHTML via libBody).
// s = the library state, LOCKED by the caller (libDetailHTML render path).
func (u *UI) ceDetailHTML(s *libSt) string {
	return u.ceRailHTML(s)
}

// ceWaveHTML is the full-width player strip: info topbar + waveform (beatgrid +
// markers + beat distances) + transport. The editor's readouts live HERE, on the wave.
func (u *UI) ceWaveHTML() string {
	c := u.ce()
	c.mu.Lock()
	path, tr := c.path, c.track
	c.mu.Unlock()
	u.mpEnsureFile("library", path, tr)
	return `<div id=ce-topbar>` + u.ceTopbarHTML() + `</div>` + u.mpHTML("library")
}

// ceTopbarHTML: track identity, cursor position (time + bar.beat), jump size, drops
// (clickable = jump) and cue census in one strip above the waveform.
func (u *UI) ceTopbarHTML() string {
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=ce-topbar>`)
	b.WriteString(`<span class=ce-tb-eyebrow>` + esc(i18n.T("library.ce.eyebrow")) + `</span>`)
	b.WriteString(`<span class=ce-tb-title>` + esc(trackTitle(c.track)) + `</span>`)
	meta := ""
	if c.track.BPM > 0 {
		meta = fmt.Sprintf("%.1f BPM", c.track.BPM)
	}
	if k := strings.TrimSpace(c.track.Key); k != "" {
		if meta != "" {
			meta += " · "
		}
		meta += k
	}
	if meta != "" {
		b.WriteString(`<span class=ce-tb-meta>` + esc(meta) + `</span>`)
	}
	b.WriteString(`<span class=ce-tb-cursor>▸ ` + pubClock(c.cursorMs/1000) + ` · ` +
		esc(i18n.T("library.ce.bar")) + ` ` + ceBarBeat(c.grid, c.cursorMs) + `</span>`)
	b.WriteString(`<span class=ce-jump>` + esc(i18n.T("library.ce.jump", i18n.A{"n": fmt.Sprint(int(c.jump))})) + `</span>`)
	for i, d := range c.drops {
		b.WriteString(`<span class=ce-tb-drop data-act=` + attrQ(fmt.Sprintf("ce-goto:%f", d)) +
			`>D` + ceDropLabel(i) + ` ` + pubClock(d/1000) + `</span>`)
	}
	b.WriteString(`<span class=ce-tb-meta>` + esc(i18n.Tn("library.ce.patternCues", ceCueCount(c.track.Cues))) + `</span>`)
	if !c.fileTag {
		b.WriteString(`<span class=ce-tb-warn title=` + attrQ(i18n.T("library.ce.noFileTag")) + `>⚠</span>`)
	}
	b.WriteString(`<span class=ce-tb-spacer></span>` + tipTopic("cue-edit") +
		btn("✕ "+i18n.T("common.close"), "ghost", "ce-close", ""))
	b.WriteString(`</div>`)
	return b.String()
}

// ceCueCount counts non-grid cues (what the waveform flags show).
func ceCueCount(cues []musiclib.CuePoint) int {
	n := 0
	for _, c := range cues {
		if c.Kind != musiclib.CueGrid {
			n++
		}
	}
	return n
}

// ceRailHTML is the cue-editor card in the library detail rail. s is LOCKED by the
// caller - never re-lock it below (deadlock).
func (u *UI) ceRailHTML(s *libSt) string {
	wb := u.ceWriteHTML(s) // built first - locks ceSt itself (never nested under c.mu)
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return ""
	}
	// controls only - the readouts (cursor, drop times, cue census) live in the
	// ce-topbar on the waveform strip
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.ce.eyebrow")) + `</div><div class=insp-title>` +
		esc(trackTitle(c.track)) + `</div></div>`)

	// drops → pattern assign grid (fixed rows drop 1-4 + X; unplaced rows still show)
	st := u.cePatterns() // ensure the store is open so the pickers render on first use
	b.WriteString(ceAssignGridHTML(c, st))
	b.WriteString(btnRow(
		btn(i18n.T("library.ce.addDrop"), "outline", "ce-drop-add", ""),
		btn(i18n.T("library.ce.removeDrop"), "ghost", "ce-drop-del", "")))

	// selection → pattern (cues) / delete (cues + drops)
	nsel, ndsel := 0, 0
	for _, on := range c.sel {
		if on {
			nsel++
		}
	}
	for _, on := range c.dsel {
		if on {
			ndsel++
		}
	}
	if nsel > 0 {
		b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.ce.selection", i18n.A{"n": fmt.Sprint(nsel)})) + `</div>`)
		b.WriteString(`<div class=lib-toolbar>` + fieldRaw("ce-pat-name", "", i18n.T("library.ce.patternName")) +
			btn(i18n.T("library.ce.savePattern"), "outline", "ce-pat-save", "") + `</div>`)
	}
	if ndsel > 0 {
		b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.ce.selDrops", i18n.A{"n": fmt.Sprint(ndsel)})) + `</div>`)
	}
	if nsel+ndsel > 0 {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.ce.delHint")) + `</div>`)
	}

	// apply
	if len(c.drops) > 0 {
		b.WriteString(`<div class=btn-col>` +
			btn(i18n.T("library.ce.applyHot"), "primary", "ce-apply:hot", "") +
			btn(i18n.T("library.ce.applyMem"), "outline", "ce-apply:mem", "") + `</div>`)
	}
	b.WriteString(btnRow(btn(i18n.T("library.ce.convertAll"), "ghost", "ce-convert", "")))
	if c.report != nil {
		r := c.report
		b.WriteString(hint("ok", i18n.T("library.ce.reportHint", i18n.A{
			"added": fmt.Sprint(r.Added), "cut": fmt.Sprint(r.Cut),
			"skipped": fmt.Sprint(r.Skipped), "demoted": fmt.Sprint(r.Demoted)})))
	}
	if c.lastErr != "" {
		b.WriteString(hint("bad", c.lastErr))
	}
	b.WriteString(wb)
	b.WriteString(btnRow(btn(i18n.T("common.close"), "ghost", "ce-close", "")))
	return b.String()
}

// ceAssignRows is the fixed minimum of assign-grid rows: drop 1-4 + the extra "X".
// The grid always shows these five (more if extra drops are placed) so a pattern can be
// picked for a slot even before its marker exists.
const ceAssignRows = 5

// ceDropLabel names a drop slot by index: 1-4, then X (the extra "x" drop), then 6+.
func ceDropLabel(i int) string {
	if i == 4 {
		return "X"
	}
	return fmt.Sprint(i + 1)
}

// ceAssignGridHTML renders the compact drop→pattern assign grid: one row per drop
// (1-4 + X, plus any extra placed drops), each carrying the drop label (click = jump when
// placed), its position (or an "unplaced" hint), and the pattern picker. Assignments
// persist in c.assign (drop index → pattern id) - the same map ceSavePattern auto-fills
// and ceApply reads - so they survive track nav with the rest of the cue-edit state.
// c is LOCKED by the caller (ceRailHTML); never re-lock it here.
func ceAssignGridHTML(c *ceSt, st *cuepattern.Store) string {
	rows := ceAssignRows
	if len(c.drops) > rows {
		rows = len(c.drops)
	}
	var b strings.Builder
	b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.ce.assignTitle")) + `</div>`)
	b.WriteString(`<div class=ce-agrid>`)
	for i := 0; i < rows; i++ {
		placed := i < len(c.drops)
		cls := "ce-arow"
		if !placed {
			cls += " unplaced"
		}
		b.WriteString(`<div class="` + cls + `">`)
		tag := `DROP ` + ceDropLabel(i)
		if placed {
			b.WriteString(`<span class=ce-arow-tag data-act=` + attrQ(fmt.Sprintf("ce-goto:%f", c.drops[i])) + `>` + tag + `</span>`)
			b.WriteString(`<span class=ce-arow-when>` + pubClock(c.drops[i]/1000) + `</span>`)
		} else {
			b.WriteString(`<span class=ce-arow-tag>` + tag + `</span>`)
			b.WriteString(`<span class="ce-arow-when unplaced" title=` + attrQ(i18n.T("library.ce.unplacedTip")) +
				`>` + esc(i18n.T("library.ce.unplaced")) + `</span>`)
		}
		if st != nil {
			b.WriteString(ceAssignSelect(i, c.assign[i], st))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	if len(c.drops) == 0 {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.ce.noDropsHint")) + `</div>`)
	}
	return b.String()
}

// ceAssignSelect renders the per-drop pattern picker.
func ceAssignSelect(dropIdx int, cur string, st *cuepattern.Store) string {
	id := fmt.Sprintf("ce-assign-%d", dropIdx)
	curLabel := ""
	if p, ok := st.Get(cur); ok {
		curLabel = p.Name
	}
	return smartSelect(id, "", fmt.Sprintf("ce-assign:%d:", dropIdx), curLabel, func() []ssOpt {
		opts := []ssOpt{{Val: "", Label: "—"}}
		for _, p := range st.List() {
			opts = append(opts, ssOpt{Val: p.ID, Label: p.Name,
				Sub: i18n.Tn("library.ce.patternCues", len(p.Cues)), Badge: p.FromTrack})
		}
		return opts
	})
}

// ── actions ──

func init() {
	onPrefix("ce-open:", func(u *UI, m actMsg) { u.ceEnter(m.arg("ce-open:")) })
	onPrefix("ce-open-pl:", func(u *UI, m actMsg) {
		id := int64(atoi(m.arg("ce-open-pl:")))
		paths, _ := u.svc.Lib.PlaylistTracks(id)
		u.ceEnterSet(paths, id)
	})
	onExact("ce-open-dir", func(u *UI, m actMsg) {
		dir := u.libDirOr()
		ents, err := os.ReadDir(dir)
		if err != nil {
			u.toast(i18n.T("library.browse.cannotRead", i18n.A{"path": dir}))
			return
		}
		var paths []string
		for _, e := range ents {
			if !e.IsDir() {
				if p := filepath.Join(dir, e.Name()); pubIsAudio(p) {
					paths = append(paths, p)
				}
			}
		}
		u.ceEnterSet(paths, 0)
	})
	onExact("ce-close", func(u *UI, _ actMsg) { u.ceClose() })
	onExact("ce-drop-add", func(u *UI, _ actMsg) { u.ceToggleDrop(false) })
	onExact("ce-drop-del", func(u *UI, _ actMsg) { u.ceToggleDrop(true) })
	onPrefix("ce-goto:", func(u *UI, m actMsg) {
		var ms float64
		if _, err := fmt.Sscanf(m.arg("ce-goto:"), "%f", &ms); err == nil {
			u.ceSetCursor(ms)
		}
	})
	onExact("ce-pat-name", func(u *UI, m actMsg) {
		c := u.ce()
		c.mu.Lock()
		c.patName = m.Val
		c.mu.Unlock()
	})
	onExact("ce-pat-save", func(u *UI, _ actMsg) {
		c := u.ce()
		c.mu.Lock()
		name := c.patName
		c.mu.Unlock()
		u.ceSavePattern(name)
	})
	onPrefix("ce-assign:", func(u *UI, m actMsg) {
		rest := m.arg("ce-assign:") // "<dropIdx>:<patternID>"
		var di int
		var pid string
		if i := strings.Index(rest, ":"); i >= 0 {
			if _, err := fmt.Sscanf(rest[:i], "%d", &di); err != nil {
				return
			}
			pid = rest[i+1:]
		} else {
			return
		}
		c := u.ce()
		c.mu.Lock()
		if c.assign == nil {
			c.assign = map[int]string{}
		}
		c.assign[di] = pid
		c.mu.Unlock()
		u.cePatchRail()
	})
	onPrefix("ce-apply:", func(u *UI, m actMsg) { u.ceApply(m.arg("ce-apply:") == "mem") })
	onPrefix("ce-apply-sel:", func(u *UI, m actMsg) { u.ceApplySelected(m.arg("ce-apply-sel:") == "mem") })
	onExact("ce-convert", func(u *UI, _ actMsg) { u.ceConvertAll() })
	// keyboard scopes (shell.go keydown transport; scope-gated + focus-gated in JS)
	onPrefix("key:", func(u *UI, m actMsg) {
		switch m.arg("key:") {
		case "cueedit":
			u.ceKey(m.Val)
		case "library":
			switch m.Val {
			case "up":
				u.libKeyNav(false)
			case "down":
				u.libKeyNav(true)
			}
		}
	})
}
