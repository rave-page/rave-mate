package webui

// Beatgrid-fixer cockpit: lives in the Collection right rail. States: idle (entry
// button) -> scope confirm -> running (live FIX/OK/MANUAL tiles + tray tooltip) ->
// done (summary + Apply / prep-playlist) -> results view swaps the track list.
// The batch itself is READ-ONLY (gridfix.Batch); Apply is the only write, routed
// per detected DJ software (Traktor NML / Rekordbox XML / VirtualDJ database.xml /
// Serato file tags), each after a backup of its target.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/cuewriteback"
	"rave.page/mate/internal/gridfix"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/seratolib"
	"rave.page/mate/internal/sysactivity"
	"rave.page/mate/internal/tray"
)

const gridfixPrepPlaylist = "MANUAL_GRIDDING_PREP"

type gfState struct {
	mu        sync.Mutex
	stage     string // "" idle | "confirm" | "running" | "done"
	scope     string // "all" | "filtered" | "selected"
	force     bool   // force re-analyze: override the multi-marker/lock skips + the cache (verified stay protected)
	prog      gridfix.BatchProgress
	results   []gridfix.TrackResult
	applied   map[string]int // per-software entries written on Apply (key absent = not applied)
	applyBusy bool           // an apply is in flight (serialize writes)
	applyErr  string         // last apply error ("" = none)
	prepped   int            // tracks pushed to the prep playlist (-1 = not yet)
	resView   bool           // main list shows batch results instead of tracks
	resFlt    string         // results filter: "" all | FIX | OK | SKIP | ERR
	cancel    context.CancelFunc
	cache     *gridfix.DetectionCache
	eng       *gridfix.Engine
}

// gfVStores shares ONE VerifiedStore per data dir across ALL UIs (window + headless remote
// sessions): saves rewrite the whole file, so two open handles would last-write-wins clobber
// each other's marks.
var (
	gfVStoreMu sync.Mutex
	gfVStores  = map[string]*gridfix.VerifiedStore{}
)

// gfVerified lazily opens the verified-grid store (nil on error - marking disabled).
func (u *UI) gfVerified() *gridfix.VerifiedStore {
	dir, err := config.DataPath("gridfix")
	if err != nil {
		return nil
	}
	gfVStoreMu.Lock()
	defer gfVStoreMu.Unlock()
	if st := gfVStores[dir]; st != nil {
		return st
	}
	st, err := gridfix.OpenVerifiedStore(dir)
	if err != nil {
		if u.log != nil {
			u.log.Warn("gridfix", "verified store unavailable", map[string]any{"err": err.Error()})
		}
		return nil
	}
	gfVStores[dir] = st
	return st
}

// ── rail rendering ──

// gfStageActive reports whether a beatgrid-fixer flow (confirm/running/done/cal) is open. When it
// is, the fixer owns the collection inspector even over a selected track - otherwise "Fix beatgrids"
// from the collection TOOLBAR while a track is open in the inspector set stage="confirm" but the
// confirm had nowhere to render (the inspector showed the track detail), so the click did nothing.
func (u *UI) gfStageActive() bool {
	g := &u.gf
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stage != ""
}

// gfRailState resolves the cockpit region of the Collection right rail. Pure renderer:
// libGFRailHTML (render_library_fixers.go) / native/zigui/src/libfixers.zig.
func (u *UI) gfRailState(s *libSt) libGFSt {
	g := &u.gf
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.stage {
	case "running":
		return u.gfRunningState(g)
	case "cal":
		return u.gfCalRunningState(g)
	case "done":
		return u.gfDoneState(g)
	case "confirm":
		return u.gfConfirmState(s)
	}
	// idle: health summary + entry point
	return u.gfHealthState(s)
}

// gfHealthState is the idle rail card: collection at a glance + the fixer entry.
func (u *UI) gfHealthState(s *libSt) libGFSt {
	total := len(s.tracks)
	verified := 0
	if vs := u.gfVerified(); vs != nil {
		verified = vs.Count()
	}
	noGrid, multi := 0, 0
	for _, t := range s.tracks {
		switch len(t.Beatgrid) {
		case 0:
			noGrid++
		case 1:
		default:
			multi++
		}
	}
	st := libGFSt{Kind: libGFHealth,
		Eyebrow: i18n.T("library.gf.healthEyebrow"), Title: i18n.T("library.gf.healthTitle"),
		Stats: []libGFStatSt{
			{N: fmt.Sprint(total), Label: i18n.T("library.gf.statTracks")},
			{N: fmt.Sprint(verified), Label: i18n.T("library.gf.statVerified"), Tone: "mint"},
			{N: fmt.Sprint(noGrid), Label: i18n.T("library.gf.statNoGrid"), Tone: "amber"},
			{N: fmt.Sprint(multi), Label: i18n.T("library.gf.statManual")},
		}}
	engineOK, probing := false, false
	if gst, ready := u.gridfixStatusCached(); ready {
		engineOK = gst.CPU.EngineOK || gst.CUDA.EngineOK
	} else {
		probing = true // env probe (spawns Python) hasn't landed yet - NOT the same as "not installed"
	}
	switch {
	case !u.svc.Cfg.Features.GridFix.Enabled:
		st.Note = i18n.T("library.gf.disabledHint")
	case probing:
		st.Note = i18n.T("library.gf.checking")
	case !engineOK:
		st.Note = i18n.T("library.gf.noEngineHint")
		st.Btns = []uiBtn{newBtn(i18n.T("library.gf.openSettings"), "outline", "gf-settings")}
	default:
		st.Btns = []uiBtn{newBtn(i18n.T("library.gf.start"), "primary", "gf-open")}
		if verified > 0 {
			st.Btns = append(st.Btns, newBtn(i18n.T("library.gf.calibrate"), "outline", "gf-cal"))
		}
		st.NoteAfter = true // the ready branch notes AFTER the buttons
		if bias := u.svc.Cfg.Features.GridFix.BiasExt; len(bias) > 0 {
			st.Note = i18n.T("library.gf.calBias", i18n.A{"vals": gfBiasSummary(bias)})
		} else if verified > 0 {
			st.Note = i18n.T("library.gf.calHint")
		}
	}
	return st
}

