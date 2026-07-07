package ui

// "Media sync" settings card: keep chosen OBS media sources locked to a shared house clock across
// the local OBS + any paired instance / LAN OBS. Education-first - every control carries a "?"
// tooltip (see help.go) explaining the concept for newcomers. Wording never names a personal rig.

import (
	"fmt"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/mediasync"
	"rave.page/mate/internal/obs"
)

// help copy (kept here so the whole feature's education text lives in one place).
const (
	helpMediaSync = "Locks a video/media source in OBS to a shared clock so it plays in step across machines. " +
		"Start the clock, pick the media source(s), and rave-mate nudges each one to stay on time."
	helpSyncOffset = "A constant head-start (ms) added to this source's target position. Line it up with the mix " +
		"or with a source on another machine - increase if it runs late, decrease if it runs early."
	helpDeadBand = "How far out of step (in frames) a source may drift before rave-mate corrects it. A small dead " +
		"band avoids constant micro-jumps that look like stutter. ~2 frames is a good start."
	helpFps     = "Your project frame rate. Used only to turn the dead band from frames into milliseconds."
	helpRestart = "If a source is more than this many ms off, rave-mate restarts it and seeks straight to the target " +
		"instead of nudging - for big jumps (a source that ended, or was just enabled)."
	helpEndpoint = "Which OBS this source lives in - the local OBS, or a paired instance / another machine on the network."
	helpKind     = "VLC Video Source seeks are millisecond-exact. OBS's Media Source snaps seeks to the nearest keyframe " +
		"(~40–80ms), so sync is looser - prefer a VLC source for tight timing."
	helpStartStop = "Start sets the shared clock to zero right now; every synced source chases it from here. Stop " +
		"freezes the clock and leaves the sources where they are."
)

func (u *UI) obsSyncCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.OBS.Sync

	// ── control-law tunables ──
	fpsEntry := newEntry()
	fpsEntry.SetText(strconv.FormatFloat(orFloat(f.Fps, 30), 'f', -1, 64))
	fpsEntry.OnChanged = func(s string) {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
			f.Fps = v
			u.saveCfg()
		}
	}
	deadEntry := newEntry()
	deadEntry.SetText(strconv.FormatFloat(orFloat(f.DeadBandFrames, 2), 'f', -1, 64))
	deadEntry.OnChanged = func(s string) {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
			f.DeadBandFrames = v
			u.saveCfg()
		}
	}
	restartEntry := newEntry()
	restartEntry.SetText(strconv.Itoa(orInt(f.RestartThresholdMs, 1500)))
	restartEntry.OnChanged = func(s string) {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			f.RestartThresholdMs = v
			u.saveCfg()
		}
	}

	// ── sources list ──
	sourcesBox := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		sourcesBox.RemoveAll()
		for i := range f.Sources {
			idx := i
			src := &f.Sources[i]
			edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				u.obsSyncSourceDialog(src, rebuild)
			})
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Sources = append(f.Sources[:idx], f.Sources[idx+1:]...)
				u.saveCfg()
				rebuild()
			})
			state := "off"
			if src.Enabled {
				state = "on"
			}
			ep := src.Endpoint
			if ep == "" {
				ep = "local"
			}
			line := fmt.Sprintf("%s @ %s  (%+dms · %s)", orText(src.InputName, "?"), ep, src.StaticOffsetMs, state)
			sourcesBox.Add(container.NewBorder(nil, nil, nil, container.NewHBox(edit, del), widget.NewLabel(line)))
		}
		if len(f.Sources) == 0 {
			sourcesBox.Add(mutedLabel("No media sources yet. Add one below (its OBS input name, e.g. a VLC Video Source)."))
		}
		sourcesBox.Refresh()
	}
	rebuild()
	add := widget.NewButtonWithIcon("Add media source", theme.ContentAddIcon(), func() {
		f.Sources = append(f.Sources, config.OBSSyncSource{Enabled: true})
		u.saveCfg()
		rebuild()
	})

	// Status dot (the live per-source readout lives on the Live tab's Media sync card).
	st := u.newStatus(func(s *cardStatus) {
		if u.svc.OBSControl == nil {
			s.set(colMuted, "unavailable")
			return
		}
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case u.svc.OBSControl.SyncRunning():
			s.set(colBrandMint, fmt.Sprintf("syncing %d source(s)", len(u.svc.OBSControl.SyncStatuses())))
		default:
			s.set(colBrandAmber, "clock stopped")
		}
	})

	toggle := u.simpleToggle(&f.Enabled) // obscontrol runs always; tickSync re-reads this flag live
	body := container.NewVBox(
		container.NewBorder(nil, nil, nil, helpIcon(helpMediaSync), mutedLabel("Keep OBS media sources in step with a shared clock.")),
		formGrid(
			labelWithHelp("Frame rate", helpFps), fpsEntry,
			labelWithHelp("Dead band (frames)", helpDeadBand), deadEntry,
			labelWithHelp("Restart threshold (ms)", helpRestart), restartEntry,
		),
		widget.NewSeparator(),
		container.NewBorder(nil, add, nil, nil, sourcesBox),
		mutedLabel("Start/stop + the live per-source readout live on the Live tab (Media sync card - enable it via Edit dashboard). Also: `rave-mate ctl obs-sync-status`."),
	)
	return featureCard("Media sync", "Chase OBS media sources to a shared house clock (local OBS + paired/LAN OBS).", toggle, st, body)
}

