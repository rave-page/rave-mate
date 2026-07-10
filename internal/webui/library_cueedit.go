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
	"sort"
	"strings"
	"sync"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/cuepattern"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagwrite"
)

// ceJumpSizes are the Shift+Up/Down beat-jump steps.
var ceJumpSizes = []float64{1, 2, 4, 8, 16, 32, 64}

type ceSt struct {
	mu       sync.Mutex
	active   bool
	path     string
	track    musiclib.Track
	grid     *cuepattern.Grid
	drops    []float64
	cursorMs float64
	jump     float64      // Shift+arrow beat-jump size
	sel      map[int]bool // selected indexes into track.Cues
	dragA    float64      // rubber band anchor (axis ms; <0 idle)
	dragB    float64
	assign   map[int]string // drop index -> pattern id (apply flow)
	patName  string         // save-pattern name input
	toMem    bool           // last apply wrote memory cues (render hint only)
	report   *cuepattern.ApplyReport
	lastErr  string
	fileTag  bool // drops also written to the file tag (format supported)
}

// ceOverlay is the render snapshot mpWaveSVG draws (nil = mode off).
type ceOverlay struct {
	grid     *cuepattern.Grid
	cues     []musiclib.CuePoint
	drops    []float64
	cursorMs float64
	sel      map[int]bool
	dragA    float64 // rubber band (axis ms; dragA<0 = none)
	dragB    float64
}

func (u *UI) ce() *ceSt {
	u.ceMu.Lock()
	defer u.ceMu.Unlock()
	if u.ceState == nil {
		u.ceState = &ceSt{jump: 4, dragA: -1, dragB: -1}
	}
	return u.ceState
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
	return &ceOverlay{grid: c.grid, cues: c.track.Cues, drops: append([]float64(nil), c.drops...),
		cursorMs: c.cursorMs, sel: sel, dragA: c.dragA, dragB: c.dragB}
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
	c.sel = map[int]bool{}
	c.dragA, c.dragB = -1, -1
	c.assign = map[int]string{}
	c.report, c.lastErr = nil, ""
	c.fileTag = tagwrite.Supported(path)
	c.mu.Unlock()
	u.mpEnsureFile("library", path, tr)
	u.patchMain()
}

func (u *UI) ceClose() {
	c := u.ce()
	c.mu.Lock()
	c.active = false
	c.mu.Unlock()
	u.patchMain()
}

// ceReloadTrack re-reads the track from the collection after a cue write.
func (u *UI) ceReloadTrack() {
	c := u.ce()
	c.mu.Lock()
	path, active := c.path, c.active
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
	c.sel = map[int]bool{}
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
	u.cePatchWave()
	u.cePatchRail()
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

// ceToggleDrop adds (or with remove=true deletes) a drop at the cursor and persists it
// to libdb + the file tag.
func (u *UI) ceToggleDrop(remove bool) {
	c := u.ce()
	c.mu.Lock()
	if !c.active {
		c.mu.Unlock()
		return
	}
	if remove {
		c.drops = cuepattern.RemoveDrop(c.drops, c.cursorMs)
	} else {
		c.drops = cuepattern.AddDrop(c.drops, c.cursorMs)
	}
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
}

// ceSurf handles the waveform pointer stream while the editor owns it: click = move
// cursor (beat-snapped), drag = rubber-band select cues.
func (u *UI) ceSurf(host, val string) {
	phase, fx, ok := mpPos(val)
	if !ok {
		return
	}
	t := u.mpSnap(host)
	if len(t.media) == 0 {
		return
	}
	axisMs := t.mpAxisAt(fx) * 1000
	c := u.ce()
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
		if a < 0 {
			break
		}
		if b < a {
			a, b = b, a
		}
		beatMs := 500.0
		if c.grid != nil {
			beatMs = c.grid.BeatLenMs(axisMs)
		}
		if b-a < beatMs/2 { // click: move the cursor
			if c.grid != nil {
				c.cursorMs = c.grid.SnapMs(axisMs)
			}
			break
		}
		// drag: select cues inside [a,b]
		c.sel = map[int]bool{}
		for i, cue := range c.track.Cues {
			if cue.StartMs >= a && cue.StartMs <= b && cue.Kind != musiclib.CueGrid {
				c.sel[i] = true
			}
		}
	}
	c.mu.Unlock()
	u.cePatchWave()
	if phase == "up" {
		u.cePatchRail()
	}
}

// ceSavePattern exports the selected cues as a named pattern anchored at the cursor.
func (u *UI) ceSavePattern(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		u.toast(i18n.T("library.ce.nameNeeded"))
		return
	}
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
	anchor := c.cursorMs
	if di := cuepattern.NearestDrop(c.drops, anchor); di >= 0 {
		anchor = c.drops[di] // prefer the nearest drop as the anchor
	}
	tr := c.track
	c.mu.Unlock()
	p, err := cuepattern.Extract(tr, idx, anchor, name)
	if err != nil {
		u.toast(err.Error())
		return
	}
	if _, err := st.Save(p); err != nil {
		u.toast(i18n.T("library.ce.saveFailed") + err.Error())
		return
	}
	u.toast(i18n.T("library.ce.patternSaved", i18n.A{"name": name, "n": fmt.Sprint(len(p.Cues))}))
	u.cePatchRail()
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