// gfBiasSummary formats a bias map for display: ".mp3 +42.7 ms · * −2.9 ms" ("*" last).
func gfBiasSummary(bias map[string]float64) string {
	exts := make([]string, 0, len(bias))
	for ext := range bias {
		if ext != "*" {
			exts = append(exts, ext)
		}
	}
	sort.Strings(exts)
	if _, ok := bias["*"]; ok {
		exts = append(exts, "*")
	}
	parts := make([]string, 0, len(exts))
	for _, ext := range exts {
		parts = append(parts, fmt.Sprintf("%s %+.1f ms", ext, bias[ext]*1000))
	}
	return strings.Join(parts, " · ")
}

// gfCalRunningState: live calibration progress (reuses prog + gf-cancel). Same chrome as a
// batch run, only the title + the tile-less live fragment differ.
func (u *UI) gfCalRunningState(g *gfState) libGFSt {
	p := g.prog
	frac := 0.0
	if p.Total > 0 {
		frac = float64(p.Done) / float64(p.Total)
	}
	return libGFSt{Kind: libGFRunning,
		Eyebrow: i18n.T("library.gf.eyebrow"), Title: i18n.T("library.gf.calibratingTitle"),
		Live:    gfCalLiveState(p, frac),
		StopLbl: i18n.T("library.gf.stop")}
}

// gfCalLiveState is the calibration #gf-live fragment (bar + current track, no tiles).
func gfCalLiveState(p gridfix.BatchProgress, frac float64) libGFLiveSt {
	return libGFLiveSt{Pct: progressPct(frac), Caption: fmt.Sprintf("%d / %d", p.Done, p.Total), Current: p.Current}
}

// gfConfirmState: pick scope + see what will happen before anything runs.
func (u *UI) gfConfirmState(s *libSt) libGFSt {
	filtered := len(s.collView())
	selected := len(s.collSel)
	st := libGFSt{Kind: libGFConfirm,
		Eyebrow: i18n.T("library.gf.eyebrow"), Title: i18n.T("library.gf.confirmTitle"),
		ConfirmNote: i18n.T("library.gf.confirmNote"),
		// force re-analyze: override the multi-marker/lock skips + the cache (verified stay protected)
		Force:     newToggle(i18n.T("library.gf.force"), "gf-force", u.gf.force),
		ForceHint: i18n.T("library.gf.forceHint")}
	scope := func(act, label string, n int, variant string) {
		if n == 0 {
			return
		}
		st.Scopes = append(st.Scopes, newBtn(label+" ("+fmt.Sprint(n)+")", variant, act))
	}
	scope("gf-run:all", i18n.T("library.gf.scopeAll"), len(s.tracks), "primary")
	scope("gf-run:filtered", i18n.T("library.gf.scopeFiltered"), filtered, "outline")
	scope("gf-run:selected", i18n.T("library.gf.scopeSelected"), selected, "outline")
	st.Scopes = append(st.Scopes, newBtn(i18n.T("common.cancel"), "ghost", "gf-close"))
	return st
}

// gfRunningState: the live cockpit - counter tiles + progress + current track.
func (u *UI) gfRunningState(g *gfState) libGFSt {
	p := g.prog
	frac := 0.0
	if p.Total > 0 {
		frac = float64(p.Done) / float64(p.Total)
	}
	eta := ""
	if p.ETA > 0 {
		eta = i18n.T("library.gf.eta", i18n.A{"eta": shortDur(p.ETA)})
	}
	return libGFSt{Kind: libGFRunning,
		Eyebrow: i18n.T("library.gf.eyebrow"), Title: i18n.T("library.gf.runningTitle"),
		Live:    gfLiveState(p, frac, eta),
		StopLbl: i18n.T("library.gf.stop")}
}

// gfLiveState is the per-tick patched #gf-live fragment (tiles + bar + current).
func gfLiveState(p gridfix.BatchProgress, frac float64, eta string) libGFLiveSt {
	return libGFLiveSt{Tiles: gfTilesState(p), Pct: progressPct(frac),
		Caption: fmt.Sprintf("%d / %d  %s", p.Done, p.Total, eta), Current: p.Current}
}

// gfTilesState is the four counter tiles (FIX / OK / MANUAL / ERR), numbers pre-formatted.
func gfTilesState(p gridfix.BatchProgress) []libGFTileSt {
	return []libGFTileSt{
		{N: fmt.Sprint(p.Fixed), Label: i18n.T("library.gf.tileFix"), Tone: "violet"},
		{N: fmt.Sprint(p.OK), Label: i18n.T("library.gf.tileOk"), Tone: "mint"},
		{N: fmt.Sprint(p.Skipped), Label: i18n.T("library.gf.tileManual"), Tone: "amber"},
		{N: fmt.Sprint(p.Failed), Label: i18n.T("library.gf.tileErr"), Tone: "red"},
	}
}

