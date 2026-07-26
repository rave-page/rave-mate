package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/spoutdll"
	"rave.page/mate/internal/videoshare"
	"rave.page/mate/internal/zigui"
)

// Overlays is a Zig-rendered tab (native/zigui/src/overlays.zig): Go resolves config +
// live status + i18n into ovlState, the Zig lib renders HTML byte-identical to the Go
// renderers below (fallback + golden reference, zigui_golden_overlays_test.go). The four
// live-patched fragments (#ovl-strip, #ovl-appearance, #ovl-spout, #ovl-st-<kind>) each
// get their own state + renderer + export.

// ovlCardState is one output card's header + optional live-status region + optional
// enable switch (Fyne sessionToggle parity; zero Act = no switch, e.g. appearance).
type ovlCardState struct {
	Title    string   `json:"title"`
	StatusID string   `json:"statusId"` // "" = no status region (trusted literal id)
	Status   uiStatus `json:"status"`
	En       uiToggle `json:"en"`
}

// ovlApprState is the appearance card (browser editor + fade-by-fader).
type ovlApprState struct {
	Card  ovlCardState `json:"card"`
	Note1 string       `json:"note1"`
	Btns  []uiBtn      `json:"btns"`
	Fader uiToggle     `json:"fader"`
	Note2 string       `json:"note2"`
}

// ovlWebState is the browser-overlay-server card.
type ovlWebState struct {
	Card    ovlCardState `json:"card"`
	Port    uiField      `json:"port"`
	Btns    []uiBtn      `json:"btns"`
	URL     uiKV         `json:"url"`
	Note1   string       `json:"note1"`
	AutoAdd uiToggle     `json:"autoAdd"`
	Scene   uiField      `json:"scene"`
	Nest    uiToggle     `json:"nest"`
	Note2   string       `json:"note2"`
}

// ovlWaveState is the waveform/EQ card.
type ovlWaveState struct {
	Card      ovlCardState `json:"card"`
	Note1     string       `json:"note1"`
	Zoom      selState     `json:"zoom"`
	Playhead  selState     `json:"playhead"`
	WaveColor uiField      `json:"waveColor"`
	WaveOpac  uiSlider     `json:"waveOpac"`
	BgColor   uiField      `json:"bgColor"`
	BgOpac    uiSlider     `json:"bgOpac"`
	Note2     string       `json:"note2"`
}

// ovlDirState is a folder-output card (PNG cards, now-playing files).
type ovlDirState struct {
	Card ovlCardState `json:"card"`
	Dir  uiField      `json:"dir"`
	Open uiBtn        `json:"open"`
	Note string       `json:"note"`
}

// ovlNoteState is a status-only card (obs-websocket renderer).
type ovlNoteState struct {
	Card ovlCardState `json:"card"`
	Note string       `json:"note"`
}

// ovlSpoutState is the SpoutLibrary.dll detect + install block (#ovl-spout).
type ovlSpoutState struct {
	Note       string `json:"note"`
	StatusLine string `json:"statusLine"`
	InstallLbl string `json:"installLbl"`
	CanInstall bool   `json:"canInstall"`
	OpenSdk    string `json:"openSdk"` // "" = installed (no SDK button)
	SdkURL     string `json:"sdkUrl"`
}

// ovlVSState is the GPU/IPC video-share card.
type ovlVSState struct {
	Card     ovlCardState  `json:"card"`
	Note     string        `json:"note"`
	Scale    selState      `json:"scale"`
	Note2    string        `json:"note2"`
	Spout    bool          `json:"spout"` // backend == "Spout" → render the runtime block
	SpoutCtl ovlSpoutState `json:"spoutCtl"`
}

// ovlStripState is the bottom outputs-summary strip (#ovl-strip).
type ovlStripState struct {
	Parts string `json:"parts"` // per-output marks, already joined with " · "
	Hint  string `json:"hint"`
	Right string `json:"right"` // OBS state
}