// ceGridShift nudges every grid marker by deltaMs (manual alignment), rebuilds the
// beat math, and persists the new grid to the library (journaled).
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
	c.track.Beatgrid = grid
	if g, err := cuepattern.NewGrid(grid, c.track.DurationSec*1000); err == nil {
		c.grid = g
		c.cursorMs = g.SnapMs(c.cursorMs)
	}
	tr := c.track
	c.mu.Unlock()
	// mirror into the collection view + persist (coalescing writes is not worth the
	// complexity: one UPDATE per press on a single-writer sqlite is cheap)
	s := u.lib()
	s.mu.Lock()
	if t, ok := s.byPath[tr.Path]; ok {
		t.Beatgrid = grid
		s.byPath[tr.Path] = t
		for i := range s.tracks {
			if s.tracks[i].Path == tr.Path {
				s.tracks[i].Beatgrid = grid
			}
		}
	}
	s.mu.Unlock()
	u.bg(func() {
		if err := u.svc.Lib.UpdateTrackBeatgrid(tr, grid); err != nil {
			u.logErr("save beatgrid", err)
		}
	})
	u.cePatchWave()
}

// ceAudition: hold Space = play from the beat cursor, release = stop (press again
// restarts from the cursor).
func (u *UI) ceAudition(down bool) {
	const host = "library"
	t := u.mpSnap(host)
	m := t.activeMedia()
	if m == nil {
		return
	}
	if !down {
		u.mpStop(host)
		return
	}
	c := u.ce()
	c.mu.Lock()
	cur := c.cursorMs / 1000
	c.mu.Unlock()
	local := clampF(cur-t.mediaStart(t.active), 0, math.Max(m.dur, 0))
	if tr := u.mpEngineState(&t, m); tr.loaded {
		u.svc.Player.Seek(local)
		if tr.paused {
			u.mpAudCall(host, "play", func() { u.svc.Player.TogglePause() })
		}
		u.mpPatchTransport(u.mpSnap(host))
		return
	}
	u.mpStartPlayback(host, *m, local)
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

// cePatchRail re-renders the detail rail (cursor readout, drops list, selection).
func (u *UI) cePatchRail() {
	u.libPatchDetail()
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
func (u *UI) ceDetailHTML() string {
	return u.ceRailHTML()
}

// ceWaveHTML is the full-width player strip (waveform + beatgrid + markers + transport).
func (u *UI) ceWaveHTML() string {
	c := u.ce()
	c.mu.Lock()
	path, tr := c.path, c.track
	c.mu.Unlock()
	u.mpEnsureFile("library", path, tr)
	return u.mpHTML("library")
}

// ceRailHTML is the cue-editor card in the library detail rail.
func (u *UI) ceRailHTML() string {
	c := u.ce()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.active {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.ce.eyebrow")) + `</div><div class=insp-title>` +
		esc(trackTitle(c.track)) + `</div></div>`)

	// cursor + jump readout
	b.WriteString(`<div class=ce-status><span class=ce-pos>` + esc(i18n.T("library.ce.cursor")) + ` ` +
		pubClock(c.cursorMs/1000) + ` · ` + esc(i18n.T("library.ce.bar")) + ` ` + ceBarBeat(c.grid, c.cursorMs) + `</span>` +
		`<span class=ce-jump>` + esc(i18n.T("library.ce.jump", i18n.A{"n": fmt.Sprint(int(c.jump))})) + `</span>` +
		tipTopic("cue-edit") + `</div>`)

	// drops
	b.WriteString(`<div class=pb-label>` + esc(i18n.Tn("library.ce.drops", len(c.drops))) + `</div>`)
	if len(c.drops) == 0 {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.ce.noDropsHint")) + `</div>`)
	}
	for i, d := range c.drops {
		b.WriteString(`<div class=ce-drop><span class=ce-dropname data-act=` + attrQ(fmt.Sprintf("ce-goto:%f", d)) + `>DROP ` + fmt.Sprint(i+1) +
			` · ` + pubClock(d/1000) + `</span>`)
		if st := u.ceStore; st != nil {
			cur := c.assign[i]
			b.WriteString(ceAssignSelect(i, cur, st))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(btnRow(
		btn(i18n.T("library.ce.addDrop"), "outline", "ce-drop-add", ""),
		btn(i18n.T("library.ce.removeDrop"), "ghost", "ce-drop-del", "")))
	if !c.fileTag {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.ce.noFileTag")) + `</div>`)
	}

	// selection → pattern
	nsel := 0
	for _, on := range c.sel {
		if on {
			nsel++
		}
	}
	if nsel > 0 {
		b.WriteString(`<div class=pb-label>` + esc(i18n.T("library.ce.selection", i18n.A{"n": fmt.Sprint(nsel)})) + `</div>`)
		b.WriteString(`<div class=lib-toolbar>` + fieldRaw("ce-pat-name", "", i18n.T("library.ce.patternName")) +
			btn(i18n.T("library.ce.savePattern"), "outline", "ce-pat-save", "") + `</div>`)
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
	b.WriteString(btnRow(btn(i18n.T("common.close"), "ghost", "ce-close", "")))
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