// buildMediaSyncContent is the "mediasync" Live card: start/stop the shared house clock +
// the live per-source chase readout. nil when OBS control is unavailable.
func (u *UI) buildMediaSyncContent() fyne.CanvasObject {
	oc := u.svc.OBSControl
	if oc == nil {
		return nil
	}
	running := mutedInline("")
	readout := container.NewVBox()
	update := func() {
		if oc.SyncRunning() {
			running.SetText("clock running")
		} else {
			running.SetText("clock stopped")
		}
		readout.RemoveAll()
		for _, ss := range oc.SyncStatuses() {
			readout.Add(mutedLabel(fmtSyncStatus(ss)))
		}
		readout.Refresh()
	}
	startBtn := widget.NewButtonWithIcon("Start sync now", theme.MediaPlayIcon(), func() {
		oc.StartSync()
		update()
	})
	startBtn.Importance = widget.HighImportance
	stopBtn := widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		oc.StopSync()
		update()
	})
	update()
	tick := time.NewTicker(2 * time.Second)
	u.closers = append(u.closers, tick.Stop)
	goUI("live-mediasync", func() {
		for range tick.C {
			fyne.Do(update)
		}
	})
	return container.NewVBox(
		container.NewBorder(nil, nil, container.NewHBox(startBtn, stopBtn, helpIcon(helpStartStop)), nil, running),
		readout,
	)
}

// obsSyncSourceDialog edits one synced media source.
func (u *UI) obsSyncSourceDialog(src *config.OBSSyncSource, onSave func()) {
	input := newEntry()
	input.SetText(src.InputName)
	input.SetPlaceHolder("OBS input name (e.g. VJ Loop)")

	endpoint := widget.NewSelect(u.obsEndpointOptions(), nil)
	endpoint.SetSelected(endpointLabel(src.Endpoint))

	kind := widget.NewSelect([]string{"(auto-detect)", "VLC Video Source (ms-exact)", "Media Source (keyframe-snapped)"}, nil)
	kind.SetSelected(kindLabel(src.InputKind))

	offset := newEntry()
	offset.SetText(strconv.Itoa(src.StaticOffsetMs))
	offset.SetPlaceHolder("0")

	enabled := widget.NewCheck("Enabled", nil)
	enabled.SetChecked(src.Enabled)

	form := widget.NewForm(
		widget.NewFormItem("Input name", input),
		&widget.FormItem{Widget: container.NewHBox(endpoint, helpIcon(helpEndpoint)), Text: "OBS"},
		&widget.FormItem{Widget: container.NewHBox(kind, helpIcon(helpKind)), Text: "Source kind"},
		&widget.FormItem{Widget: container.NewHBox(offset, helpIcon(helpSyncOffset)), Text: "Sync offset (ms)"},
		widget.NewFormItem("", enabled),
	)
	d := dialog.NewCustomConfirm("Synced media source", "Save", "Cancel", form, func(ok bool) {
		if !ok {
			return
		}
		src.InputName = input.Text
		src.Endpoint = endpointID(endpoint.Selected)
		src.InputKind = kindID(kind.Selected)
		src.Enabled = enabled.Checked
		if n, err := strconv.Atoi(offset.Text); err == nil {
			src.StaticOffsetMs = n
		}
		u.saveCfg()
		onSave()
	}, u.win)
	d.Resize(fyne.NewSize(460, 380))
	d.Show()
}

// obsEndpointOptions lists selectable OBS endpoints: local + configured LAN remotes.
func (u *UI) obsEndpointOptions() []string {
	opts := []string{"local (this PC)"}
	for _, r := range u.svc.Cfg.Features.OBS.Remotes {
		opts = append(opts, r.ResolvedName())
	}
	return opts
}

// endpointLabel maps a stored endpoint id to its Select label.
func endpointLabel(ep string) string {
	if ep == "" || ep == "local" {
		return "local (this PC)"
	}
	return ep
}

// endpointID maps a Select label back to a stored endpoint id.
func endpointID(label string) string {
	if label == "" || label == "local (this PC)" {
		return "local"
	}
	return label
}

func kindLabel(kind string) string {
	switch kind {
	case obs.KindVLCSource:
		return "VLC Video Source (ms-exact)"
	case obs.KindMediaSource, obs.KindMediaSource2:
		return "Media Source (keyframe-snapped)"
	default:
		return "(auto-detect)"
	}
}

func kindID(label string) string {
	switch label {
	case "VLC Video Source (ms-exact)":
		return obs.KindVLCSource
	case "Media Source (keyframe-snapped)":
		return obs.KindMediaSource
	default:
		return ""
	}
}

// fmtSyncStatus renders one chaser status line for the live readout.
func fmtSyncStatus(s mediasync.Status) string {
	state := s.LastAction
	if s.Err != "" {
		state = "error: " + s.Err
	} else if !s.Playing && s.Active {
		state = "waiting for playback"
	}
	warn := ""
	if s.KeyframeSnapped {
		warn = " · keyframe-snapped"
	}
	return fmt.Sprintf("%s: err %+dms · %.0f corr/min · %s%s", orText(s.Source, "?"), s.ErrorMs, s.CorrectionsPerMin, state, warn)
}

func orInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func orText(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