// ovlState is the resolved render state for the Overlays view (JSON → Zig).
type ovlState struct {
	Title       string        `json:"title"`
	Sub         string        `json:"sub"`
	Available   bool          `json:"available"` // Cfg resolved
	Unavailable string        `json:"unavailable"`
	TopBtns     []uiBtn       `json:"topBtns"`
	Appearance  ovlApprState  `json:"appearance"`
	Web         ovlWebState   `json:"web"`
	Wave        ovlWaveState  `json:"wave"`
	Png         ovlDirState   `json:"png"`
	Obs         ovlNoteState  `json:"obs"`
	VS          ovlVSState    `json:"vs"`
	NP          ovlDirState   `json:"np"`
	Strip       ovlStripState `json:"strip"`
}

// emptyOvlState is the zero view with NON-NIL slices everywhere: nil marshals to JSON
// null, which fails the Zig slice parse (and would silently drop the tab to Go).
func emptyOvlState() ovlState {
	return ovlState{
		TopBtns:    []uiBtn{},
		Appearance: ovlApprState{Btns: []uiBtn{}},
		Web:        ovlWebState{Btns: []uiBtn{}},
		Wave:       ovlWaveState{Zoom: selState{Rows: []selRow{}}, Playhead: selState{Rows: []selRow{}}},
		VS:         ovlVSState{Scale: selState{Rows: []selRow{}}},
	}
}

// overlaysState resolves config + live status + i18n into render state.
func (u *UI) overlaysState() ovlState {
	st := emptyOvlState()
	st.Title = i18n.T("tab.overlays")
	st.Sub = i18n.T("overlays.subtitle")
	st.Available = u.svc.Cfg != nil
	st.Unavailable = i18n.T("overlays.configUnavailable")
	if !st.Available {
		return st
	}
	base := u.ovlBase()
	edit := base + "?edit=1"
	st.TopBtns = []uiBtn{
		{Label: i18n.T("overlays.editStyle"), Variant: "primary", Act: "open-url", Val: edit},
		{Label: i18n.T("overlays.openOverlay"), Variant: "explore", Act: "open-url", Val: base},
		{Label: i18n.T("overlays.copyUrl"), Variant: "ghost", Act: "copy", Val: base},
	}
	st.Appearance = u.ovlApprState(base)
	st.Web = u.ovlWebState(base)
	st.Wave = u.ovlWaveState()
	st.Png = u.ovlPngState()
	st.Obs = u.ovlObsState()
	st.VS = u.ovlVSState()
	st.NP = u.ovlNPState()
	st.Strip = u.ovlStripState()
	return st
}

// ovlBase is the local overlay-server root URL (http://127.0.0.1:<port>/).
func (u *UI) ovlBase() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", u.svc.Cfg.Features.OverlayWeb.ResolvedPort())
}

// ── per-output card state ──

// ovlApprState: the single appearance source of truth (browser editor) + the fade-by-fader
// toggle (surgically read/written to overlay-style.json so browser-owned keys survive).
func (u *UI) ovlApprState(base string) ovlApprState {
	edit := base + "?edit=1"
	return ovlApprState{
		Card:  ovlCardState{Title: i18n.T("overlays.appearance.title")},
		Note1: i18n.T("overlays.appearance.note1"),
		Btns: []uiBtn{
			{Label: i18n.T("overlays.editColours"), Variant: "primary", Act: "open-url", Val: edit},
			{Label: i18n.T("overlays.copyEditorUrl"), Variant: "ghost", Act: "copy", Val: edit},
		},
		// cached; the overlay-style.json read runs off the render goroutine
		Fader: newToggle(i18n.T("overlays.faderToggle"), "ovl-fader", u.ovlFaderCached()),
		Note2: i18n.T("overlays.appearance.note2"),
	}
}

