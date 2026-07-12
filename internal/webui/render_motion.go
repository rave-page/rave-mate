package webui

// Motion tab - webview port of the Fyne VR tools the rewrite missed (parity gap,
// user-reported 2026-07-06): camera-path browser (view_campaths.go) + motion studio
// (view_motion.go). The camera-path preview is the shared campathview.go component
// (also hosted by the VRChat tab). The skeleton preview renders as Go-built SVG on
// the shared orbitCam. Motion playback streams OSC/VMC from a daemon goroutine
// (the real playback path); the preview scrubs exact frames and plays smoothly via
// SMIL values-list animation (moSkeletonAnim) - the live tick only updates the clock.
// VRM mesh preview stays with C5 (subprocess render) - stick figure here.

import (
	"fmt"
	"html"
	"path/filepath"
	"sort"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/motionrender"
	"rave.page/mate/internal/vrmotion"
)

func (u *UI) renderMotion() string {
	s := u.mo()
	s.mu.Lock()
	sec := s.section
	s.mu.Unlock()
	var b strings.Builder
	b.WriteString(`<h1 class=page-title>` + html.EscapeString(i18n.T("motion.title")) + `</h1><p class=page-sub>` +
		html.EscapeString(i18n.T("motion.subtitle")) + `</p>`)
	b.WriteString(`<div class=subtabs>` +
		subtabBtn("campaths", i18n.T("motion.tabCamPaths"), sec) + subtabBtn("studio", i18n.T("motion.tabStudio"), sec) + `</div>`)
	b.WriteString(`<div id=mo-body>` + u.moBody() + `</div>`)
	return b.String()
}

func subtabBtn(id, label, cur string) string {
	cls := "subtab"
	if id == cur {
		cls += " active"
	}
	return `<button class="` + cls + `" data-act="mo-section:` + id + `">` + html.EscapeString(label) + `</button>`
}

func (u *UI) moBody() string {
	s := u.mo()
	s.mu.Lock()
	sec := s.section
	s.mu.Unlock()
	if sec == "studio" {
		return u.moStudioHTML()
	}
	return u.moCamPathsHTML()
}

// ── camera paths ─────────────────────────────────────────────────────────────

func (u *UI) moCamPathsHTML() string {
	if u.svc.VRCTools == nil {
		return emptyState(i18n.T("motion.vrchatUnavailable"))
	}
	s := u.mo()
	s.mu.Lock()
	paths, sel := s.cpPaths, s.cpSel
	s.mu.Unlock()

	var list strings.Builder
	list.WriteString(`<div class=mo-list>`)
	lastFolder := ""
	for i, p := range paths {
		folder := p.Folder()
		if folder != lastFolder {
			list.WriteString(`<div class=mo-group>` + html.EscapeString(folder) + `</div>`)
			lastFolder = folder
		}
		cls := "irow"
		if i == sel {
			cls += " selected"
		}
		list.WriteString(`<div class="` + cls + `" data-act="mo-cp-sel:` + fmt.Sprintf("%d", i) + `"><div class=irow-main>` +
			`<div class=irow-title>` + html.EscapeString(p.Name) + `</div>` +
			`<div class=irow-sub>` + html.EscapeString(i18n.T("motion.camPathMeta", i18n.A{
			"count": fmt.Sprint(p.Points), "duration": fmt.Sprintf("%.1f", p.DurationSec), "saved": p.SavedAt.Format("2006-01-02 15:04"),
		})) + `</div>` +
			`</div></div>`)
	}
	if len(paths) == 0 {
		list.WriteString(emptyState(i18n.T("motion.noCamPaths")))
	}
	list.WriteString(`</div>`)
	list.WriteString(btnRow(
		btn(i18n.T("motion.reloadList"), "ghost", "mo-cp-refresh", ""),
		btn(i18n.T("motion.organizeNow"), "outline", "mo-cp-organize", ""),
		btn(i18n.T("motion.installDjPaths"), "outline", "mo-cp-dj", "")))

	file := ""
	if sel >= 0 && sel < len(paths) {
		file = paths[sel].File
	}
	u.cpvEnsure("mo", file)
	detail := u.cpvView("mo") +
		`<div class=mo-hint>` + html.EscapeString(i18n.T("campath.hint")) + `</div>` +
		`<div id=mo-cp-info class=mo-info>` + u.moCamPathInfo() + `</div>` +
		btnRow(
			u.cpvPlayBtn("mo"),
			btn(i18n.T("motion.loadIntoVrchat"), "primary", "mo-cp-load", ""),
			btn(i18n.T("motion.copyFilePath"), "outline", "mo-cp-copy", ""))
	head := `<div class=card-label>` + html.EscapeString(i18n.T("motion.preview")) + tipTopic("camera-paths") + `</div>`
	return masterDetail(list.String(), head+detail)
}