// gfDoneState: summary + the two write actions (Apply fixes / prep playlist). Hint ORDER is
// load-bearing (no-targets, per-target applied, apply error, prepped) - it is the byte order
// the rail renders them in.
func (u *UI) gfDoneState(g *gfState) libGFSt {
	p := g.prog
	title := i18n.T("library.gf.doneTitle")
	if p.Phase == gridfix.PhaseCancelled {
		title = i18n.T("library.gf.cancelledTitle")
	}
	st := libGFSt{Kind: libGFDone,
		Eyebrow: i18n.T("library.gf.eyebrow"), Title: title, Tiles: gfTilesState(p)}
	if p.Cached > 0 {
		st.CachedNote = i18n.T("library.gf.cachedNote", i18n.A{"n": fmt.Sprint(p.Cached)})
	}
	targets := u.gfTargets()
	if p.Fixed > 0 {
		if len(targets) == 0 {
			st.Hints = append(st.Hints, libHintSt{Tone: "bad", Text: i18n.T("library.gf.noTargets")})
		}
		variant := "primary"
		for _, t := range targets {
			if n, ok := g.applied[t.key]; ok {
				st.Hints = append(st.Hints, libHintSt{Tone: "ok",
					Text: i18n.T("library.gf.appliedToHint", i18n.A{"app": t.label, "n": fmt.Sprint(n)})})
				continue
			}
			if g.applyBusy {
				continue // one write at a time; rail re-renders when it lands
			}
			st.Acts = append(st.Acts, newBtn(i18n.T("library.gf.applyTo", i18n.A{"app": t.label, "n": fmt.Sprint(p.Fixed)}), variant, "gf-apply:"+t.key))
			variant = "outline"
			switch t.key {
			case "rekordbox":
				st.Notes = append(st.Notes, i18n.T("library.gf.rbNote"))
			case "serato":
				st.Notes = append(st.Notes, i18n.T("library.gf.seratoNote"))
			}
		}
	}
	if g.applyErr != "" {
		st.Hints = append(st.Hints, libHintSt{Tone: "bad", Text: g.applyErr})
	}
	hasTraktor := false
	for _, t := range targets {
		if t.key == "traktor" {
			hasTraktor = true
		}
	}
	if p.Skipped > 0 && g.prepped < 0 && hasTraktor {
		st.Acts = append(st.Acts, newBtn(i18n.T("library.gf.prep", i18n.A{"n": fmt.Sprint(p.Skipped)}), "outline", "gf-prep"))
	}
	if g.prepped >= 0 {
		st.Hints = append(st.Hints, libHintSt{Tone: "ok",
			Text: i18n.T("library.gf.preppedHint", i18n.A{"n": fmt.Sprint(g.prepped), "playlist": gridfixPrepPlaylist})})
	}
	resLbl := i18n.T("library.gf.viewResults")
	if g.resView {
		resLbl = i18n.T("library.gf.viewTracks")
	}
	st.Acts = append(st.Acts, newBtn(resLbl, "outline", "gf-results"), newBtn(i18n.T("common.close"), "ghost", "gf-close"))
	st.ApplyNote = i18n.T("library.gf.applyNote")
	return st
}

// gfResultsState resolves the batch outcome table that swaps the main track list.
func (u *UI) gfResultsState(g *gfState) libGFResSt {
	g.mu.Lock()
	results := g.results
	flt := g.resFlt
	g.mu.Unlock()
	st := libGFResSt{Empty: i18n.T("library.gf.noResults")}
	for _, f := range [][2]string{{"", i18n.T("library.gf.fltAll")}, {"FIX", i18n.T("library.gf.tileFix")},
		{"OK", i18n.T("library.gf.tileOk")}, {"SKIP", i18n.T("library.gf.tileManual")}, {"ERR", i18n.T("library.gf.tileErr")}} {
		st.Chips = append(st.Chips, newChip(f[1], "", "gf-flt:"+f[0], flt == f[0]))
	}
	shown := 0
	for _, r := range results {
		cls := gfResultClass(r)
		if flt != "" && cls != flt {
			continue
		}
		shown++
		if shown > libMaxRows {
			break
		}
		detail := r.Plan.Detail
		if r.Err != "" {
			detail = r.Err
		}
		delta := ""
		if r.Plan.Status == gridfix.StatusFix {
			if r.OldBPM > 0 && r.Plan.NewBPM != r.OldBPM {
				delta = fmt.Sprintf("%.2f → %.2f BPM", r.OldBPM, r.Plan.NewBPM)
			} else if !r.Plan.Created && r.Plan.OffsetMS == r.Plan.OffsetMS { // not NaN
				delta = fmt.Sprintf("%+.0f ms", r.Plan.OffsetMS)
			} else {
				delta = i18n.T("library.gf.newMarker")
			}
		}
		st.Rows = append(st.Rows, libGFResRowSt{Path: r.Path, St: cls, StLow: strings.ToLower(cls),
			Title: r.Title, Detail: detail, Delta: delta})
	}
	st.IsEmpty = shown == 0
	return st
}

func gfResultClass(r gridfix.TrackResult) string {
	if r.Err != "" {
		return "ERR"
	}
	return string(r.Plan.Status)
}