// ovlWebState: the browser overlay server (OBS Browser source) - port + open/layout/copy +
// OBS auto-manage (scene / nest).
func (u *UI) ovlWebState(base string) ovlWebState {
	f := &u.svc.Cfg.Features.OverlayWeb
	src := &f.OBSSource
	return ovlWebState{
		Card: ovlCardState{Title: i18n.T("overlays.web.title"), StatusID: "ovl-st-web", Status: u.ovlStatus("web"), En: newToggle(i18n.T("common.enabledCap"), "ovl-en-web", f.Enabled)},
		Port: newField(i18n.T("overlays.port"), "set:overlay-port", strconv.Itoa(f.ResolvedPort()), "number"),
		Btns: []uiBtn{
			{Label: i18n.T("overlays.openOverlay"), Variant: "explore", Act: "open-url", Val: base},
			{Label: i18n.T("overlays.layoutEditor"), Variant: "outline", Act: "open-url", Val: base + "?edit=1"},
			{Label: i18n.T("overlays.copyUrl"), Variant: "ghost", Act: "copy", Val: base},
		},
		URL:     newKV(i18n.T("overlays.overlayUrl"), base),
		Note1:   i18n.T("overlays.web.note1"),
		AutoAdd: newToggle(i18n.T("overlays.web.autoAdd"), "ovl-obssrc", src.Enabled),
		Scene:   newField(i18n.T("overlays.web.obsScene"), "ovl-obsscene", src.ResolvedScene(), "text"),
		Nest:    newToggle(i18n.T("overlays.web.nest"), "ovl-obsnest", src.NestInProgram),
		Note2:   i18n.T("overlays.web.note2"),
	}
}

// ovlWaveState: scrolling waveform + EQ/FX panel - zoom / playhead / colours / opacities.
func (u *UI) ovlWaveState() ovlWaveState {
	f := &u.svc.Cfg.Features.OverlayWaveform
	zoomOpts := make([][2]string, 0, 7)
	for _, n := range []string{"8", "12", "16", "20", "30", "45", "60"} {
		zoomOpts = append(zoomOpts, [2]string{n, i18n.T("overlays.wf.zoomOptionSeconds", i18n.A{"n": n})})
	}
	playheadOpts := [][2]string{
		{"0.25", i18n.T("overlays.wf.playheadQuarter")}, {"0.333", i18n.T("overlays.wf.playheadThird")},
		{"0.5", i18n.T("overlays.wf.playheadCenter")}, {"0.75", i18n.T("overlays.wf.playheadRightQuarter")},
	}
	return ovlWaveState{
		Card:      ovlCardState{Title: i18n.T("overlays.wf.title"), StatusID: "ovl-st-wave", Status: u.ovlStatus("wave"), En: newToggle(i18n.T("common.enabledCap"), "ovl-en-wave", f.Enabled)},
		Note1:     i18n.T("overlays.wf.note1"),
		Zoom:      resolveSelectBox(i18n.T("overlays.wf.zoom"), "ovl-wf-zoom", zoomOpts, trimNum(f.ResolvedZoomSeconds())),
		Playhead:  resolveSelectBox(i18n.T("overlays.wf.playhead"), "ovl-wf-playhead", playheadOpts, ovlPlayheadBucket(f.ResolvedPlayheadPct())),
		WaveColor: newField(i18n.T("overlays.wf.waveColor"), "ovl-wf-wavecolor", f.ResolvedWaveColor(), "text"),
		WaveOpac:  newSlider(i18n.T("overlays.wf.waveOpacity"), "ovl-wf-waveopac", 0, 1, 0.05, f.ResolvedWaveOpacity(), ""),
		BgColor:   newField(i18n.T("overlays.wf.bgColor"), "ovl-wf-bgcolor", f.ResolvedBgColor(), "text"),
		BgOpac:    newSlider(i18n.T("overlays.wf.bgOpacity"), "ovl-wf-bgopac", 0, 1, 0.05, f.ResolvedBgOpacity(), ""),
		Note2:     i18n.T("overlays.wf.note2"),
	}
}

