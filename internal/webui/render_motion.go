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
	"os"
	"path/filepath"
	"sort"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/motionrender"
	"rave.page/mate/internal/vrmotion"
	"rave.page/mate/internal/zigui"
)

// Motion is a Zig-rendered tab (native/zigui/src/motion.zig): Go resolves everything
// impure into moState - data + i18n + the pre-rendered preview fragments (camera-path
// viewer, skeleton/mesh SVG, render progress, tooltips) the renderers embed verbatim -
// and the Zig lib renders HTML byte-identical to the Go renderers below (fallback +
// golden reference, zigui_golden_motion_test.go).

// moToggleSt is one resolved toggleRow (DL = Go strings.ToLower(Label): Unicode
// lowercasing stays Go-side).
type moToggleSt struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Act   string `json:"act"`
	On    bool   `json:"on"`
}

// moTog resolves a toggleRow's render state.
func moTog(label, act string, on bool) moToggleSt {
	return moToggleSt{Label: label, DL: strings.ToLower(label), Act: act, On: on}
}

// moSliderSt is a resolved slider. Numbers ride BOTH as floats (the Go path feeds the
// shared slider() unchanged) and pre-formatted via trimNum (the Zig path never formats
// a float), so the golden gate catches any drift between the two.
type moSliderSt struct {
	Label  string  `json:"label"`
	DL     string  `json:"dl"`
	Act    string  `json:"act"`
	Unit   string  `json:"unit"`
	UnitJS string  `json:"unitJs"` // jsQuote(Unit) - the oninput display splice
	Min    float64 `json:"-"`
	Max    float64 `json:"-"`
	Step   float64 `json:"-"`
	Val    float64 `json:"-"`
	MinS   string  `json:"minS"`
	MaxS   string  `json:"maxS"`
	StepS  string  `json:"stepS"`
	ValS   string  `json:"valS"`
}

// moSlide resolves a slider's render state (both number representations).
func moSlide(label, act string, minV, maxV, step, val float64, unit string) moSliderSt {
	return moSliderSt{
		Label: label, DL: strings.ToLower(label), Act: act, Unit: unit, UnitJS: jsQuote(unit),
		Min: minV, Max: maxV, Step: step, Val: val,
		MinS: trimNum(minV), MaxS: trimNum(maxV), StepS: trimNum(step), ValS: trimNum(val),
	}
}

// moCamRow is one camera-path list row (ShowGroup = this row opens a new folder group).
type moCamRow struct {
	Group     string `json:"group"`
	ShowGroup bool   `json:"showGroup"`
	Act       string `json:"act"` // "mo-cp-sel:<index>"
	Sel       bool   `json:"sel"`
	Name      string `json:"name"`
	Meta      string `json:"meta"` // resolved motion.camPathMeta
}

// moCamSt is the camera-paths section's render state.
type moCamSt struct {
	Unavailable string     `json:"unavailable"` // non-empty → only the emptyState renders
	Rows        []moCamRow `json:"rows"`
	Empty       string     `json:"empty"`
	ReloadLbl   string     `json:"reloadLbl"`
	OrganizeLbl string     `json:"organizeLbl"`
	DJLbl       string     `json:"djLbl"`
	PreviewLbl  string     `json:"previewLbl"`
	Tip         string     `json:"tip"`             // legacy RAW tooltip markup (bridge)
	TipS        *tipSt     `json:"tipSt,omitempty"` // structured tipTopic("camera-paths")
	View        string     `json:"view"`            // raw cpvView("mo")
	Hint        string     `json:"hint"`
	Info        string     `json:"info"`    // plain text; renderers escape
	PlayBtn     string     `json:"playBtn"` // raw cpvPlayBtn("mo")
	LoadLbl     string     `json:"loadLbl"`
	CopyLbl     string     `json:"copyLbl"`
}

// moAvatarSt is the avatar-management block's render state.
type moAvatarSt struct {
	Label     string   `json:"label"`
	Sel       selState `json:"sel"`
	ImportLbl string   `json:"importLbl"`
	SyncLbl   string   `json:"syncLbl"`
	Info      string   `json:"info"`
}

// moRecRow is one motion-recording list row.
type moRecRow struct {
	Name string `json:"name"`
	Act  string `json:"act"` // "mo-rec-sel:<name>"
	Sel  bool   `json:"sel"`
}

