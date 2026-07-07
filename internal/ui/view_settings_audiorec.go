package ui

import (
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
)

// audioRecordCard configures native audio-device recording: pick a device, record to FLAC
// (default) following OBS record start/stop + manual. Files register as set recordings linked to
// the tracklist (with a .cue sidecar + embedded tags).
func (u *UI) audioRecordCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.AudioRecord
	ar := u.svc.AudioRec

	// Device picker - populated async (enumeration shells out to ffmpeg).
	deviceSel := widget.NewSelect(nil, func(s string) { f.Device = s; u.saveCfg() })
	deviceSel.PlaceHolder = "(select audio input device)"
	if f.Device != "" {
		deviceSel.Options = []string{f.Device}
		deviceSel.SetSelected(f.Device)
	}
	devHint := mutedLabel("")
	loadDevices := func() {
		if ar == nil {
			return
		}
		devHint.SetText("Scanning devices…")
		goUI("audiorec-dev", func() {
			defer debuglog.Recover(u.svc.Log, "audiorec-dev", false)
			names, err := ar.Devices()
			fyne.Do(func() {
				if err != nil || len(names) == 0 {
					devHint.SetText("No audio devices found. Plug in your interface (or a loopback cable like VB-CABLE), then Refresh. Needs ffmpeg (Settings → Transcode).")
					return
				}
				if f.Device != "" && !hasStr(names, f.Device) {
					names = append([]string{f.Device}, names...) // keep the saved one selectable
				}
				deviceSel.Options = names
				deviceSel.Refresh()
				devHint.SetText("")
			})
		})
	}
	loadDevices()
	refreshDev := widget.NewButtonWithIcon("Refresh devices", theme.ViewRefreshIcon(), loadDevices)

	// Encoding: format + lossy bitrate.
	formatSel := widget.NewSelect([]string{"flac", "wav", "mp3", "aac"}, func(s string) { f.Format = s; u.saveCfg() })
	formatSel.SetSelected(f.ResolvedFormat())
	bitrate := newEntry()
	bitrate.SetText(strconv.Itoa(f.ResolvedBitrate()))
	bitrate.OnChanged = func(s string) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			f.Bitrate = n
			u.saveCfg()
		}
	}
	sampleRate := newEntry()
	sampleRate.SetPlaceHolder("auto (device native)")
	if f.SampleRate > 0 {
		sampleRate.SetText(strconv.Itoa(f.SampleRate))
	}
	sampleRate.OnChanged = func(s string) {
		n, _ := strconv.Atoi(s) // blank/invalid → 0 = auto
		f.SampleRate = n
		u.saveCfg()
	}

	dirEntry := newEntry()
	dirEntry.SetPlaceHolder("default: app data / recordings")
	dirEntry.SetText(f.Dir)
	dirEntry.OnChanged = func(s string) { f.Dir = s; u.saveCfg() }
	openDir := widget.NewButton("Open recordings folder", func() { openFile(f.ResolvedDir()) })

	followOBS := widget.NewCheck("Auto start/stop with OBS recording", func(v bool) { f.FollowOBS = v; u.saveCfg() })
	followOBS.SetChecked(f.FollowOBS)
	writeTags := widget.NewCheck("Embed the played tracklist into the recording (tags + .cue sidecar)", func(v bool) { f.WriteTags = v; u.saveCfg() })
	writeTags.SetChecked(f.WriteTags)

	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled || ar == nil:
			s.set(colMuted, "off")
		case ar.Status().Recording:
			s.set(colBrandBase, "recording")
		case f.Device == "":
			s.set(colBrandAmber, "no device selected")
		default:
			s.set(colBrandMint, "armed")
		}
	})
	toggle := u.moduleToggle("audiorecord", &f.Enabled)
	body := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Device"), nil, deviceSel),
		container.NewHBox(refreshDev),
		devHint,
		formGrid(
			fieldLabel("Format"), formatSel,
			fieldLabel("Bitrate kbps (mp3/aac)"), bitrate,
			fieldLabel("Sample rate"), sampleRate,
			fieldLabel("Recordings folder"), folderPickerRow(dirEntry),
		),
		container.NewHBox(openDir),
		followOBS,
		writeTags,
		mutedLabel("FLAC is lossless at the device's native rate. Toggle off/on (or Refresh devices) to apply a device change."),
		mutedLabel("Manual record start/stop lives on the Live tab's transport strip (REC)."),
	)
	return featureCard("Audio recording (native)",
		"Record a chosen audio device to disk (lossless FLAC) - synced to OBS recording or manual. Linked to the tracklist.",
		toggle, st, body)
}