// ovlPngState: native per-deck PNG cards - output folder + open.
func (u *UI) ovlPngState() ovlDirState {
	f := &u.svc.Cfg.Features.OverlayPNG
	return ovlDirState{
		Card: ovlCardState{Title: i18n.T("overlays.png.title"), StatusID: "ovl-st-png", Status: u.ovlStatus("png"), En: newToggle(i18n.T("common.enabledCap"), "ovl-en-png", f.Enabled)},
		Dir:  newField(i18n.T("overlays.outputFolder"), "ovl-png-dir", f.Dir, "text"),
		Open: uiBtn{Label: i18n.T("overlays.openFolder"), Variant: "outline", Act: "ovl-png-open"},
		Note: i18n.T("overlays.png.note"),
	}
}

// ovlObsState: obs-websocket renderer - status-only card (no fields), mirrors Fyne.
func (u *UI) ovlObsState() ovlNoteState {
	f := &u.svc.Cfg.Features.OverlayOBS
	return ovlNoteState{
		Card: ovlCardState{Title: i18n.T("overlays.obs.title"), StatusID: "ovl-st-obs", Status: u.ovlStatus("obs"), En: newToggle(i18n.T("common.enabledCap"), "ovl-en-obs", f.Enabled)},
		Note: i18n.T("overlays.obs.note"),
	}
}

// ovlVSState: GPU/IPC video-share sink - render scale + (Spout) runtime install.
func (u *UI) ovlVSState() ovlVSState {
	f := &u.svc.Cfg.Features.VideoShare
	backend := videoshare.Backend()
	note := i18n.T("overlays.vs.note", i18n.A{"name": videoshare.SenderName("A")})
	if backend != "none" {
		note += " " + i18n.T("overlays.vs.sharesVia", i18n.A{"backend": backend})
	} else {
		note += " " + i18n.T("overlays.vs.noBackend")
	}
	scaleOpts := [][2]string{}
	for _, o := range [][2]string{{"1", "360×120"}, {"2", "720×240"}, {"3", "1080×360"}, {"4", "1440×480"}, {"6", "2160×720"}} {
		scaleOpts = append(scaleOpts, [2]string{o[0], i18n.T("overlays.vs.scaleOption", i18n.A{"mult": o[0], "res": o[1]})})
	}
	st := ovlVSState{
		Card:  ovlCardState{Title: i18n.T("overlays.vs.title"), StatusID: "ovl-st-vs", Status: u.ovlStatus("vs"), En: newToggle(i18n.T("common.enabledCap"), "ovl-en-vs", f.Enabled)},
		Note:  note,
		Scale: resolveSelectBox(i18n.T("overlays.vs.renderScale"), "ovl-vs-scale", scaleOpts, strconv.Itoa(f.ResolvedRenderScale())),
		Note2: i18n.T("overlays.vs.note2"),
		Spout: backend == "Spout",
	}
	if st.Spout {
		st.SpoutCtl = u.spoutState()
	}
	return st
}

// ovlNPState: now_playing.{json,txt} for OBS - output folder + open.
func (u *UI) ovlNPState() ovlDirState {
	f := &u.svc.Cfg.Features.NowPlayingFile
	return ovlDirState{
		Card: ovlCardState{Title: i18n.T("overlays.np.title"), StatusID: "ovl-st-np", Status: u.ovlStatus("np"), En: newToggle(i18n.T("common.enabledCap"), "ovl-en-np", f.Enabled)},
		Dir:  newField(i18n.T("overlays.outputFolder"), "ovl-np-dir", f.Dir, "text"),
		Open: uiBtn{Label: i18n.T("overlays.openFolder"), Variant: "outline", Act: "ovl-np-open"},
		Note: i18n.T("overlays.np.note"),
	}
}

