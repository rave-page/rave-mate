package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/overlaystyle"
	"rave.page/mate/internal/spoutdll"
	"rave.page/mate/internal/videoshare"
)

// renderOverlays is the overlay-pipeline cockpit at parity with the Fyne Overlays tab: a style
// toolstrip, then a per-output card for each renderer (appearance/browser/waveform/PNG/OBS-direct/
// video-share/now-playing-files) with its own live status dot + body controls, and a bottom
// outputs-summary strip. Config lives in Cfg.Features.*; appearance (gradients/EQ colours) is edited
// in the browser overlay editor (opened via open-url) - the native side toggles outputs + fields.
func (u *UI) renderOverlays() string {
	if u.svc.Cfg == nil {
		return panel(i18n.T("tab.overlays"), "") + emptyState(i18n.T("overlays.configUnavailable"))
	}
	f := &u.svc.Cfg.Features
	base := fmt.Sprintf("http://127.0.0.1:%d/", f.OverlayWeb.ResolvedPort())

	var b strings.Builder
	b.WriteString(panel(i18n.T("tab.overlays"), i18n.T("overlays.subtitle")))
	b.WriteString(btnRow(
		btn(i18n.T("overlays.editStyle"), "primary", "open-url", base+"?edit=1"),
		btn(i18n.T("overlays.openOverlay"), "explore", "open-url", base),
		btn(i18n.T("overlays.copyUrl"), "ghost", "copy", base),
	))

	b.WriteString(`<div class=ovl-cards>`)
	b.WriteString(u.overlayAppearanceCard(base))
	b.WriteString(u.overlayWebCardHTML(base))
	b.WriteString(u.overlayWaveformCardHTML())
	b.WriteString(u.overlayPngCardHTML())
	b.WriteString(u.overlayObsCardHTML())
	b.WriteString(u.overlayVideoShareCardHTML())
	b.WriteString(u.overlayNowPlayingCardHTML())
	b.WriteString(`</div>`)

	b.WriteString(`<div id=ovl-strip class=livestrip>` + u.ovlStripHTML() + `</div>`)
	return b.String()
}

// ── per-output cards ──

// overlayAppearanceCard: the single appearance source of truth (browser editor) + the fade-by-fader
// toggle (surgically read/written to overlay-style.json so browser-owned keys survive).
func (u *UI) overlayAppearanceCard(base string) string {
	stylePath, _ := config.DataPath("overlay-style.json")
	fader := overlaystyle.GetBool(stylePath, "cardFaderReact", false)
	body := `<p class=ovl-note>` + htmlEscape(i18n.T("overlays.appearance.note1")) + `</p>` +
		btnRow(btn(i18n.T("overlays.editColours"), "primary", "open-url", base+"?edit=1"), btn(i18n.T("overlays.copyEditorUrl"), "ghost", "copy", base+"?edit=1")) +
		toggleRow(i18n.T("overlays.faderToggle"), "ovl-fader", fader) +
		`<p class=ovl-note>` + htmlEscape(i18n.T("overlays.appearance.note2")) + `</p>`
	return ovlCard(i18n.T("overlays.appearance.title"), "", "", body)
}

// overlayWebCardHTML: the browser overlay server (OBS Browser source) - port + open/layout/copy +
// OBS auto-manage (scene / nest).
func (u *UI) overlayWebCardHTML(base string) string {
	f := &u.svc.Cfg.Features.OverlayWeb
	src := &f.OBSSource
	body := field(i18n.T("overlays.port"), "set:overlay-port", strconv.Itoa(f.ResolvedPort()), "number") +
		btnRow(btn(i18n.T("overlays.openOverlay"), "explore", "open-url", base), btn(i18n.T("overlays.layoutEditor"), "outline", "open-url", base+"?edit=1"), btn(i18n.T("overlays.copyUrl"), "ghost", "copy", base)) +
		kv(i18n.T("overlays.overlayUrl"), base) +
		`<p class=ovl-note>` + htmlEscape(i18n.T("overlays.web.note1")) + `</p>` +
		`<hr class=ovl-sep>` +
		toggleRow(i18n.T("overlays.web.autoAdd"), "ovl-obssrc", src.Enabled) +
		field(i18n.T("overlays.web.obsScene"), "ovl-obsscene", src.ResolvedScene(), "text") +
		toggleRow(i18n.T("overlays.web.nest"), "ovl-obsnest", src.NestInProgram) +
		`<p class=ovl-note>` + htmlEscape(i18n.T("overlays.web.note2")) + `</p>`
	return ovlCard(i18n.T("overlays.web.title"), "ovl-st-web", u.ovlStatus("web"), body)
}