// moStudioSt is the motion-studio section's render state.
type moStudioSt struct {
	Recs        []moRecRow `json:"recs"`
	Empty       string     `json:"empty"`
	RefreshLbl  string     `json:"refreshLbl"`
	ExportLbl   string     `json:"exportLbl"`
	RenderLbl   string     `json:"renderLbl"`
	PCViewLbl   string     `json:"pcViewLbl"`
	RenderProg  string     `json:"renderProg"` // raw moRenderProgHTML
	Avatar      moAvatarSt `json:"avatar"`
	PreviewLbl  string     `json:"previewLbl"`
	Tip         string     `json:"tip"`             // legacy RAW tooltip markup (bridge)
	TipS        *tipSt     `json:"tipSt,omitempty"` // structured tipTopic("motion-studio")
	View        string     `json:"view"`            // raw moViewHTML (SVG / raster frame)
	Hint        string     `json:"hint"`
	Time        string     `json:"time"` // plain text; renderers escape
	Scrub       moSliderSt `json:"scrub"`
	PlayLbl     string     `json:"playLbl"`
	StopLbl     string     `json:"stopLbl"`
	Loop        moToggleSt `json:"loop"`
	OSC         moToggleSt `json:"osc"`
	VMC         moToggleSt `json:"vmc"`
	Model       moToggleSt `json:"model"`
	ModelOn     bool       `json:"modelOn"` // gates every model-only row below
	HasDyn      bool       `json:"hasDyn"`
	PhysNote    string     `json:"physNote"` // RAW (the Go original emits it unescaped)
	Phys        moToggleSt `json:"phys"`
	Rest        moToggleSt `json:"rest"`
	Marks       moToggleSt `json:"marks"`
	PC          moToggleSt `json:"pc"`
	PCOn        bool       `json:"pcOn"`
	PCDensity   selState   `json:"pcDensity"`
	PCColor     moToggleSt `json:"pcColor"`
	PCNote      string     `json:"pcNote"`
	PCExportLbl string     `json:"pcExportLbl"`
	VMCHelp     string     `json:"vmcHelp"`
}

// moState is the resolved render state for the Motion view (JSON → Zig). Exactly one
// section state is built per render (the inactive one stays nil/null).
type moState struct {
	Title     string      `json:"title"`
	Sub       string      `json:"sub"`
	Section   string      `json:"section"` // "campaths" | "studio"
	TabCam    string      `json:"tabCam"`
	TabStudio string      `json:"tabStudio"`
	Cam       *moCamSt    `json:"cam"`
	Studio    *moStudioSt `json:"studio"`
}

// moState resolves the active section + i18n into render state.
func (u *UI) moState() moState {
	s := u.mo()
	s.mu.Lock()
	sec := s.section
	s.mu.Unlock()
	st := moState{
		Title: i18n.T("motion.title"), Sub: i18n.T("motion.subtitle"), Section: sec,
		TabCam: i18n.T("motion.tabCamPaths"), TabStudio: i18n.T("motion.tabStudio"),
	}
	if sec == "studio" {
		v := u.moStudioState()
		st.Studio = &v
		return st
	}
	v := u.moCamPathsState()
	st.Cam = &v
	return st
}