func shortDur(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func esc(s string) string { return html.EscapeString(s) }

// ── run lifecycle ──

// gfStart launches the batch over the chosen scope. force (the cockpit toggle) overrides the
// multi-marker/lock skips + the detection cache; verified grids stay protected.
func (u *UI) gfStart(scope string) {
	s := u.lib()
	s.mu.Lock()
	var tracks []musiclib.Track
	switch scope {
	case "selected":
		for _, t := range s.tracks {
			if s.collSel[t.Path] {
				tracks = append(tracks, t)
			}
		}
	case "filtered":
		for _, i := range s.collView() {
			tracks = append(tracks, s.tracks[i])
		}
	default:
		tracks = append(tracks, s.tracks...)
	}
	s.mu.Unlock()
	g := &u.gf
	g.mu.Lock()
	force := g.force
	g.mu.Unlock()
	u.gfRunTracks(tracks, scope, force)
}

// gfReanalyze force-re-analyzes ONE track (the right-click "Re-analyze beatgrid" in the collection
// browser / cue editor): overrides the skip checks + cache for that track (verified stays
// protected), routed through the cockpit so results + Apply surface exactly like a scoped run.
func (u *UI) gfReanalyze(path string) {
	s := u.lib()
	s.mu.Lock()
	t, ok := s.byPath[path]
	s.mu.Unlock()
	if !ok {
		u.toast(i18n.T("library.gf.nothingToDo"))
		return
	}
	u.gfRunTracks([]musiclib.Track{t}, "selected", true)
}

// gfCallTimeout bounds EVERY engine round-trip (model load AND per-track analyze). A child call that
// never replies - torch.load on a just-trained/incomplete .ckpt, a CUDA/cuDNN or decode hang -
// otherwise blocks its run goroutine forever, wedging the cockpit at stage="running"/"cal" until an
// app restart. Generous enough for a slow first base-model download + a large-file analyze; the
// cancel button unblocks earlier. Applied via gridfix.Engine.CallTimeout.
const gfCallTimeout = 5 * time.Minute

// gfTeardown is the deferred run-lifecycle teardown shared by gfRunTracks + gfCalibrate. It ALWAYS
// resolves the stage so a run can never stay wedged at "running"/"cal": recovers a panic (u.bg is
// unguarded - a run panic would otherwise crash the daemon), stops the engine, closes the cache, and
// - only while this run still owns the stage (ownStage; mutual exclusion via the start guards means
// no other run can have taken it) - sets the resolved stage (successStage on a clean finish, else ""
// idle) and clears the run handles. Returns the recovered panic value (nil = none) for the caller.
func (u *UI) gfTeardown(eng *gridfix.Engine, cache *gridfix.DetectionCache, ownStage, successStage string, ok bool) any {
	rec := recover()
	if eng != nil {
		eng.Stop()
	}
	if cache != nil {
		_ = cache.Close()
	}
	tray.SetTooltip("")
	g := &u.gf
	g.mu.Lock()
	if g.stage == ownStage { // still ours - never clobber a run that took over
		if ok && rec == nil {
			g.stage = successStage
		} else {
			g.stage = "" // warm-fail / cancel / crash → idle, retryable (no app restart)
		}
		g.cancel, g.cache, g.eng = nil, nil, nil
	}
	g.mu.Unlock()
	if rec != nil {
		u.toast(i18n.T("library.gf.runCrashed"))
	}
	return rec
}

// gfRunTracks runs the read-only batch over tracks. force overrides the multi-marker/lock skips
// AND the detection cache (fresh detection - e.g. after switching models); verified grids are
// always protected.
func (u *UI) gfRunTracks(tracks []musiclib.Track, scope string, force bool) {
	u.gfRunTracksHook(tracks, scope, force, nil)
}

// gfRunTracksHook additionally hands the results to onDone after a clean (non-cancelled)
// run - the folder-import flow saves created grids straight into libdb from it.
func (u *UI) gfRunTracksHook(tracks []musiclib.Track, scope string, force bool, onDone func([]gridfix.TrackResult)) {
	if len(tracks) == 0 {
		u.toast(i18n.T("library.gf.nothingToDo"))
		return
	}
	f := u.svc.Cfg.Features.GridFix
	mgr := u.gridfixEnvMgr()
	py, dev := u.gridfixEngine() // honors the engine preference (auto/cpu/cuda)
	if py == "" {
		u.toast(i18n.T("library.gf.noEngineHint"))
		return
	}
	dir := mgr.DataDir
	cache, err := gridfix.OpenDetectionCache(dir)
	if err != nil {
		u.toast(i18n.T("library.gf.cacheErr") + err.Error())
		return
	}
	ffmpeg := ""
	if p, ok := mediatools.Resolve("ffmpeg"); ok {
		ffmpeg = p
	}
	eng := &gridfix.Engine{Python: py, DataDir: dir, Device: dev,
		Checkpoint: f.ActiveModel, FFmpeg: ffmpeg, CallTimeout: gfCallTimeout,
		OnLog: func(line string) {
			if u.log != nil {
				u.log.Debug("gridfix", line, nil)
			}
		}}
	batch := gridfix.NewBatch(eng, cache, gridfix.BatchOptions{
		MinQuality: f.ResolvedMinQuality(), ThresholdMS: f.ResolvedThresholdMS(),
		BiasS: f.BiasS, Bias: gridfix.Calibration(f.BiasExt), Checkpoint: f.ActiveModel, Force: force})
	vs := u.gfVerified()
	rules, _ := u.svc.Lib.LoadBPMRules() // nil-DB safe; empty = no ranges
	bts := make([]gridfix.BatchTrack, 0, len(tracks))
	for _, t := range tracks {
		bt := gridfix.BatchTrack{Path: t.Path, Title: trackTitle(t), OldBPM: t.BPM,
			MultiMarker: len(t.Beatgrid) > 1,
			Verified:    vs != nil && vs.Has(t.Path)}
		if r, ok := rules.Resolve(t.Path, t.Genre); ok {
			bt.RangeLo, bt.RangeHi = r.Min, r.Max
		}
		if len(t.Beatgrid) == 1 {
			ms := t.Beatgrid[0].PositionMs
			bt.OldStartMs = &ms
		}
		bts = append(bts, bt)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &u.gf
	g.mu.Lock()
	if g.stage == "running" || g.stage == "cal" { // reject while a run of ANY kind is active
		g.mu.Unlock()
		cancel()
		_ = cache.Close()
		u.toast(i18n.T("library.gf.alreadyRunning")) // feedback instead of a silent no-op
		return
	}
	g.stage, g.scope = "running", scope
	g.prog = gridfix.BatchProgress{Phase: gridfix.PhaseScanning, Total: len(bts)}
	g.results, g.applied, g.applyErr, g.prepped, g.resView, g.resFlt = nil, nil, "", -1, false, ""
	g.applyBusy = false
	g.cancel, g.cache, g.eng = cancel, cache, eng
	g.mu.Unlock()
	u.patchMain()
	u.bg(func() {
		done := false
		defer func() {
			rec := u.gfTeardown(eng, cache, "running", "done", done)
			g.mu.Lock()
			p := g.prog
			g.mu.Unlock()
			u.patchMain()
			// don't announce completion for a run the user aborted (batch.Run returns cleanly on cancel)
			if done && rec == nil && p.Phase != gridfix.PhaseCancelled {
				u.Notify(i18n.T("library.gf.doneNotifyTitle"),
					i18n.T("library.gf.doneNotifyBody", i18n.A{"fix": fmt.Sprint(p.Fixed), "ok": fmt.Sprint(p.OK), "manual": fmt.Sprint(p.Skipped)}))
			}
		}()
		defer cancel()

		// Warm + validate the (possibly just-switched) checkpoint FIRST so a hung/bad .ckpt fails
		// fast (ONE attempt) instead of re-hanging per track. Only when the batch will actually
		// analyze - a fully-cached / all-skipped re-run stays instant, never paying a model load.
		// Predicate mirrors batch.Run's skip ORDER exactly: Verified always skipped (even in force),
		// then force analyzes, then Locked/MultiMarker skipped, then a checkpoint-matched cache hit.
		warm := false
		for i := 0; !warm && i < len(bts); i++ {
			switch {
			case bts[i].Verified:
			case force:
				warm = true
			case bts[i].MultiMarker || bts[i].Locked:
			default:
				if _, hit := cache.Get(bts[i].Path, f.ActiveModel); !hit {
					warm = true
				}
			}
		}
		if warm {
			g.mu.Lock() // a distinct "loading model" state - a motionless 0/N reads like the wedge
			g.prog.Phase, g.prog.Current = gridfix.PhaseScanning, i18n.T("library.gf.loadingModel")
			pr := g.prog
			g.mu.Unlock()
			u.eval("window.__patch('gf-live'," + jsQuote(gfLiveRender(gfLiveState(pr, 0, ""))) + ")")
			if _, _, werr := eng.Ping(ctx, true); werr != nil { // gfCallTimeout bounds it internally
				if ctx.Err() == nil { // a real load failure / timeout, not a user cancel
					if errors.Is(werr, context.DeadlineExceeded) {
						u.toast(i18n.T("library.gf.modelLoadTimeout"))
					} else {
						u.toast(i18n.T("library.gf.modelLoadFailed") + werr.Error())
					}
				}
				return // teardown resets stage → idle, retryable (no app restart)
			}
		}

		var lastPatch, lastTip time.Time
		results, autoBias := batch.RunAutoBias(ctx, bts, func(p gridfix.BatchProgress) {
			g.mu.Lock()
			g.prog = p
			g.mu.Unlock()
			now := time.Now()
			if now.Sub(lastPatch) > 500*time.Millisecond {
				lastPatch = now
				frac := 0.0
				if p.Total > 0 {
					frac = float64(p.Done) / float64(p.Total)
				}
				eta := ""
				if p.ETA > 0 {
					eta = i18n.T("library.gf.eta", i18n.A{"eta": shortDur(p.ETA)})
				}
				u.eval("window.__patch('gf-live'," + jsQuote(gfLiveRender(gfLiveState(p, frac, eta))) + ")")
			}
			if now.Sub(lastTip) > 2*time.Second {
				lastTip = now
				tray.SetTooltip(fmt.Sprintf("rave-mate — %s %d/%d · FIX %d · OK %d",
					i18n.T("library.gf.trayLabel"), p.Done, p.Total, p.Fixed, p.OK))
			}
		})
		if autoBias.Applied && u.log != nil {
			u.log.Info("gridfix", fmt.Sprintf("auto-bias: %.1fms systematic detector offset measured over %d tracks - corrections re-planned",
				autoBias.MedianMS, autoBias.Samples), nil)
		}
		g.mu.Lock()
		g.results = results
		cancelled := g.prog.Phase == gridfix.PhaseCancelled
		g.mu.Unlock()
		done = true
		if onDone != nil && !cancelled {
			onDone(results)
		}
	})
}

// gfCalTarget is the default calibration sample size (Python --calibrate 60).
const gfCalTarget = 60

// gfCalibrate measures the detector's systematic phase bias against verified
// grids (per file extension, stride-sampled) and stores it in config — the
// rave-mate mirror of fix_grids --calibrate (which used locked grids).
func (u *UI) gfCalibrate() {
	vs := u.gfVerified()
	if vs == nil {
		return
	}
	byExt := map[string][]gridfix.VerifiedGrid{}
	for _, v := range vs.All() {
		if v.BPM > 0 {
			byExt[strings.ToLower(filepath.Ext(v.Path))] = append(byExt[strings.ToLower(filepath.Ext(v.Path))], v)
		}
	}
	if len(byExt) == 0 {
		u.toast(i18n.T("library.gf.calFailedToast"))
		return
	}
	quota := gridfix.CalibrationQuota(len(byExt), gfCalTarget)
	var sample []gridfix.VerifiedGrid
	exts := make([]string, 0, len(byExt))
	for ext := range byExt {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	for _, ext := range exts {
		v := byExt[ext]
		for _, i := range gridfix.StrideIndices(len(v), quota) {
			sample = append(sample, v[i])
		}
	}
	mgr := u.gridfixEnvMgr()
	py, dev := u.gridfixEngine()
	if py == "" {
		u.toast(i18n.T("library.gf.noEngineHint"))
		return
	}
	cache, err := gridfix.OpenDetectionCache(mgr.DataDir)
	if err != nil {
		u.toast(i18n.T("library.gf.cacheErr") + err.Error())
		return
	}
	ffmpeg := ""
	if p, ok := mediatools.Resolve("ffmpeg"); ok {
		ffmpeg = p
	}
	f := u.svc.Cfg.Features.GridFix
	eng := &gridfix.Engine{Python: py, DataDir: mgr.DataDir, Device: dev,
		Checkpoint: f.ActiveModel, FFmpeg: ffmpeg, CallTimeout: gfCallTimeout,
		OnLog: func(line string) {
			if u.log != nil {
				u.log.Debug("gridfix", line, nil)
			}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	g := &u.gf
	g.mu.Lock()
	if g.stage != "" { // only from idle
		g.mu.Unlock()
		cancel()
		_ = cache.Close()
		return
	}
	g.stage = "cal"
	g.prog = gridfix.BatchProgress{Total: len(sample)}
	g.cancel, g.cache, g.eng = cancel, cache, eng
	g.mu.Unlock()
	u.patchMain()
	u.bg(func() {
		ok := false
		defer func() {
			u.gfTeardown(eng, cache, "cal", "", ok) // calibrate always resolves to idle
			u.patchMain()
		}()
		defer cancel()

		// Warm + validate the checkpoint once (fail-fast) before the sample loop - same rationale as
		// gfRunTracks; calibrate always analyzes, so warm whenever any sample is a cache miss.
		warmC := false
		for i := 0; !warmC && i < len(sample); i++ {
			if _, hit := cache.Get(sample[i].Path, f.ActiveModel); !hit {
				warmC = true
			}
		}
		if warmC {
			if _, _, werr := eng.Ping(ctx, true); werr != nil {
				if ctx.Err() == nil {
					if errors.Is(werr, context.DeadlineExceeded) {
						u.toast(i18n.T("library.gf.modelLoadTimeout"))
					} else {
						u.toast(i18n.T("library.gf.modelLoadFailed") + werr.Error())
					}
				}
				return // teardown resets stage → idle, retryable
			}
		}

		offsets := map[string][]float64{}
		var lastPatch time.Time
		cancelled := false
		for _, v := range sample {
			if ctx.Err() != nil {
				cancelled = true
				break
			}
			det, hit := cache.Get(v.Path, f.ActiveModel)
			if !hit {
				d, err := eng.Analyze(ctx, v.Path)
				if err != nil {
					if ctx.Err() != nil {
						cancelled = true
						break
					}
					if u.log != nil {
						u.log.Warn("gridfix", "calibrate analyze failed", map[string]any{"path": v.Path, "err": err.Error()})
					}
					continue
				}
				det = d
				_ = cache.Put(v.Path, det, f.ActiveModel) // cache persist failure is non-fatal
			}
			if off, ok := gridfix.CalibrationOffset(det.Beats, det.Downbeats, v.BPM, v.StartMs/1000.0); ok {
				ext := strings.ToLower(filepath.Ext(v.Path))
				offsets[ext] = append(offsets[ext], off)
			}
			g.mu.Lock()
			g.prog.Done++
			g.prog.Current = filepath.Base(v.Path)
			p := g.prog
			g.mu.Unlock()
			if time.Since(lastPatch) > 500*time.Millisecond {
				lastPatch = time.Now()
				frac := float64(p.Done) / float64(p.Total)
				u.eval("window.__patch('gf-live'," + jsQuote(gfLiveRender(gfCalLiveState(p, frac))) + ")")
			}
		}
		if cancelled {
			return // teardown resets stage → idle
		}
		bias, _ := gridfix.SummarizeCalibration(offsets)
		if len(bias) == 0 {
			u.toast(i18n.T("library.gf.calFailedToast"))
		} else {
			u.svc.Cfg.Features.GridFix.BiasExt = map[string]float64(bias)
			u.saveCfg()
			u.toast(i18n.T("library.gf.calDoneToast", i18n.A{"vals": gfBiasSummary(bias)}))
		}
		ok = true
	})
}

// gfTarget is one detected DJ-software write destination (cuewriteback.Target, webui view).
type gfTarget struct {
	key   string // "traktor" | "rekordbox" | "virtualdj" | "serato"
	label string // product name (not translated)
	path  string // file (NML/XML) or _Serato_ dir the write hits
}

// gfTargets detects which DJ libraries exist on this machine (cuewriteback probes).
func (u *UI) gfTargets() []gfTarget {
	var out []gfTarget
	for _, t := range cuewriteback.DetectTargets(strings.TrimSpace(u.svc.Cfg.Features.NML.CollectionPath)) {
		out = append(out, gfTarget{t.Key, t.Label, t.Path})
	}
	return out
}

// gfApply routes the FIX plans into the chosen software's library (backup first).
// Traktor additionally prunes applied tracks from the prep playlist; Traktor +
// Rekordbox re-import afterwards so the collection view reflects the new grids.
func (u *UI) gfApply(sw string) {
	g := &u.gf
	g.mu.Lock()
	if g.stage != "done" || g.applyBusy {
		g.mu.Unlock()
		return
	}
	if _, done := g.applied[sw]; done {
		g.mu.Unlock()
		return
	}
	var fixes []musiclib.GridFixUpdate
	var fixedPaths []string
	lock := u.svc.Cfg.Features.GridFix.LockFixed
	for _, r := range g.results {
		if r.Err == "" && r.Plan.Status == gridfix.StatusFix {
			fixes = append(fixes, musiclib.GridFixUpdate{Path: r.Path,
				BPM: r.Plan.NewBPM, StartMs: r.Plan.NewStartS * 1000, Lock: lock})
			fixedPaths = append(fixedPaths, r.Path)
		}
	}
	if len(fixes) == 0 {
		g.mu.Unlock()
		return
	}
	var target *gfTarget
	for _, t := range u.gfTargets() {
		if t.key == sw {
			tc := t
			target = &tc
			break
		}
	}
	if target == nil {
		g.mu.Unlock()
		u.gfApplyFail(i18n.T("library.gf.noNml"))
		return
	}
	g.applyBusy = true
	g.mu.Unlock()
	u.patchMain()
	u.bg(func() {
		res, err := u.gfApplyTo(*target, fixes, fixedPaths)
		g.mu.Lock()
		g.applyBusy = false
		g.mu.Unlock()
		if err != nil {
			u.gfApplyFail(err.Error())
			return
		}
		g.mu.Lock()
		if g.applied == nil {
			g.applied = map[string]int{}
		}
		g.applied[sw] = res.Updated
		g.mu.Unlock()
		u.toast(i18n.T("library.gf.appliedToast", i18n.A{"n": fmt.Sprint(res.Updated)}))
		switch sw {
		case "traktor", "rekordbox":
			u.libImport(sw) // re-import so the collection view reflects the new grids
		}
		u.patchMain()
	})
}

// gfApplyTo performs the per-software write (called off the action goroutine).
func (u *UI) gfApplyTo(t gfTarget, fixes []musiclib.GridFixUpdate, fixedPaths []string) (musiclib.WritebackResult, error) {
	var zero musiclib.WritebackResult
	switch t.key {
	case "traktor":
		// Traktor holds collection.nml in memory and rewrites it on save/exit - a live
		// write silently vanishes. Refuse early with a clear message instead.
		if cuewriteback.TraktorRunning() {
			return zero, errors.New(i18n.T("library.gf.traktorRunning"))
		}
		// safety: full collection backup before the write
		if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 && installs[0].Collection != "" {
			if _, berr := musiclib.BackupCollection(installs[0], libBackupRoot()); berr != nil {
				return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), berr.Error())
			}
		} else if err := gfBackupFile("traktor", t.path); err != nil {
			return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), err.Error())
		}
		res, err := musiclib.ApplyGridFixes(t.path, fixes)
		if err != nil {
			return zero, gfWriteErr(err)
		}
		// fixed tracks no longer need manual gridding
		_, _ = musiclib.RemoveFromNMLPlaylist(t.path, gridfixPrepPlaylist, fixedPaths)
		return res, nil
	case "rekordbox":
		if set, ok := sysactivity.New().RunningProcesses(); ok && sysactivity.Running(set, "rekordbox") {
			return zero, errors.New(i18n.T("library.gf.rekordboxRunning"))
		}
		if err := gfBackupFile("rekordbox", t.path); err != nil {
			return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), err.Error())
		}
		res, err := musiclib.ApplyGridFixesRekordboxXML(t.path, fixes)
		if err != nil {
			return zero, gfWriteErr(err)
		}
		return res, nil
	case "virtualdj":
		// VDJ rewrites database.xml from memory on exit - a live write would be clobbered.
		if set, ok := sysactivity.New().RunningProcesses(); ok && sysactivity.RunningPrefix(set, "virtualdj") {
			return zero, fmt.Errorf("%s", i18n.T("library.gf.vdjRunning"))
		}
		if err := gfBackupFile("virtualdj", t.path); err != nil {
			return zero, fmt.Errorf("%s%s", i18n.T("library.gf.backupFailed"), err.Error())
		}
		return musiclib.ApplyGridFixesVirtualDJ(t.path, fixes)
	case "serato":
		// per-file temp+verify+rename with its own Serato-running refusal; no library backup needed
		return seratolib.ApplyGridFixesSerato(t.path, fixes)
	}
	return zero, fmt.Errorf("unknown apply target %q", t.key)
}

