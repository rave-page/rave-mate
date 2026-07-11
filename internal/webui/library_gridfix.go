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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gridfix"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/serato"
	"rave.page/mate/internal/seratolib"
	"rave.page/mate/internal/sysactivity"
	"rave.page/mate/internal/tray"
)

const gridfixPrepPlaylist = "MANUAL_GRIDDING_PREP"

type gfState struct {
	mu        sync.Mutex
	stage     string // "" idle | "confirm" | "running" | "done"
	scope     string // "all" | "filtered" | "selected"
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

// gfVerified lazily opens the verified-grid store (nil on error - marking disabled).
func (u *UI) gfVerified() *gridfix.VerifiedStore {
	u.gfVMu.Lock()
	defer u.gfVMu.Unlock()
	if u.gfVStore != nil {
		return u.gfVStore
	}
	dir, err := config.DataPath("gridfix")
	if err != nil {
		return nil
	}
	st, err := gridfix.OpenVerifiedStore(dir)
	if err != nil {
		if u.log != nil {
			u.log.Warn("gridfix", "verified store unavailable", map[string]any{"err": err.Error()})
		}
		return nil
	}
	u.gfVStore = st
	return st
}

// ── rail rendering ──

// gfRailHTML renders the cockpit region of the Collection right rail.
func (u *UI) gfRailHTML(s *libSt) string {
	g := &u.gf
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.stage {
	case "running":
		return u.gfRunningHTML(g)
	case "cal":
		return u.gfCalRunningHTML(g)
	case "done":
		return u.gfDoneHTML(g)
	case "confirm":
		return u.gfConfirmHTML(s)
	}
	// idle: health summary + entry point
	return u.gfHealthHTML(s)
}

// gfHealthHTML is the idle rail card: collection at a glance + the fixer entry.
func (u *UI) gfHealthHTML(s *libSt) string {
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
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.gf.healthEyebrow")) + `</div><div class=insp-title>` +
		esc(i18n.T("library.gf.healthTitle")) + `</div></div>`)
	b.WriteString(`<div class=gf-stats>` +
		gfStat(fmt.Sprint(total), i18n.T("library.gf.statTracks"), "") +
		gfStat(fmt.Sprint(verified), i18n.T("library.gf.statVerified"), "mint") +
		gfStat(fmt.Sprint(noGrid), i18n.T("library.gf.statNoGrid"), "amber") +
		gfStat(fmt.Sprint(multi), i18n.T("library.gf.statManual"), "") + `</div>`)
	engineOK := false
	if st, ready := u.gridfixStatusCached(); ready && (st.CPU.EngineOK || st.CUDA.EngineOK) {
		engineOK = true
	}
	if !u.svc.Cfg.Features.GridFix.Enabled {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.gf.disabledHint")) + `</div>`)
	} else if !engineOK {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.gf.noEngineHint")) + `</div>` +
			btnRow(btn(i18n.T("library.gf.openSettings"), "outline", "gf-settings", "")))
	} else {
		buttons := btn(i18n.T("library.gf.start"), "primary", "gf-open", "")
		if verified > 0 {
			buttons += btn(i18n.T("library.gf.calibrate"), "outline", "gf-cal", "")
		}
		b.WriteString(btnRow(buttons))
		if bias := u.svc.Cfg.Features.GridFix.BiasExt; len(bias) > 0 {
			b.WriteString(`<div class=set-note>` + esc(i18n.T("library.gf.calBias", i18n.A{"vals": gfBiasSummary(bias)})) + `</div>`)
		} else if verified > 0 {
			b.WriteString(`<div class=set-note>` + esc(i18n.T("library.gf.calHint")) + `</div>`)
		}
	}
	return b.String()
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

// gfCalRunningHTML: live calibration progress (reuses prog + gf-cancel).
func (u *UI) gfCalRunningHTML(g *gfState) string {
	p := g.prog
	frac := 0.0
	if p.Total > 0 {
		frac = float64(p.Done) / float64(p.Total)
	}
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.gf.eyebrow")) + `</div><div class=insp-title>` +
		esc(i18n.T("library.gf.calibratingTitle")) + `</div></div>`)
	b.WriteString(`<div id=gf-live>` + progressBar(frac, fmt.Sprintf("%d / %d", p.Done, p.Total)) +
		`<div class=gf-current>` + esc(p.Current) + `</div></div>`)
	b.WriteString(btnRow(btn(i18n.T("library.gf.stop"), "outline", "gf-cancel", "")))
	return b.String()
}