func (u *UI) moCamPathInfo() string {
	s := u.mo()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cpSel < 0 || s.cpSel >= len(s.cpPaths) {
		return html.EscapeString(i18n.T("motion.selectPath"))
	}
	p := s.cpPaths[s.cpSel]
	where := p.WorldName
	if p.Local {
		where = i18n.T("motion.playerRelative")
	} else if where == "" {
		where = i18n.T("motion.unknownWorld")
	}
	return html.EscapeString(i18n.T("motion.camPathDetail", i18n.A{
		"name": p.Name, "where": where, "count": fmt.Sprint(p.Points), "duration": fmt.Sprintf("%.1f", p.DurationSec),
	}))
}

// ── motion studio ────────────────────────────────────────────────────────────

func (u *UI) moStudioHTML() string {
	s := u.mo()
	s.mu.Lock()
	names, recName := s.recNames, s.recName
	playing, loop, oscOn, vmcOn := s.playing, s.loop, s.oscOn, s.vmcOn
	modelOn := s.modelOn && s.model != nil
	physOn, hasDyn := s.physOn, s.dyn != nil && len(s.dyn.Chains()) > 0
	restPose, marks := s.restPose, s.marks
	pcOn, pcColor, pcDensity := s.pcOn, s.pcColor, s.pcDensity
	t, dur := s.t, 0.0
	if s.player != nil {
		dur = s.player.Duration()
	}
	s.mu.Unlock()

	var list strings.Builder
	list.WriteString(`<div class=mo-list>`)
	for _, n := range names {
		cls := "irow"
		if n == recName {
			cls += " selected"
		}
		list.WriteString(`<div class="` + cls + `" data-act="mo-rec-sel:` + html.EscapeString(n) + `"><div class=irow-main><div class=irow-title>` +
			html.EscapeString(n) + `</div></div></div>`)
	}
	if len(names) == 0 {
		list.WriteString(emptyState(i18n.T("motion.noRecordings")))
	}
	list.WriteString(`</div>`)
	list.WriteString(btnRow(
		btn(i18n.T("common.refresh"), "ghost", "mo-rec-refresh", ""),
		btn(i18n.T("motion.exportAnim"), "outline", "pick-save:anim:mo-export", ""),
		btn(i18n.T("motion.renderVideo"), "outline", "mo-render", "")))
	list.WriteString(`<div id=mo-render-prog>` + u.moRenderProgHTML() + `</div>`)
	list.WriteString(u.moAvatarHTML())

	playLbl := "▶ " + i18n.T("player.play")
	if playing {
		playLbl = "⏸ " + i18n.T("player.pause")
	}
	detail := `<div id=mo-view data-actpos="mo-orbit" data-actwheel="mo-zoom">` + u.moViewHTML() + `</div>` +
		`<div class=mo-hint>` + html.EscapeString(i18n.T("motion.studioHint")) + `</div>` +
		`<div id=mo-time class=mo-info>` + html.EscapeString(i18n.T("motion.timeDisplay", i18n.A{"cur": fmt.Sprintf("%.1f", t), "dur": fmt.Sprintf("%.1f", dur)})) + `</div>` +
		slider(i18n.T("motion.scrub"), "mo-scrub", 0, 1000, 1, scrubVal(t, dur), "") +
		btnRow(btn(playLbl, "go", "mo-play", ""), btn("⏹ "+i18n.T("player.stop"), "outline", "mo-stop", "")) +
		`<div class=mo-toggles>` +
		toggleRow(i18n.T("motion.loop"), "mo-loop", loop) +
		toggleRow(i18n.T("motion.oscTrackers"), "mo-osc", oscOn) +
		toggleRow(i18n.T("motion.streamVmc"), "mo-vmc", vmcOn) +
		toggleRow(i18n.T("motion.showAvatarModel"), "mo-model", modelOn) +
		moPhysRow(modelOn, physOn, hasDyn) +
		moCompareRows(modelOn, restPose, marks) +
		moPointCloudRows(modelOn, pcOn, pcColor, pcDensity) +
		`</div>` +
		`<p class=page-sub>` + html.EscapeString(i18n.T("motion.vmcHelp", i18n.A{"addr": u.svc.Cfg.Features.VROverlay.ResolvedVMCAddr()})) + `</p>`
	head := `<div class=card-label>` + html.EscapeString(i18n.T("motion.preview")) + tipTopic("motion-studio") + `</div>`
	return masterDetail(list.String(), head+detail)
}