// gfWriteErr turns a raw OS permission error from a library write into an actionable
// message (locked / read-only file) instead of the cryptic "permission denied" the
// atomic rename surfaces when the DJ software still holds the file (or it's read-only).
func gfWriteErr(err error) error {
	if errors.Is(err, os.ErrPermission) {
		return errors.New(i18n.T("library.gf.writeDenied"))
	}
	return err
}

// gfBackupFile copies a library file into the backup root before a write.
func gfBackupFile(app, path string) error {
	return cuewriteback.BackupFile(libBackupRoot(), app, path)
}

func (u *UI) gfApplyFail(msg string) {
	g := &u.gf
	g.mu.Lock()
	g.applyErr = msg
	g.mu.Unlock()
	u.toast(msg)
	u.patchMain()
}

// gfPrep collects SKIP tracks into the manual-gridding prep playlist.
func (u *UI) gfPrep() {
	g := &u.gf
	g.mu.Lock()
	if g.stage != "done" || g.prepped >= 0 {
		g.mu.Unlock()
		return
	}
	var paths []string
	for _, r := range g.results {
		if r.Err == "" && r.Plan.Status == gridfix.StatusSkip {
			paths = append(paths, r.Path)
		}
	}
	g.mu.Unlock()
	nml := u.gfNMLPath()
	if nml == "" {
		u.toast(i18n.T("library.gf.noNml"))
		return
	}
	if len(paths) == 0 {
		return
	}
	u.bg(func() {
		added, err := musiclib.UpsertNMLPlaylist(nml, gridfixPrepPlaylist, paths)
		if err != nil {
			u.toast(i18n.T("library.gf.prepFailed") + err.Error())
			return
		}
		g.mu.Lock()
		g.prepped = added
		g.mu.Unlock()
		u.toast(i18n.T("library.gf.preppedToast", i18n.A{"n": fmt.Sprint(added), "playlist": gridfixPrepPlaylist}))
		u.patchMain()
	})
}