// gfConfirmHTML: pick scope + see what will happen before anything runs.
func (u *UI) gfConfirmHTML(s *libSt) string {
	filtered := len(s.collView())
	selected := len(s.collSel)
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.gf.eyebrow")) + `</div><div class=insp-title>` +
		esc(i18n.T("library.gf.confirmTitle")) + `</div></div>`)
	b.WriteString(`<div class=set-note>` + esc(i18n.T("library.gf.confirmNote")) + `</div>`)
	row := func(act, label string, n int, variant string) string {
		if n == 0 {
			return ""
		}
		return btn(label+" ("+fmt.Sprint(n)+")", variant, act, "")
	}
	b.WriteString(`<div class=btn-col>` +
		row("gf-run:all", i18n.T("library.gf.scopeAll"), len(s.tracks), "primary") +
		row("gf-run:filtered", i18n.T("library.gf.scopeFiltered"), filtered, "outline") +
		row("gf-run:selected", i18n.T("library.gf.scopeSelected"), selected, "outline") +
		btn(i18n.T("common.cancel"), "ghost", "gf-close", "") + `</div>`)
	return b.String()
}

// gfRunningHTML: the live cockpit - counter tiles + progress + current track.
func (u *UI) gfRunningHTML(g *gfState) string {
	p := g.prog
	frac := 0.0
	if p.Total > 0 {
		frac = float64(p.Done) / float64(p.Total)
	}
	eta := ""
	if p.ETA > 0 {
		eta = i18n.T("library.gf.eta", i18n.A{"eta": shortDur(p.ETA)})
	}
	var b strings.Builder
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.gf.eyebrow")) + `</div><div class=insp-title>` +
		esc(i18n.T("library.gf.runningTitle")) + `</div></div>`)
	b.WriteString(`<div id=gf-live>` + gfLiveInner(p, frac, eta) + `</div>`)
	b.WriteString(btnRow(btn(i18n.T("library.gf.stop"), "outline", "gf-cancel", "")))
	return b.String()
}

// gfLiveInner is the per-tick patched fragment (tiles + bar + current).
func gfLiveInner(p gridfix.BatchProgress, frac float64, eta string) string {
	return `<div class=gf-tiles>` +
		gfTile(p.Fixed, i18n.T("library.gf.tileFix"), "violet") +
		gfTile(p.OK, i18n.T("library.gf.tileOk"), "mint") +
		gfTile(p.Skipped, i18n.T("library.gf.tileManual"), "amber") +
		gfTile(p.Failed, i18n.T("library.gf.tileErr"), "red") + `</div>` +
		progressBar(frac, fmt.Sprintf("%d / %d  %s", p.Done, p.Total, eta)) +
		`<div class=gf-current>` + esc(p.Current) + `</div>`
}

// gfDoneHTML: summary + the two write actions (Apply fixes / prep playlist).
func (u *UI) gfDoneHTML(g *gfState) string {
	p := g.prog
	var b strings.Builder
	title := i18n.T("library.gf.doneTitle")
	if p.Phase == gridfix.PhaseCancelled {
		title = i18n.T("library.gf.cancelledTitle")
	}
	b.WriteString(`<div class=insp-hd><div class=insp-eyebrow>` + esc(i18n.T("library.gf.eyebrow")) + `</div><div class=insp-title>` +
		esc(title) + `</div></div>`)
	b.WriteString(`<div class=gf-tiles>` +
		gfTile(p.Fixed, i18n.T("library.gf.tileFix"), "violet") +
		gfTile(p.OK, i18n.T("library.gf.tileOk"), "mint") +
		gfTile(p.Skipped, i18n.T("library.gf.tileManual"), "amber") +
		gfTile(p.Failed, i18n.T("library.gf.tileErr"), "red") + `</div>`)
	if p.Cached > 0 {
		b.WriteString(`<div class=set-note>` + esc(i18n.T("library.gf.cachedNote", i18n.A{"n": fmt.Sprint(p.Cached)})) + `</div>`)
	}
	var acts []string
	var notes []string
	targets := u.gfTargets()
	if p.Fixed > 0 {
		if len(targets) == 0 {
			b.WriteString(hint("bad", i18n.T("library.gf.noTargets")))
		}
		variant := "primary"
		for _, t := range targets {
			if n, ok := g.applied[t.key]; ok {
				b.WriteString(hint("ok", i18n.T("library.gf.appliedToHint", i18n.A{"app": t.label, "n": fmt.Sprint(n)})))
				continue
			}
			if g.applyBusy {
				continue // one write at a time; rail re-renders when it lands
			}
			acts = append(acts, btn(i18n.T("library.gf.applyTo", i18n.A{"app": t.label, "n": fmt.Sprint(p.Fixed)}), variant, "gf-apply:"+t.key, ""))
			variant = "outline"
			switch t.key {
			case "rekordbox":
				notes = append(notes, i18n.T("library.gf.rbNote"))
			case "serato":
				notes = append(notes, i18n.T("library.gf.seratoNote"))
			}
		}
	}
	if g.applyErr != "" {
		b.WriteString(hint("bad", g.applyErr))
	}
	hasTraktor := false
	for _, t := range targets {
		if t.key == "traktor" {
			hasTraktor = true
		}
	}
	if p.Skipped > 0 && g.prepped < 0 && hasTraktor {
		acts = append(acts, btn(i18n.T("library.gf.prep", i18n.A{"n": fmt.Sprint(p.Skipped)}), "outline", "gf-prep", ""))
	}
	if g.prepped >= 0 {
		b.WriteString(hint("ok", i18n.T("library.gf.preppedHint", i18n.A{"n": fmt.Sprint(g.prepped), "playlist": gridfixPrepPlaylist})))
	}
	resLbl := i18n.T("library.gf.viewResults")
	if g.resView {
		resLbl = i18n.T("library.gf.viewTracks")
	}
	acts = append(acts, btn(resLbl, "outline", "gf-results", ""), btn(i18n.T("common.close"), "ghost", "gf-close", ""))
	b.WriteString(`<div class=btn-col>` + strings.Join(acts, "") + `</div>`)
	for _, n := range notes {
		b.WriteString(`<div class=set-note>` + esc(n) + `</div>`)
	}
	b.WriteString(`<div class=set-note>` + esc(i18n.T("library.gf.applyNote")) + `</div>`)
	return b.String()
}