// spoutState resolves the SpoutLibrary.dll detect + download/install UI (parity with the Fyne
// spoutRuntimeControls).
func (u *UI) spoutState() ovlSpoutState {
	dll := u.spoutStatusCached() // cached; the DLL os.Stat sweep runs off the render goroutine
	st := ovlSpoutState{
		Note:       i18n.T("overlays.spout.note"),
		InstallLbl: i18n.T("overlays.spout.install"),
		CanInstall: spoutdll.CanInstall(),
		SdkURL:     spoutdll.HomePage,
	}
	if dll.Installed {
		st.StatusLine = i18n.T("overlays.spout.installed", i18n.A{"path": dll.Path})
		st.InstallLbl = i18n.T("overlays.spout.reinstall")
	} else {
		st.StatusLine = i18n.T("overlays.spout.notFound")
		st.OpenSdk = i18n.T("overlays.spout.openSdk")
	}
	return st
}

// ── status + strip (live-patched by the overlays tick) ──

// ovlStatus resolves one output's status dot + line (kind ∈ web/wave/png/np/obs/vs).
func (u *UI) ovlStatus(kind string) uiStatus {
	f := &u.svc.Cfg.Features
	onoff := func(on bool, t string) uiStatus {
		if on {
			return newStatus("success", t, "")
		}
		return newStatus("muted", i18n.T("common.off"), "")
	}
	switch kind {
	case "web":
		return onoff(f.OverlayWeb.Enabled, i18n.T("overlays.status.web"))
	case "wave":
		return onoff(f.OverlayWaveform.Enabled, i18n.T("overlays.status.wave"))
	case "png":
		return onoff(f.OverlayPNG.Enabled, i18n.T("overlays.status.png"))
	case "np":
		return onoff(f.NowPlayingFile.Enabled, i18n.T("overlays.status.np"))
	case "obs":
		switch {
		case !f.OverlayOBS.Enabled:
			return newStatus("muted", i18n.T("common.off"), "")
		case !f.OBS.Enabled:
			return newStatus("warning", i18n.T("overlays.status.obsEnableFirst"), "")
		case u.svc.OBS != nil && u.svc.OBS.Status().Connected:
			return newStatus("success", i18n.T("overlays.status.obsDriving"), "")
		default:
			return newStatus("warning", i18n.T("overlays.status.obsNotConnected"), "")
		}
	case "vs":
		switch b := videoshare.Backend(); {
		case !f.VideoShare.Enabled:
			return newStatus("muted", i18n.T("common.off"), "")
		case b == "none":
			return newStatus("warning", i18n.T("overlays.status.vsNoBackend"), "")
		default:
			return newStatus("success", i18n.T("overlays.status.vsSharing", i18n.A{"backend": b}), "")
		}
	}
	return uiStatus{}
}

// ovlStripState resolves the bottom outputs-summary strip (left = per-output marks, center = hint,
// right = OBS state) - parity with overlayOutputsSummary + the Fyne kitStatusStrip.
func (u *UI) ovlStripState() ovlStripState {
	f := &u.svc.Cfg.Features
	mark := func(on bool) string {
		if on {
			return "✓"
		}
		return "-"
	}
	parts := []string{i18n.T("overlays.strip.web") + " " + mark(f.OverlayWeb.Enabled), i18n.T("overlays.strip.png") + " " + mark(f.OverlayPNG.Enabled), i18n.T("overlays.strip.obs") + " " + mark(f.OverlayOBS.Enabled)}
	if f.VideoShare.Enabled && videoshare.Backend() != "none" {
		parts = append(parts, i18n.T("overlays.strip.share")+" "+videoshare.Backend())
	} else {
		parts = append(parts, i18n.T("overlays.strip.share")+" "+mark(false))
	}
	parts = append(parts, i18n.T("overlays.strip.waveform")+" "+mark(f.OverlayWaveform.Enabled), i18n.T("overlays.strip.files")+" "+mark(f.NowPlayingFile.Enabled))
	right := i18n.T("overlays.strip.obsOff")
	switch {
	case u.svc.OBS != nil && f.OBS.Enabled && u.svc.OBS.Status().Connected:
		right = i18n.T("overlays.strip.obsOn")
	case f.OBS.Enabled:
		right = i18n.T("overlays.strip.obsDisconnected")
	}
	return ovlStripState{Parts: strings.Join(parts, " · "), Hint: i18n.T("overlays.strip.hint"), Right: right}
}