// overlayWaveformCardHTML: scrolling waveform + EQ/FX panel - zoom / playhead / colours / opacities.
func (u *UI) overlayWaveformCardHTML() string {
	f := &u.svc.Cfg.Features.OverlayWaveform
	zoomOpts := make([][2]string, 0, 7)
	for _, n := range []string{"8", "12", "16", "20", "30", "45", "60"} {
		zoomOpts = append(zoomOpts, [2]string{n, i18n.T("overlays.wf.zoomOptionSeconds", i18n.A{"n": n})})
	}
	zoom := selectBox(i18n.T("overlays.wf.zoom"), "ovl-wf-zoom", zoomOpts, trimNum(f.ResolvedZoomSeconds()))
	playhead := selectBox(i18n.T("overlays.wf.playhead"), "ovl-wf-playhead", [][2]string{
		{"0.25", i18n.T("overlays.wf.playheadQuarter")}, {"0.333", i18n.T("overlays.wf.playheadThird")}, {"0.5", i18n.T("overlays.wf.playheadCenter")}, {"0.75", i18n.T("overlays.wf.playheadRightQuarter")},
	}, ovlPlayheadBucket(f.ResolvedPlayheadPct()))
	body := `<p class=ovl-note>` + htmlEscape(i18n.T("overlays.wf.note1")) + `</p>` +
		zoom + playhead +
		field(i18n.T("overlays.wf.waveColor"), "ovl-wf-wavecolor", f.ResolvedWaveColor(), "text") +
		slider(i18n.T("overlays.wf.waveOpacity"), "ovl-wf-waveopac", 0, 1, 0.05, f.ResolvedWaveOpacity(), "") +
		field(i18n.T("overlays.wf.bgColor"), "ovl-wf-bgcolor", f.ResolvedBgColor(), "text") +
		slider(i18n.T("overlays.wf.bgOpacity"), "ovl-wf-bgopac", 0, 1, 0.05, f.ResolvedBgOpacity(), "") +
		`<p class=ovl-note>` + htmlEscape(i18n.T("overlays.wf.note2")) + `</p>`
	return ovlCard(i18n.T("overlays.wf.title"), "ovl-st-wave", u.ovlStatus("wave"), body)
}

// overlayPngCardHTML: native per-deck PNG cards - output folder + open.
func (u *UI) overlayPngCardHTML() string {
	f := &u.svc.Cfg.Features.OverlayPNG
	body := field(i18n.T("overlays.outputFolder"), "ovl-png-dir", f.Dir, "text") +
		btnRow(btn(i18n.T("overlays.openFolder"), "outline", "ovl-png-open", "")) +
		`<p class=ovl-note>` + htmlEscape(i18n.T("overlays.png.note")) + `</p>`
	return ovlCard(i18n.T("overlays.png.title"), "ovl-st-png", u.ovlStatus("png"), body)
}

// overlayObsCardHTML: obs-websocket renderer - status-only card (no fields), mirrors Fyne.
func (u *UI) overlayObsCardHTML() string {
	body := `<p class=ovl-note>` + htmlEscape(i18n.T("overlays.obs.note")) + `</p>`
	return ovlCard(i18n.T("overlays.obs.title"), "ovl-st-obs", u.ovlStatus("obs"), body)
}

// overlayVideoShareCardHTML: GPU/IPC video-share sink - render scale + (Spout) runtime install.
func (u *UI) overlayVideoShareCardHTML() string {
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
	scale := selectBox(i18n.T("overlays.vs.renderScale"), "ovl-vs-scale", scaleOpts, strconv.Itoa(f.ResolvedRenderScale()))
	body := `<p class=ovl-note>` + htmlEscape(note) + `</p>` + scale +
		`<p class=ovl-note>` + htmlEscape(i18n.T("overlays.vs.note2")) + `</p>`
	if backend == "Spout" {
		body += `<hr class=ovl-sep><div id=ovl-spout>` + u.spoutControlsHTML() + `</div>`
	}
	return ovlCard(i18n.T("overlays.vs.title"), "ovl-st-vs", u.ovlStatus("vs"), body)
}