// gfResultsHTML swaps the main track list for the batch outcome table.
func (u *UI) gfResultsHTML(g *gfState) string {
	g.mu.Lock()
	results := g.results
	flt := g.resFlt
	g.mu.Unlock()
	var b strings.Builder
	b.WriteString(`<div class=lib-toolbar>`)
	for _, f := range [][2]string{{"", i18n.T("library.gf.fltAll")}, {"FIX", i18n.T("library.gf.tileFix")},
		{"OK", i18n.T("library.gf.tileOk")}, {"SKIP", i18n.T("library.gf.tileManual")}, {"ERR", i18n.T("library.gf.tileErr")}} {
		b.WriteString(fchip(f[1], "", "gf-flt:"+f[0], flt == f[0]))
	}
	b.WriteString(`</div><div class=trk-table>`)
	shown := 0
	for _, r := range results {
		st := gfResultClass(r)
		if flt != "" && st != flt {
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
		b.WriteString(`<div class=trk-row data-ctx="lib-ctx:` + esc(r.Path) + `">` +
			`<span class="gf-chip gf-` + strings.ToLower(st) + `">` + st + `</span>` +
			`<span class=trk-main data-act="lib-track:` + esc(r.Path) + `"><span class=trk-title>` + esc(r.Title) + `</span>` +
			`<span class=trk-sub>` + esc(detail) + `</span></span>` +
			`<span class=gf-delta>` + esc(delta) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	if shown == 0 {
		b.WriteString(emptyState(i18n.T("library.gf.noResults")))
	}
	return b.String()
}

func gfResultClass(r gridfix.TrackResult) string {
	if r.Err != "" {
		return "ERR"
	}
	return string(r.Plan.Status)
}

func gfStat(n, label, tone string) string {
	cls := "gf-stat"
	if tone != "" {
		cls += " gf-" + tone
	}
	return `<div class="` + cls + `"><div class=gf-n>` + esc(n) + `</div><div class=gf-l>` + esc(label) + `</div></div>`
}

func gfTile(n int, label, tone string) string {
	return `<div class="gf-tile gf-` + tone + `"><div class=gf-n>` + fmt.Sprint(n) + `</div><div class=gf-l>` + esc(label) + `</div></div>`
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

// gfStart launches the batch over the chosen scope. Engine + cache live for the run.
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
		Checkpoint: f.ActiveModel, FFmpeg: ffmpeg,
		OnLog: func(line string) {
			if u.log != nil {
				u.log.Debug("gridfix", line, nil)
			}
		}}
	batch := gridfix.NewBatch(eng, cache, gridfix.BatchOptions{
		MinQuality: f.ResolvedMinQuality(), ThresholdMS: f.ResolvedThresholdMS(),
		BiasS: f.BiasS, Bias: gridfix.Calibration(f.BiasExt), Checkpoint: f.ActiveModel})
	bts := make([]gridfix.BatchTrack, 0, len(tracks))
	for _, t := range tracks {
		bt := gridfix.BatchTrack{Path: t.Path, Title: trackTitle(t), OldBPM: t.BPM,
			MultiMarker: len(t.Beatgrid) > 1}
		if len(t.Beatgrid) == 1 {
			ms := t.Beatgrid[0].PositionMs
			bt.OldStartMs = &ms
		}
		bts = append(bts, bt)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &u.gf
	g.mu.Lock()
	if g.stage == "running" {
		g.mu.Unlock()
		cancel()
		_ = cache.Close()
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
		defer cancel()
		var lastPatch, lastTip time.Time
		results := batch.Run(ctx, bts, func(p gridfix.BatchProgress) {
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
				u.eval("window.__patch('gf-live'," + jsQuote(gfLiveInner(p, frac, eta)) + ")")
			}
			if now.Sub(lastTip) > 2*time.Second {
				lastTip = now
				tray.SetTooltip(fmt.Sprintf("rave-mate — %s %d/%d · FIX %d · OK %d",
					i18n.T("library.gf.trayLabel"), p.Done, p.Total, p.Fixed, p.OK))
			}
		})
		eng.Stop()
		_ = cache.Close()
		tray.SetTooltip("")
		g.mu.Lock()
		g.results = results
		g.stage = "done"
		g.cancel, g.cache, g.eng = nil, nil, nil
		p := g.prog
		g.mu.Unlock()
		u.Notify(i18n.T("library.gf.doneNotifyTitle"),
			i18n.T("library.gf.doneNotifyBody", i18n.A{"fix": fmt.Sprint(p.Fixed), "ok": fmt.Sprint(p.OK), "manual": fmt.Sprint(p.Skipped)}))
		u.patchMain()
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
		Checkpoint: f.ActiveModel, FFmpeg: ffmpeg,
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
		defer cancel()
		offsets := map[string][]float64{}
		var lastPatch time.Time
		cancelled := false
		for _, v := range sample {
			if ctx.Err() != nil {
				cancelled = true
				break
			}
			det, hit := cache.Get(v.Path)
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
				u.eval("window.__patch('gf-live'," + jsQuote(progressBar(frac, fmt.Sprintf("%d / %d", p.Done, p.Total))+
					`<div class=gf-current>`+esc(p.Current)+`</div>`) + ")")
			}
		}
		eng.Stop()
		_ = cache.Close()
		g.mu.Lock()
		g.stage = ""
		g.cancel, g.cache, g.eng = nil, nil, nil
		g.mu.Unlock()
		if !cancelled {
			bias, _ := gridfix.SummarizeCalibration(offsets)
			if len(bias) == 0 {
				u.toast(i18n.T("library.gf.calFailedToast"))
			} else {
				u.svc.Cfg.Features.GridFix.BiasExt = map[string]float64(bias)
				u.saveCfg()
				u.toast(i18n.T("library.gf.calDoneToast", i18n.A{"vals": gfBiasSummary(bias)}))
			}
		}
		u.patchMain()
	})
}