// gfNMLPath resolves the Traktor collection file the write actions target.
func (u *UI) gfNMLPath() string {
	if p := strings.TrimSpace(u.svc.Cfg.Features.NML.CollectionPath); p != "" {
		return p
	}
	if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 {
		return installs[0].Collection
	}
	return ""
}

// ── actions ──

func init() {
	onExact("gf-open", func(u *UI, _ actMsg) { u.gfSetStage("confirm") })
	onExact("gf-close", func(u *UI, _ actMsg) {
		g := &u.gf
		g.mu.Lock()
		if g.stage == "running" || g.stage == "cal" { // live runs end via gf-cancel
			g.mu.Unlock()
			return
		}
		g.stage, g.resView = "", false
		g.mu.Unlock()
		u.patchMain()
	})
	onPrefix("gf-run:", func(u *UI, m actMsg) { u.gfStart(m.arg("gf-run:")) })
	onExact("gf-force", func(u *UI, m actMsg) { // cockpit toggle; carries the new bool
		g := &u.gf
		g.mu.Lock()
		g.force = m.Val == "true"
		g.mu.Unlock()
	})
	onPrefix("gf-reanalyze:", func(u *UI, m actMsg) { // right-click: force re-analyze one track
		u.closeModal()
		u.gfReanalyze(m.arg("gf-reanalyze:"))
	})
	onExact("gf-cal", func(u *UI, _ actMsg) { u.gfCalibrate() })
	onExact("gf-cancel", func(u *UI, _ actMsg) {
		g := &u.gf
		g.mu.Lock()
		c := g.cancel
		g.mu.Unlock()
		if c != nil {
			c()
		}
	})
	onPrefix("gf-apply:", func(u *UI, m actMsg) { u.gfApply(m.arg("gf-apply:")) })
	onExact("gf-prep", func(u *UI, _ actMsg) { u.gfPrep() })
	onExact("gf-results", func(u *UI, _ actMsg) {
		g := &u.gf
		g.mu.Lock()
		g.resView = !g.resView
		g.mu.Unlock()
		u.patchMain()
	})
	onPrefix("gf-flt:", func(u *UI, m actMsg) {
		g := &u.gf
		g.mu.Lock()
		g.resFlt = m.arg("gf-flt:")
		g.mu.Unlock()
		u.patchMain()
	})
	onExact("gf-settings", func(u *UI, _ actMsg) {
		u.setMu.Lock()
		u.setSec, u.setQuery = "libmedia", ""
		u.setMu.Unlock()
		u.setTab("settings")
	})
	// verified-grid marking: single track (from the details rail) + bulk (selection bar)
	onPrefix("gf-verify:", func(u *UI, m actMsg) { u.gfToggleVerify([]string{m.arg("gf-verify:")}) })
	onExact("gf-verify-sel", func(u *UI, _ actMsg) {
		s := u.lib()
		s.mu.Lock()
		paths := make([]string, 0, len(s.collSel))
		for p := range s.collSel {
			paths = append(paths, p)
		}
		s.mu.Unlock()
		sort.Strings(paths)
		u.gfToggleVerify(paths)
	})
}