// overlayNowPlayingCardHTML: now_playing.{json,txt} for OBS - output folder + open.
func (u *UI) overlayNowPlayingCardHTML() string {
	f := &u.svc.Cfg.Features.NowPlayingFile
	body := field(i18n.T("overlays.outputFolder"), "ovl-np-dir", f.Dir, "text") +
		btnRow(btn(i18n.T("overlays.openFolder"), "outline", "ovl-np-open", "")) +
		`<p class=ovl-note>` + htmlEscape(i18n.T("overlays.np.note")) + `</p>`
	return ovlCard(i18n.T("overlays.np.title"), "ovl-st-np", u.ovlStatus("np"), body)
}

// spoutControlsHTML renders the SpoutLibrary.dll detect + download/install UI (parity with the Fyne
// spoutRuntimeControls). Re-rendered into #ovl-spout on install completion.
func (u *UI) spoutControlsHTML() string {
	st := spoutdll.Probe()
	installLabel := i18n.T("overlays.spout.install")
	var statusLine, extra string
	if st.Installed {
		statusLine = htmlEscape(i18n.T("overlays.spout.installed", i18n.A{"path": st.Path}))
		installLabel = i18n.T("overlays.spout.reinstall")
	} else {
		statusLine = htmlEscape(i18n.T("overlays.spout.notFound"))
		extra = btn(i18n.T("overlays.spout.openSdk"), "ghost", "open-url", spoutdll.HomePage)
	}
	installBtn := btn(installLabel, "outline", "ovl-spout-install", "")
	if !spoutdll.CanInstall() {
		installBtn = `<button class="rp-btn rp-btn--outline" disabled>` + htmlEscape(installLabel) + `</button>`
	}
	return `<p class=ovl-note>` + htmlEscape(i18n.T("overlays.spout.note")) + `</p>` +
		`<div class=ovl-note>` + statusLine + `</div>` +
		btnRow(installBtn, extra) +
		`<div id=ovl-spout-prog></div>`
}

// ── status + strip (live-patched by the overlays tick) ──

// ovlStatus renders one output's status dot + line (kind ∈ web/wave/png/np/obs/vs).
func (u *UI) ovlStatus(kind string) string {
	f := &u.svc.Cfg.Features
	onoff := func(on bool, t string) string {
		if on {
			return statusRow("success", t, "")
		}
		return statusRow("muted", i18n.T("common.off"), "")
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
			return statusRow("muted", i18n.T("common.off"), "")
		case !f.OBS.Enabled:
			return statusRow("warning", i18n.T("overlays.status.obsEnableFirst"), "")
		case u.svc.OBS != nil && u.svc.OBS.Status().Connected:
			return statusRow("success", i18n.T("overlays.status.obsDriving"), "")
		default:
			return statusRow("warning", i18n.T("overlays.status.obsNotConnected"), "")
		}
	case "vs":
		switch b := videoshare.Backend(); {
		case !f.VideoShare.Enabled:
			return statusRow("muted", i18n.T("common.off"), "")
		case b == "none":
			return statusRow("warning", i18n.T("overlays.status.vsNoBackend"), "")
		default:
			return statusRow("success", i18n.T("overlays.status.vsSharing", i18n.A{"backend": b}), "")
		}
	}
	return ""
}

// ovlStripHTML renders the bottom outputs-summary strip (left = per-output marks, center = hint,
// right = OBS state) - parity with overlayOutputsSummary + the Fyne kitStatusStrip.
func (u *UI) ovlStripHTML() string {
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
	return `<span>` + htmlEscape(strings.Join(parts, " · ")) + `</span><span>` + htmlEscape(i18n.T("overlays.strip.hint")) +
		`</span><span>` + htmlEscape(right) + `</span>`
}

// ── small helpers ──

// ovlCard wraps a card with an optional live-status region (stable id → patched by the tick).
func ovlCard(title, statusID, statusHTML, body string) string {
	if statusID != "" {
		body = `<div id=` + statusID + `>` + statusHTML + `</div>` + body
	}
	return card(title, "", body)
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