// moPhysRow: avatar-physics toggle, shown only with the model on. Chain source:
// <avatar>.physbones.json sidecar (exported from Unity - real PhysBone/DynamicBone
// params) when present, otherwise name-heuristic detection (hair/tail/ears/…).
func moPhysRow(modelOn, physOn, hasDyn bool) string {
	if !modelOn {
		return ""
	}
	lbl := i18n.T("motion.avatarPhysics")
	if !hasDyn {
		return `<div class=mo-info>` + i18n.T("motion.noPhysBones") + `</div>`
	}
	return toggleRow(lbl, "mo-phys", physOn)
}

// moCompareRows: pose-debug toggles, shown only with the model on. Rest pose renders the
// mesh at its authored A/T reference (the take's tracker points still draw, so retarget
// alignment is inspectable); the marker overlay draws the raw take points over the posed mesh.
func moCompareRows(modelOn, restPose, marks bool) string {
	if !modelOn {
		return ""
	}
	return toggleRow(i18n.T("motion.restPose"), "mo-rest", restPose) +
		toggleRow(i18n.T("motion.overlayTrackerPoints"), "mo-marks", marks)
}

// moPointCloudRows: point-cloud preview toggle + (when on) export density / colour / .rmpc
// export. Shown only with the model on (the cloud IS the posed mesh's surface).
func moPointCloudRows(modelOn, pcOn, pcColor bool, density string) string {
	if !modelOn {
		return ""
	}
	out := toggleRow(i18n.T("motion.pointCloud"), "mo-pc", pcOn)
	if !pcOn {
		return out
	}
	if density == "" {
		density = "med"
	}
	out += smartSelect("mo-pc-density", i18n.T("motion.pcDensity"), "mo-pc-density:", density, func() []ssOpt {
		return []ssOpt{
			{Val: "low", Label: i18n.T("motion.pcLow"), Sub: i18n.T("motion.pcLowSub")},
			{Val: "med", Label: i18n.T("motion.pcMed"), Sub: i18n.T("motion.pcMedSub")},
			{Val: "high", Label: i18n.T("motion.pcHigh"), Sub: i18n.T("motion.pcHighSub")},
			{Val: "ultra", Label: i18n.T("motion.pcUltra"), Sub: i18n.T("motion.pcUltraSub")},
		}
	}) +
		toggleRow(i18n.T("motion.pcColor"), "mo-pc-color", pcColor) +
		`<div class=mo-info>` + html.EscapeString(i18n.T("motion.pcNote")) + `</div>` +
		btnRow(btn(i18n.T("motion.pcExport"), "primary", "pick-save:rmpc:mo-pc-export", ""))
	return out
}

