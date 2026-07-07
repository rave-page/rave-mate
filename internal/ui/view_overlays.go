package ui

// Overlays workflow page (UI_WORKFLOW_IA.md phase 2): compose + style the now-playing
// deck overlays end-to-end on one page - style editor shortcut (kitToolStrip), every
// output's card (browser / per-deck PNG / OBS-direct / video share / waveform /
// now-playing files, moved off Settings), and a kitStatusStrip of which outputs are on.

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/videoshare"
)

const helpOverlays = "Everything the overlay pipeline needs on one page: style it once in the " +
	"browser editor (colours, gradients, per-band EQ, card border - saved to overlay-style.json), " +
	"then enable the outputs you need. All outputs render the same style live: OBS Browser " +
	"source, per-deck PNGs, native OBS inputs, GPU video share (Spout/Syphon/PipeWire)."

// buildOverlays builds the Overlays tab.
func (u *UI) buildOverlays() fyne.CanvasObject {
	// The overlay cards register live indicators via u.newStatus, which normally feeds the
	// Settings ticker (and buildSettings resets that slice). Collect them for this page's
	// own ticker instead.
	prev := u.settingsStats
	u.settingsStats = nil
	cards := []fyne.CanvasObject{
		u.overlayStyleHintCard(),
		u.overlayWebCard(),
		u.overlayWaveformCard(),
		u.overlayPngCard(),
		u.overlayObsCard(),
		u.overlayVideoShareCard(),
		u.nowPlayingFileCard(),
	}
	stats := u.settingsStats
	u.settingsStats = prev

	strip := newKitStatusStrip()
	update := func() {
		for _, s := range stats {
			if s.update != nil {
				s.update(s)
			}
		}
		strip.SetLeft(u.overlayOutputsSummary())
		strip.SetCenter("style edits apply live to every output")
		f := &u.svc.Cfg.Features
		switch {
		case u.svc.OBS != nil && f.OBS.Enabled && u.svc.OBS.Status().Connected:
			strip.SetRight("OBS ✓")
		case f.OBS.Enabled:
			strip.SetRight("OBS ✕")
		default:
			strip.SetRight("OBS off")
		}
	}
	update()
	stop := make(chan struct{})
	u.closers = append(u.closers, func() { close(stop) })
	goUI("overlays-status", func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fyne.Do(update)
			}
		}
	})

	head := container.NewVBox(
		widget.NewLabelWithStyle("Overlays", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		mutedLabel("Style once, output everywhere - live now-playing visuals for OBS and beyond"),
		u.overlaysToolStrip(),
	)
	body := container.NewVScroll(container.New(newMasonry(), cards...))
	return container.NewBorder(head, strip.Object(), nil, nil, body)
}

// overlaysToolStrip is the page's quick-action strip: open the style editor / overlay,
// copy the editor URL.
func (u *UI) overlaysToolStrip() fyne.CanvasObject {
	w := &u.svc.Cfg.Features.OverlayWeb
	urlOf := func(suffix string) string {
		return fmt.Sprintf("http://127.0.0.1:%d/%s", w.ResolvedPort(), suffix)
	}
	open := func(suffix string) {
		if uri, err := url.Parse(urlOf(suffix)); err == nil {
			_ = u.app.OpenURL(uri)
		}
	}
	editBtn := widget.NewButtonWithIcon("Edit style", theme.ColorPaletteIcon(), func() { open("?edit=1") })
	editBtn.Importance = widget.HighImportance
	openBtn := widget.NewButtonWithIcon("Open overlay", theme.ComputerIcon(), func() { open("") })
	copyBtn := widget.NewButtonWithIcon("Copy URL", theme.ContentCopyIcon(), func() {
		u.app.Clipboard().SetContent(urlOf(""))
		u.Notify("rave-mate", "Overlay URL copied - add a Browser source in OBS")
	})
	return kitToolStrip(
		smallCaps("STYLE"),
		editBtn, openBtn, copyBtn,
		helpIcon(helpOverlays),
	)
}

// overlayOutputsSummary renders the status-strip outputs zone ("web ✓ · png - · …").
func (u *UI) overlayOutputsSummary() string {
	f := &u.svc.Cfg.Features
	mark := func(on bool) string {
		if on {
			return "✓"
		}
		return "-"
	}
	parts := []string{
		"web " + mark(f.OverlayWeb.Enabled),
		"png " + mark(f.OverlayPNG.Enabled),
		"obs " + mark(f.OverlayOBS.Enabled),
	}
	if f.VideoShare.Enabled && videoshare.Backend() != "none" {
		parts = append(parts, "share "+videoshare.Backend())
	} else {
		parts = append(parts, "share "+mark(false))
	}
	parts = append(parts,
		"waveform "+mark(f.OverlayWaveform.Enabled),
		"files "+mark(f.NowPlayingFile.Enabled),
	)
	return strings.Join(parts, " · ")
}