func (u *UI) renderMotion() string {
	st := u.moState()
	if zigui.Available() {
		if h, ok := zigWire("RenderMotionV2", wireMoState(st), zigui.RenderMotionV2,
			zigui.RenderMotion, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return motionHTML(st)
}

// moBody is the #mo-body inner fragment (section switch + off-thread avatar-scan patch).
func (u *UI) moBody() string {
	st := u.moState()
	if zigui.Available() {
		if h, ok := zigWire("RenderMotionBodyV2", wireMoState(st), zigui.RenderMotionBodyV2,
			zigui.RenderMotionBody, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return motionBodyHTML(st)
}

// motionHTML is the pure Go renderer (golden reference; byte-identical to Zig).
func motionHTML(st moState) string {
	return `<h1 class=page-title>` + html.EscapeString(st.Title) + `</h1><p class=page-sub>` +
		html.EscapeString(st.Sub) + `</p>` +
		`<div class=subtabs>` + subtabBtn("campaths", st.TabCam, st.Section) +
		subtabBtn("studio", st.TabStudio, st.Section) + `</div>` +
		`<div id=mo-body>` + motionBodyHTML(st) + `</div>`
}

// motionBodyHTML is the pure #mo-body inner renderer.
func motionBodyHTML(st moState) string {
	if st.Section == "studio" {
		if st.Studio == nil {
			return ""
		}
		return moStudioHTML(*st.Studio)
	}
	if st.Cam == nil {
		return ""
	}
	return moCamPathsHTML(*st.Cam)
}

func subtabBtn(id, label, cur string) string {
	cls := "subtab"
	if id == cur {
		cls += " active"
	}
	return `<button class="` + cls + `" data-act="mo-section:` + id + `">` + html.EscapeString(label) + `</button>`
}

// ── camera paths ─────────────────────────────────────────────────────────────

// moCamPathsState resolves the path list + the shared campath viewer into render state.
func (u *UI) moCamPathsState() moCamSt {
	if u.svc.VRCTools == nil {
		return moCamSt{Unavailable: i18n.T("motion.vrchatUnavailable"), Rows: []moCamRow{}}
	}
	s := u.mo()
	s.mu.Lock()
	paths, sel := s.cpPaths, s.cpSel
	s.mu.Unlock()
	st := moCamSt{
		Rows: make([]moCamRow, 0, len(paths)), Empty: i18n.T("motion.noCamPaths"),
		ReloadLbl: i18n.T("motion.reloadList"), OrganizeLbl: i18n.T("motion.organizeNow"),
		DJLbl:      i18n.T("motion.installDjPaths"),
		PreviewLbl: i18n.T("motion.preview"), TipS: tipTopicSt("camera-paths"),
		Hint:    i18n.T("campath.hint"),
		LoadLbl: i18n.T("motion.loadIntoVrchat"), CopyLbl: i18n.T("motion.copyFilePath"),
	}
	lastFolder := ""
	for i, p := range paths {
		r := moCamRow{
			Act: fmt.Sprintf("mo-cp-sel:%d", i), Sel: i == sel, Name: p.Name,
			Meta: i18n.T("motion.camPathMeta", i18n.A{
				"count": fmt.Sprint(p.Points), "duration": fmt.Sprintf("%.1f", p.DurationSec), "saved": p.SavedAt.Format("2006-01-02 15:04"),
			}),
		}
		if folder := p.Folder(); folder != lastFolder {
			r.Group, r.ShowGroup = folder, true
			lastFolder = folder
		}
		st.Rows = append(st.Rows, r)
	}
	file := ""
	if sel >= 0 && sel < len(paths) {
		file = paths[sel].File
	}
	u.cpvEnsure("mo", file)
	st.View, st.Info, st.PlayBtn = u.cpvView("mo"), u.moCamPathInfoText(), u.cpvPlayBtn("mo")
	return st
}

// moCamPathsHTML is the pure camera-paths renderer.
func moCamPathsHTML(st moCamSt) string {
	if st.Unavailable != "" {
		return emptyState(st.Unavailable)
	}
	var list strings.Builder
	list.WriteString(`<div class=mo-list>`)
	for _, r := range st.Rows {
		if r.ShowGroup {
			list.WriteString(`<div class=mo-group>` + html.EscapeString(r.Group) + `</div>`)
		}
		cls := "irow"
		if r.Sel {
			cls += " selected"
		}
		list.WriteString(`<div class="` + cls + `" data-act="` + html.EscapeString(r.Act) + `"><div class=irow-main>` +
			`<div class=irow-title>` + html.EscapeString(r.Name) + `</div>` +
			`<div class=irow-sub>` + html.EscapeString(r.Meta) + `</div>` +
			`</div></div>`)
	}
	if len(st.Rows) == 0 {
		list.WriteString(emptyState(st.Empty))
	}
	list.WriteString(`</div>`)
	list.WriteString(btnRow(
		btn(st.ReloadLbl, "ghost", "mo-cp-refresh", ""),
		btn(st.OrganizeLbl, "outline", "mo-cp-organize", ""),
		btn(st.DJLbl, "outline", "mo-cp-dj", "")))

	detail := st.View +
		`<div class=mo-hint>` + html.EscapeString(st.Hint) + `</div>` +
		`<div id=mo-cp-info class=mo-info>` + html.EscapeString(st.Info) + `</div>` +
		btnRow(
			st.PlayBtn,
			btn(st.LoadLbl, "primary", "mo-cp-load", ""),
			btn(st.CopyLbl, "outline", "mo-cp-copy", ""))
	head := `<div class=card-label>` + html.EscapeString(st.PreviewLbl) + tipOr(st.TipS, st.Tip) + `</div>`
	return masterDetail(list.String(), head+detail)
}

// moCamPathInfo renders the #mo-cp-info inner text (escaped; live patch target).
func (u *UI) moCamPathInfo() string { return html.EscapeString(u.moCamPathInfoText()) }

// moCamPathInfoText resolves the selected path's detail line (plain text).
func (u *UI) moCamPathInfoText() string {
	s := u.mo()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cpSel < 0 || s.cpSel >= len(s.cpPaths) {
		return i18n.T("motion.selectPath")
	}
	p := s.cpPaths[s.cpSel]
	where := p.WorldName
	if p.Local {
		where = i18n.T("motion.playerRelative")
	} else if where == "" {
		where = i18n.T("motion.unknownWorld")
	}
	return i18n.T("motion.camPathDetail", i18n.A{
		"name": p.Name, "where": where, "count": fmt.Sprint(p.Points), "duration": fmt.Sprintf("%.1f", p.DurationSec),
	})
}

// ── motion studio ────────────────────────────────────────────────────────────

// moStudioState resolves takes + playback + avatar/physics/point-cloud toggles into render state.
func (u *UI) moStudioState() moStudioSt {
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

	playLbl := "▶ " + i18n.T("player.play")
	if playing {
		playLbl = "⏸ " + i18n.T("player.pause")
	}
	if pcDensity == "" {
		pcDensity = "med"
	}
	st := moStudioSt{
		Recs: make([]moRecRow, 0, len(names)), Empty: i18n.T("motion.noRecordings"),
		RefreshLbl: i18n.T("common.refresh"), ExportLbl: i18n.T("motion.exportAnim"),
		RenderLbl: i18n.T("motion.renderVideo"), PCViewLbl: i18n.T("motion.pcView"),
		RenderProg: u.moRenderProgHTML(), Avatar: u.moAvatarState(),
		PreviewLbl: i18n.T("motion.preview"), TipS: tipTopicSt("motion-studio"),
		View: u.moViewHTML(), Hint: i18n.T("motion.studioHint"),
		Time:    i18n.T("motion.timeDisplay", i18n.A{"cur": fmt.Sprintf("%.1f", t), "dur": fmt.Sprintf("%.1f", dur)}),
		Scrub:   moSlide(i18n.T("motion.scrub"), "mo-scrub", 0, 1000, 1, scrubVal(t, dur), ""),
		PlayLbl: playLbl, StopLbl: "⏹ " + i18n.T("player.stop"),
		Loop:  moTog(i18n.T("motion.loop"), "mo-loop", loop),
		OSC:   moTog(i18n.T("motion.oscTrackers"), "mo-osc", oscOn),
		VMC:   moTog(i18n.T("motion.streamVmc"), "mo-vmc", vmcOn),
		Model: moTog(i18n.T("motion.showAvatarModel"), "mo-model", modelOn),
		// model-only rows (moPhysRow / moCompareRows / moPointCloudRows parity)
		ModelOn: modelOn, HasDyn: hasDyn, PhysNote: i18n.T("motion.noPhysBones"),
		Phys:  moTog(i18n.T("motion.avatarPhysics"), "mo-phys", physOn),
		Rest:  moTog(i18n.T("motion.restPose"), "mo-rest", restPose),
		Marks: moTog(i18n.T("motion.overlayTrackerPoints"), "mo-marks", marks),
		PC:    moTog(i18n.T("motion.pointCloud"), "mo-pc", pcOn),
		PCOn:  pcOn,
		// never leave Rows nil: Go marshals nil slices to JSON null, the Zig slice parse
		// then fails and the whole tab silently falls back to the Go renderer.
		PCDensity:   selState{Rows: []selRow{}},
		PCColor:     moTog(i18n.T("motion.pcColor"), "mo-pc-color", pcColor),
		PCNote:      i18n.T("motion.pcNote"),
		PCExportLbl: i18n.T("motion.pcExport"),
		VMCHelp:     i18n.T("motion.vmcHelp", i18n.A{"addr": u.svc.Cfg.Features.VROverlay.ResolvedVMCAddr()}),
	}
	for _, n := range names {
		st.Recs = append(st.Recs, moRecRow{Name: n, Act: "mo-rec-sel:" + n, Sel: n == recName})
	}
	if modelOn && pcOn { // the density picker only exists while the cloud is on (ss registration too)
		st.PCDensity = moResolveSelect("mo-pc-density", i18n.T("motion.pcDensity"), "mo-pc-density:", pcDensity, func() []ssOpt {
			return []ssOpt{
				{Val: "low", Label: i18n.T("motion.pcLow"), Sub: i18n.T("motion.pcLowSub")},
				{Val: "med", Label: i18n.T("motion.pcMed"), Sub: i18n.T("motion.pcMedSub")},
				{Val: "high", Label: i18n.T("motion.pcHigh"), Sub: i18n.T("motion.pcHighSub")},
				{Val: "ultra", Label: i18n.T("motion.pcUltra"), Sub: i18n.T("motion.pcUltraSub")},
			}
		})
	}
	return st
}

// moResolveSelect registers + resolves a rich-row smart select into pure render state
// (resolveSelectBox's [][2]string form can't carry ssOpt.Sub, which the density +
// avatar pickers need). Tab-prefixed: keeps the fleet's parallel tab work collision-free.
func moResolveSelect(id, label, act, cur string, opts func() []ssOpt) selState {
	ssRegister(id, act, cur, opts)
	s := ssResolve(id)
	s.Label = label
	return s
}

// moStudioHTML is the pure motion-studio renderer.
func moStudioHTML(st moStudioSt) string {
	var list strings.Builder
	list.WriteString(`<div class=mo-list>`)
	for _, r := range st.Recs {
		cls := "irow"
		if r.Sel {
			cls += " selected"
		}
		list.WriteString(`<div class="` + cls + `" data-act="` + html.EscapeString(r.Act) + `"><div class=irow-main><div class=irow-title>` +
			html.EscapeString(r.Name) + `</div></div></div>`)
	}
	if len(st.Recs) == 0 {
		list.WriteString(emptyState(st.Empty))
	}
	list.WriteString(`</div>`)
	list.WriteString(btnRow(
		btn(st.RefreshLbl, "ghost", "mo-rec-refresh", ""),
		btn(st.ExportLbl, "outline", "pick-save:anim:mo-export", ""),
		btn(st.RenderLbl, "outline", "mo-render", ""),
		// View any exported .rmpc in the raw-WebGL point-cloud viewer (needs no avatar loaded).
		btn(st.PCViewLbl, "outline", "pick-file:mo-pc-view", "")))
	list.WriteString(`<div id=mo-render-prog>` + st.RenderProg + `</div>`)
	list.WriteString(moAvatarHTML(st.Avatar))

	detail := `<div id=mo-view data-actpos="mo-orbit" data-actwheel="mo-zoom">` + st.View + `</div>` +
		`<div class=mo-hint>` + html.EscapeString(st.Hint) + `</div>` +
		`<div id=mo-time class=mo-info>` + html.EscapeString(st.Time) + `</div>` +
		slider(st.Scrub.Label, st.Scrub.Act, st.Scrub.Min, st.Scrub.Max, st.Scrub.Step, st.Scrub.Val, st.Scrub.Unit) +
		btnRow(btn(st.PlayLbl, "go", "mo-play", ""), btn(st.StopLbl, "outline", "mo-stop", "")) +
		`<div class=mo-toggles>` +
		moToggleHTML(st.Loop) + moToggleHTML(st.OSC) + moToggleHTML(st.VMC) + moToggleHTML(st.Model) +
		moPhysRow(st) + moCompareRows(st) + moPointCloudRows(st) +
		`</div>` +
		`<p class=page-sub>` + html.EscapeString(st.VMCHelp) + `</p>`
	head := `<div class=card-label>` + html.EscapeString(st.PreviewLbl) + tipOr(st.TipS, st.Tip) + `</div>`
	return masterDetail(list.String(), head+detail)
}

// moToggleHTML renders one resolved toggle through the shared switch primitive.
func moToggleHTML(t moToggleSt) string { return toggleRow(t.Label, t.Act, t.On) }

// moPhysRow: avatar-physics toggle, shown only with the model on. Chain source:
// <avatar>.physbones.json sidecar (exported from Unity - real PhysBone/DynamicBone
// params) when present, otherwise name-heuristic detection (hair/tail/ears/…).
func moPhysRow(st moStudioSt) string {
	if !st.ModelOn {
		return ""
	}
	if !st.HasDyn {
		return `<div class=mo-info>` + st.PhysNote + `</div>`
	}
	return moToggleHTML(st.Phys)
}

// moCompareRows: pose-debug toggles, shown only with the model on. Rest pose renders the
// mesh at its authored A/T reference (the take's tracker points still draw, so retarget
// alignment is inspectable); the marker overlay draws the raw take points over the posed mesh.
func moCompareRows(st moStudioSt) string {
	if !st.ModelOn {
		return ""
	}
	return moToggleHTML(st.Rest) + moToggleHTML(st.Marks)
}

// moPointCloudRows: point-cloud preview toggle + (when on) export density / colour / .rmpc
// export. Shown only with the model on (the cloud IS the posed mesh's surface).
func moPointCloudRows(st moStudioSt) string {
	if !st.ModelOn {
		return ""
	}
	out := moToggleHTML(st.PC)
	if !st.PCOn {
		return out
	}
	return out + selHTML(st.PCDensity) + moToggleHTML(st.PCColor) +
		`<div class=mo-info>` + html.EscapeString(st.PCNote) + `</div>` +
		btnRow(btn(st.PCExportLbl, "primary", "pick-save:rmpc:mo-pc-export", ""))
}

func scrubVal(t, dur float64) float64 {
	if dur <= 0 {
		return 0
	}
	return 1000 * t / dur
}

// moAvatarState: active VRM + peer-synced avatar management (mesh preview lands with C5).
func (u *UI) moAvatarState() moAvatarSt {
	cur := u.svc.Cfg.Features.VRCTools.AvatarVRM
	curLbl := i18n.T("motion.noneLabel")
	if cur != "" {
		curLbl = filepath.Base(cur)
	}
	avatarOpts := u.moAvatarOpts() // cached snapshot; scans off-thread on first need + re-patches
	return moAvatarSt{
		Label: i18n.T("motion.avatarLabel"),
		Sel: moResolveSelect("mo-avatar", i18n.T("motion.activeAvatar"), "mo-avatar-set", cur,
			func() []ssOpt { return avatarOpts }),
		ImportLbl: i18n.T("motion.importAvatar"), SyncLbl: i18n.T("motion.syncNow"),
		Info: i18n.T("motion.avatarCurrentInfo", i18n.A{"name": curLbl}),
	}
}

// moAvatarHTML is the pure avatar-block renderer.
func moAvatarHTML(st moAvatarSt) string {
	return `<div class=mo-avatars><div class=card-label>` + html.EscapeString(st.Label) + `</div>` +
		selHTML(st.Sel) +
		btnRow(btn(st.ImportLbl, "outline", "pick-file:mo-avatar-import", ""),
			btn(st.SyncLbl, "ghost", "mo-avatar-sync", "")) +
		`<div class=mo-info>` + html.EscapeString(st.Info) + `</div></div>`
}

// moAvatarOpts returns the cached avatar-picker options. config.ListAvatars (os.ReadDir +
// per-file stat) must not run on the render thread (ssInner evaluates opts even when closed),
// so it's computed OFF-THREAD into motion state; empty until the first scan lands + patches.
// Keyed by VRMAvatarsDir's mtime (one cheap dir-stat) so avatars a peer replicates into the dir
// surface without a manual Sync; also invalidated explicitly via moInvalidateAvatars on import/sync.
func (u *UI) moAvatarOpts() []ssOpt {
	mod := avatarsDirMod()
	s := u.mo()
	s.mu.Lock()
	if s.avatarLoaded && s.avatarDirMod == mod && !s.avatarPending {
		opts := s.avatarOpts
		s.mu.Unlock()
		return opts
	}
	if s.avatarPending {
		opts := s.avatarOpts
		s.mu.Unlock()
		return opts // scan in flight: serve stale until it lands
	}
	s.avatarPending = true
	stale := s.avatarOpts // serve last-known while rescanning (no blank flash on a dir-changed refresh)
	s.mu.Unlock()
	u.bg(func() {
		var opts []ssOpt
		for _, e := range config.ListAvatars() {
			opts = append(opts, ssOpt{Val: e.Path, Label: e.Name, Sub: humanBytes(uint64(e.Size))})
		}
		s := u.mo()
		s.mu.Lock()
		s.avatarOpts, s.avatarLoaded, s.avatarDirMod, s.avatarPending = opts, true, mod, false
		s.mu.Unlock()
		if !u.stopped() {
			u.moPatchBody()
		}
	})
	return stale
}

// avatarsDirMod returns VRMAvatarsDir's mtime (unix-nano; 0 on error) - a single-stat change key.
// Adding/removing a file (peer replication, import) bumps the dir mtime, so the picker cache
// re-scans without a manual Sync. One stat, never the per-file ListAvatars walk, on the render path.
func avatarsDirMod() int64 {
	fi, err := os.Stat(config.VRMAvatarsDir())
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
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