func scrubVal(t, dur float64) float64 {
	if dur <= 0 {
		return 0
	}
	return 1000 * t / dur
}

// moAvatarHTML: active VRM + peer-synced avatar management (mesh preview lands with C5).
func (u *UI) moAvatarHTML() string {
	cur := u.svc.Cfg.Features.VRCTools.AvatarVRM
	curLbl := i18n.T("motion.noneLabel")
	if cur != "" {
		curLbl = filepath.Base(cur)
	}
	opts := func() []ssOpt {
		var out []ssOpt
		for _, e := range config.ListAvatars() {
			out = append(out, ssOpt{Val: e.Path, Label: e.Name, Sub: humanBytes(uint64(e.Size))})
		}
		return out
	}
	body := smartSelect("mo-avatar", i18n.T("motion.activeAvatar"), "mo-avatar-set", cur, opts) +
		btnRow(btn(i18n.T("motion.importAvatar"), "outline", "pick-file:mo-avatar-import", ""),
			btn(i18n.T("motion.syncNow"), "ghost", "mo-avatar-sync", "")) +
		`<div class=mo-info>` + html.EscapeString(i18n.T("motion.avatarCurrentInfo", i18n.A{"name": curLbl})) + `</div>`
	return `<div class=mo-avatars><div class=card-label>` + html.EscapeString(i18n.T("motion.avatarLabel")) + `</div>` + body + `</div>`
}

// moSkeletonSVG: floor grid + head trail + skeleton (head dot, bones head→trackers) at s.t.
// While playing, joints + bones carry SMIL values-list animations sampled from the whole
// take - the browser interpolates between samples, so playback is smooth with zero bridge
// traffic (same trick as the camera-path marker). The live tick then only updates the
// time label; re-rendering mo-view mid-play would reset the SMIL clock.
// moPrevW/H: preview raster size (SVG box + streaming frame stream).
const moPrevW, moPrevH = 640, 400

// moStaticW/H: 2x native raster for the paused/scrub avatar-mesh frame, served crisp over
// the cached loopback endpoint (browser downscales - kills the upscale blur). The live
// stream (moRunPreview) stays at moPrevW/H for playback throughput.
const moStaticW, moStaticH = 1280, 800

func (u *UI) moSkeletonSVG() string { return u.moSkeletonSVGOpt(false) }