// ── bridges ──

// renderOverlays is the overlay-pipeline cockpit at parity with the Fyne Overlays tab: a style
// toolstrip, then a per-output card for each renderer (appearance/browser/waveform/PNG/OBS-direct/
// video-share/now-playing-files) with its own live status dot + body controls, and a bottom
// outputs-summary strip. Config lives in Cfg.Features.*; appearance (gradients/EQ colours) is edited
// in the browser overlay editor (opened via open-url) - the native side toggles outputs + fields.
func (u *UI) renderOverlays() string {
	st := u.overlaysState()
	if zigui.Available() {
		if h, ok := zigWire("RenderOverlaysV2", wireOvlState(st), zigui.RenderOverlaysV2,
			zigui.RenderOverlays, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return overlaysHTML(st)
}

// overlayAppearanceCard is the #ovl-appearance fragment (re-patched by the fader-flag cache).
func (u *UI) overlayAppearanceCard(base string) string {
	st := u.ovlApprState(base)
	if zigui.Available() {
		if h, ok := zigWire("RenderOverlaysAppearanceV2", wireOvlAppr(st), zigui.RenderOverlaysAppearanceV2,
			zigui.RenderOverlaysAppearance, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return ovlApprHTML(st)
}

// spoutControlsHTML is the #ovl-spout fragment (re-rendered on install completion).
func (u *UI) spoutControlsHTML() string {
	st := u.spoutState()
	if zigui.Available() {
		if h, ok := zigWire("RenderOverlaysSpoutV2", wireOvlSpout(st), zigui.RenderOverlaysSpoutV2,
			zigui.RenderOverlaysSpout, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return ovlSpoutHTML(st)
}

// ovlStatusHTML is the #ovl-st-<kind> fragment (patched on every overlays action).
func (u *UI) ovlStatusHTML(kind string) string {
	st := u.ovlStatus(kind)
	if zigui.Available() {
		if h, ok := zigWire("RenderOverlaysStatusV2", wireUiStatus(st), zigui.RenderOverlaysStatusV2,
			zigui.RenderOverlaysStatus, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return st.html()
}

// ovlStripHTML is the #ovl-strip fragment.
func (u *UI) ovlStripHTML() string {
	st := u.ovlStripState()
	if zigui.Available() {
		if h, ok := zigWire("RenderOverlaysStripV2", wireOvlStrip(st), zigui.RenderOverlaysStripV2,
			zigui.RenderOverlaysStrip, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return ovlStripHTMLOf(st)
}

// ── pure Go renderers (golden reference; byte-identical to Zig) ──

func overlaysHTML(st ovlState) string {
	if !st.Available {
		return panel(st.Title, "") + emptyState(st.Unavailable)
	}
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(uiBtnRow(st.TopBtns))
	b.WriteString(`<div class=ovl-cards>`)
	b.WriteString(`<div id=ovl-appearance>` + ovlApprHTML(st.Appearance) + `</div>`) // stable id: the fader-flag cache re-patches it
	b.WriteString(ovlWebHTML(st.Web))
	b.WriteString(ovlWaveHTML(st.Wave))
	b.WriteString(ovlDirHTML(st.Png))
	b.WriteString(ovlNoteHTML(st.Obs))
	b.WriteString(ovlVSHTML(st.VS))
	b.WriteString(ovlDirHTML(st.NP))
	b.WriteString(`</div>`)
	b.WriteString(`<div id=ovl-strip class=livestrip>` + ovlStripHTMLOf(st.Strip) + `</div>`)
	return b.String()
}

func ovlApprHTML(st ovlApprState) string {
	body := ovlNote(st.Note1) + uiBtnRow(st.Btns) + st.Fader.html() + ovlNote(st.Note2)
	return ovlCardHTML(st.Card, body)
}

func ovlWebHTML(st ovlWebState) string {
	body := st.Port.html() + uiBtnRow(st.Btns) + st.URL.html() +
		ovlNote(st.Note1) +
		`<hr class=ovl-sep>` +
		st.AutoAdd.html() + st.Scene.html() + st.Nest.html() +
		ovlNote(st.Note2)
	return ovlCardHTML(st.Card, body)
}

func ovlWaveHTML(st ovlWaveState) string {
	body := ovlNote(st.Note1) +
		fpair(selHTML(st.Zoom), selHTML(st.Playhead)) +
		fpair(st.WaveColor.html(), st.WaveOpac.html()) +
		fpair(st.BgColor.html(), st.BgOpac.html()) +
		ovlNote(st.Note2)
	return ovlCardHTML(st.Card, body)
}

func ovlDirHTML(st ovlDirState) string {
	body := st.Dir.html() + btnRow(st.Open.html()) + ovlNote(st.Note)
	return ovlCardHTML(st.Card, body)
}

func ovlNoteHTML(st ovlNoteState) string { return ovlCardHTML(st.Card, ovlNote(st.Note)) }

func ovlVSHTML(st ovlVSState) string {
	body := ovlNote(st.Note) + selHTML(st.Scale) + ovlNote(st.Note2)
	if st.Spout {
		body += `<hr class=ovl-sep><div id=ovl-spout>` + ovlSpoutHTML(st.SpoutCtl) + `</div>`
	}
	return ovlCardHTML(st.Card, body)
}

func ovlSpoutHTML(st ovlSpoutState) string {
	installBtn := btn(st.InstallLbl, "outline", "ovl-spout-install", "")
	if !st.CanInstall {
		installBtn = `<button class="rp-btn rp-btn--outline" disabled>` + htmlEscape(st.InstallLbl) + `</button>`
	}
	extra := ""
	if st.OpenSdk != "" {
		extra = btn(st.OpenSdk, "ghost", "open-url", st.SdkURL)
	}
	return ovlNote(st.Note) +
		`<div class=ovl-note>` + htmlEscape(st.StatusLine) + `</div>` +
		btnRow(installBtn, extra) +
		`<div id=ovl-spout-prog></div>`
}

func ovlStripHTMLOf(st ovlStripState) string {
	return `<span>` + htmlEscape(st.Parts) + `</span><span>` + htmlEscape(st.Hint) +
		`</span><span>` + htmlEscape(st.Right) + `</span>`
}

// ── small helpers ──

// ovlNote is the muted per-card explanation paragraph.
func ovlNote(text string) string { return `<p class=ovl-note>` + htmlEscape(text) + `</p>` }

// ovlCardHTML wraps a card with an optional live-status region (stable id → patched by the
// tick) + optional enable switch (outside the status div so the tick patch never wipes it).
func ovlCardHTML(c ovlCardState, body string) string {
	if c.En.Act != "" {
		body = c.En.html() + body
	}
	if c.StatusID != "" {
		body = `<div id=` + c.StatusID + `>` + c.Status.html() + `</div>` + body
	}
	return card(c.Title, "", body)
}

// ovlPlayheadBucket maps a playhead fraction to the nearest select option value (mirrors Fyne).
func ovlPlayheadBucket(v float64) string {
	switch {
	case v < 0.29:
		return "0.25"
	case v < 0.42:
		return "0.333"
	case v < 0.6:
		return "0.5"
	default:
		return "0.75"
	}
}