func (u *UI) gfSetStage(st string) {
	g := &u.gf
	g.mu.Lock()
	if g.stage == "running" || g.stage == "cal" { // never clobber a live run
		g.mu.Unlock()
		return
	}
	g.stage = st
	g.mu.Unlock()
	u.patchMain()
}

// gfToggleVerify marks paths verified (or unmarks when ALL are already marked),
// capturing BPM + marker from the imported collection at marking time.
func (u *UI) gfToggleVerify(paths []string) {
	vs := u.gfVerified()
	if vs == nil || len(paths) == 0 {
		return
	}
	s := u.lib()
	s.mu.Lock()
	byPath := s.byPath
	s.mu.Unlock()
	all := true
	for _, p := range paths {
		if !vs.Has(p) {
			all = false
			break
		}
	}
	n := 0
	for _, p := range paths {
		if all {
			if vs.Unmark(p) == nil {
				n++
			}
			continue
		}
		t, ok := byPath[p]
		if !ok || t.BPM <= 0 || len(t.Beatgrid) != 1 {
			continue // verified means ONE trusted marker + a BPM
		}
		if vs.Mark(p, t.BPM, t.Beatgrid[0].PositionMs) == nil {
			n++
		}
	}
	if all {
		u.toast(i18n.T("library.gf.unverifiedToast", i18n.A{"n": fmt.Sprint(n)}))
	} else {
		u.toast(i18n.T("library.gf.verifiedToast", i18n.A{"n": fmt.Sprint(n)}))
	}
	u.patchMain()
}