// moSkeletonSVGOpt: drag=true renders the cheap mid-drag frame - static skeleton at
// the current time (no SMIL values-lists: rebuilding them per pointermove cost ~8ms
// Go + ~450KB innerHTML per event) and, with the model on, a preview-res raster
// (moPrevW/H, not the 2x static). The full view re-renders once on pointer release.
func (u *UI) moSkeletonSVGOpt(drag bool) string {
	const w, h = moPrevW, moPrevH
	s := u.mo()
	s.mu.Lock()
	rec, cam, name := s.rec, s.cam, s.recName
	player, t0, loop := s.player, s.t, s.loop
	animate := s.playing && s.player != nil && !drag
	model := s.model
	modelOn := s.modelOn && model != nil
	dyn, rt := s.dyn, s.rt
	restPose, marks := s.restPose, s.marks
	if !s.physOn || animate || s.playing {
		dyn = nil // while playing moRunPreview is the sole Stepper (no race on State)
	}
	s.mu.Unlock()

	// Avatar-mesh mode: CPU raster → image inside the SVG. Paused/scrub renders one
	// static PNG; while playing this same frame seeds the view and moRunPreview streams
	// JPEG frames onto the <image> href (~15fps, no innerHTML - nothing resets).
	if modelOn && rec != nil {
		var sample map[int]vrmotion.Pose
		if player != nil {
			sample = player.Sample(t0)
		}
		var trail [][3]float32
		for _, fr := range rec.Frames {
			if p, ok := fr.Poses[0]; ok {
				trail = append(trail, p.Pos)
			}
		}
		frameSample, markSample := sample, map[int]vrmotion.Pose(nil)
		if restPose {
			frameSample = nil // A/T rest reference
		}
		if marks || restPose {
			markSample = sample
		}
		rw, rh := moStaticW, moStaticH
		if drag {
			rw, rh = moPrevW, moPrevH // 4x cheaper raster per drag frame
		}
		fr := motionrender.Frame{
			W: rw, H: rh,
			Cam: motionrender.Camera{Yaw: cam.yaw, Pitch: cam.pitch, Dist: cam.dist,
				Center: cam.center, FloorY: cam.floorY, GridR: cam.gridR},
			Model: model, Sample: frameSample, Trail: trail, Name: name,
			Dyn: dyn, RT: rt, Marks: markSample, // DT 0: paused frames keep the settled chain pose
		}
		// Render at 2x + serve over the cached loopback endpoint: crisp source, browser-cached
		// by URL (no base64 re-ship per patch). SVG box stays moPrevW/H user-units; the browser
		// downscales the 2x source. Falls back to an inline data-URI if the loopback is down.
		if src := u.imgBytesURL(jpegBytes(motionrender.Render(fr), 82)); src != "" {
			return fmt.Sprintf(`<svg class=mo-svg viewBox="0 0 %d %d" preserveAspectRatio="xMidYMid meet">`+
				`<image width="%d" height="%d" href="%s"/></svg>`, moPrevW, moPrevH, moPrevW, moPrevH, html.EscapeString(src))
		}
		b64 := motionrender.PNGBase64(fr)
		return fmt.Sprintf(`<svg class=mo-svg viewBox="0 0 %d %d" preserveAspectRatio="xMidYMid meet">`+
			`<image width="%d" height="%d" href="data:image/png;base64,%s"/></svg>`, moPrevW, moPrevH, moPrevW, moPrevH, b64)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class=mo-svg viewBox="0 0 %d %d" preserveAspectRatio="xMidYMid meet">`, w, h)
	b.WriteString(`<rect width="100%" height="100%" fill="rgba(0,0,0,.25)"/>`)
	if rec == nil {
		b.WriteString(`<text x="20" y="200" class=mo-svgtext>` + html.EscapeString(i18n.T("motion.selectRecordingPreview")) + `</text></svg>`)
		return b.String()
	}
	b.WriteString(cam.gridSVG(w, h))
	var trail [][2]float32
	for _, fr := range rec.Frames {
		if p, ok := fr.Poses[0]; ok {
			x, y := cam.project(p.Pos, w, h)
			trail = append(trail, [2]float32{x, y})
		}
	}
	for i := 1; i < len(trail); i++ {
		b.WriteString(svgLine(trail[i-1][0], trail[i-1][1], trail[i][0], trail[i][1], "rgba(124,58,237,.5)", 1))
	}
	if animate {
		b.WriteString(moSkeletonAnim(player, cam, w, h, loop))
	} else if player != nil {
		var head struct{ x, y float32 }
		var headOK bool
		sm := player.Sample(t0)
		if p, ok := sm[0]; ok {
			head.x, head.y = cam.project(p.Pos, w, h)
			headOK = true
		}
		for k, p := range sm {
			x, y := cam.project(p.Pos, w, h)
			if k == 0 {
				b.WriteString(svgDisc(x, y, 6, "var(--rp-base,#F70864)"))
				continue
			}
			if headOK {
				b.WriteString(svgLine(head.x, head.y, x, y, "rgba(230,232,238,.35)", 1))
			}
			b.WriteString(svgDisc(x, y, 4, "var(--rp-mint,#08F79B)"))
		}
	}
	if name != "" {
		b.WriteString(`<text x="12" y="388" class=mo-svgtext>` + html.EscapeString(name) + `</text>`)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// SMIL sampling: 15/s keeps values lists small; takes past the cap sample sparser
// (the browser interpolates the gaps either way).
const (
	moAnimRate       = 15.0
	moAnimMaxSamples = 900
)

// moSkeletonAnim emits the playing skeleton with per-joint cx/cy (and per-bone endpoint)
// values-list animations over the full take. Inline-SVG SMIL clocks are rooted at page
// load, not insertion - the caller MUST re-seat the phase via moSyncAnimClock
// (svg.setCurrentTime) after every patch, or a non-loop take lands already-expired and
// freezes. Loop repeats indefinitely; non-loop freezes on the last sample (the Go
// playback goroutine re-renders the static view when it finishes).
func moSkeletonAnim(player *vrmotion.Player, cam orbitCam, w, h float32, loop bool) string {
	dur := player.Duration()
	if dur <= 0 {
		return ""
	}
	n := int(dur*moAnimRate) + 1
	n = max(min(n, moAnimMaxSamples), 2)

	// pass 1: sample the grid; union of joint keys (trackers can drop in/out mid-take)
	samples := make([]map[int]vrmotion.Pose, n)
	var keys []int
	seen := map[int]bool{}
	for i := range n {
		samples[i] = player.Sample(dur * float64(i) / float64(n-1))
		for k := range samples[i] {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Ints(keys)

	// pass 2: project per-key tracks; a missing step holds the last (or first) known spot
	type track struct{ xs, ys []string }
	tracks := map[int]*track{}
	for _, k := range keys {
		tr := &track{xs: make([]string, n), ys: make([]string, n)}
		var lx, ly string
		for i := range n {
			if p, ok := samples[i][k]; ok {
				x, y := cam.project(p.Pos, w, h)
				lx, ly = fmt.Sprintf("%.1f", x), fmt.Sprintf("%.1f", y)
			}
			tr.xs[i], tr.ys[i] = lx, ly
		}
		for i := n - 1; i >= 0; i-- { // backfill a leading gap from the first known spot
			if tr.xs[i] == "" {
				tr.xs[i], tr.ys[i] = lx, ly
			} else {
				lx, ly = tr.xs[i], tr.ys[i]
			}
		}
		tracks[k] = tr
	}

	anim := func(attr string, vals []string) string {
		rep := `repeatCount="indefinite"`
		if !loop {
			rep = `repeatCount="1" fill="freeze"`
		}
		return fmt.Sprintf(`<animate attributeName="%s" values="%s" dur="%.2fs" calcMode="linear" %s/>`,
			attr, strings.Join(vals, ";"), dur, rep)
	}

	var b strings.Builder
	head := tracks[0]
	for _, k := range keys {
		if k == 0 {
			continue
		}
		tr := tracks[k]
		if head != nil { // bone head→tracker
			fmt.Fprintf(&b, `<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="rgba(230,232,238,.35)" stroke-width="1">`,
				head.xs[0], head.ys[0], tr.xs[0], tr.ys[0])
			b.WriteString(anim("x1", head.xs) + anim("y1", head.ys) + anim("x2", tr.xs) + anim("y2", tr.ys) + `</line>`)
		}
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="4" fill="var(--rp-mint,#08F79B)">`, tr.xs[0], tr.ys[0])
		b.WriteString(anim("cx", tr.xs) + anim("cy", tr.ys) + `</circle>`)
	}
	if head != nil {
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="6" fill="var(--rp-base,#F70864)">`, head.xs[0], head.ys[0])
		b.WriteString(anim("cx", head.xs) + anim("cy", head.ys) + `</circle>`)
	}
	return b.String()
}