// gfTarget is one detected DJ-software write destination.
type gfTarget struct {
	key   string // "traktor" | "rekordbox" | "virtualdj" | "serato"
	label string // product name (not translated)
	path  string // file (NML/XML) or _Serato_ dir the write hits
}

// gfTargets detects which DJ libraries exist on this machine (cheap fs probes).
func (u *UI) gfTargets() []gfTarget {
	var out []gfTarget
	if p := u.gfNMLPath(); p != "" {
		out = append(out, gfTarget{"traktor", "Traktor", p})
	}
	if installs, err := musiclib.DiscoverRekordbox(); err == nil && len(installs) > 0 {
		out = append(out, gfTarget{"rekordbox", "Rekordbox", installs[0].XML})
	}
	if p, err := musiclib.DiscoverVirtualDJ(); err == nil && p != "" {
		out = append(out, gfTarget{"virtualdj", "VirtualDJ", p})
	}
	if dirs := serato.DetectSeratoDirs(); len(dirs) > 0 {
		out = append(out, gfTarget{"serato", "Serato", dirs[0]})
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
		// Traktor holds collection.nml open (and rewrites it on exit); a live write
		// fails the atomic rename with a cryptic OS "permission denied". Refuse early
		// with a clear message instead.
		if set, ok := sysactivity.New().RunningProcesses(); ok && sysactivity.Running(set, "traktor") {
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
		if set, ok := sysactivity.New().RunningProcesses(); ok && sysactivity.Running(set, "virtualdj") {
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
	root := libBackupRoot()
	if root == "" {
		return fmt.Errorf("no backup dir")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	name := fmt.Sprintf("%s-%s-%s", app, time.Now().Format("20060102-150405"), filepath.Base(path))
	dst, err := os.Create(filepath.Join(root, name))
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
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
