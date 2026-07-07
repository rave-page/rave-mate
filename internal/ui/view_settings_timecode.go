package ui

import (
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/timecode"
)

// Device enumeration indirection (testable / keeps the card body readable).
var (
	timecodeWaveOutDevices = timecode.WaveOutDevices
	timecodeMidiOutDevices = timecode.MidiOutDevices
)

// Education-first help copy: every control explains what LTC/MTC/Art-Net timecode ARE so a
// newcomer learns by using the app. Wording rule: never reference a specific setup ("another
// machine" / "a paired instance", never a named PC role); concrete apps as examples are fine.
const (
	tcHelpCard = "SMPTE timecode is a running clock (HH:MM:SS:FF - FF = frame number) that keeps " +
		"separate software and gear in sync. rave-mate generates one master 'house' clock and " +
		"sends it out in up to three formats at once; anything listening chases it, on this " +
		"machine or another one on the network."
	tcHelpRate = "Frames per second of the clock. Pick what the RECEIVER is configured for - both " +
		"sides must match. 25 is the norm in PAL/EBU regions, 30 and 29.97 drop-frame in NTSC " +
		"regions, 24 for film. 29.97 drop-frame skips two frame NUMBERS each minute (except every " +
		"10th) so the display stays true to the wall clock - no audio/video is dropped."
	tcHelpStart = "Where the clock starts counting. A fixed timecode (e.g. 00:00:00:00) is normal " +
		"for shows; 'time of day' jams the clock to your wall time, handy when several systems " +
		"should agree on real-world time."
	tcHelpLTC = "LTC (Linear TimeCode) is timecode as an AUDIO signal - the classic SMPTE format. " +
		"Pick an audio output here, route it into a virtual audio cable (e.g. VB-CABLE), and " +
		"select that cable as the timecode input in the receiver. Resolume Arena's SMPTE input " +
		"only accepts LTC, so this is the output to use for it. Over a real cable it also feeds " +
		"hardware on another machine."
	tcHelpLTCLevel = "Output level in dBFS (decibels below digital full scale). −3 dBFS is the " +
		"broadcast norm for LTC - loud enough to decode reliably, with headroom against clipping. " +
		"Lower it if the receiver's input meters clip."
	tcHelpMTC = "MTC (MIDI Time Code) is timecode over MIDI - for sequencers, DAWs and " +
		"show-control software that chase MIDI (e.g. Reaper, Cubase, QLab). Create a virtual " +
		"MIDI port with loopMIDI (or Windows MIDI Services loopback), pick it here, and set the " +
		"receiver to sync to MTC on that port."
	tcHelpArtNet = "Art-Net TimeCode is timecode over the network (UDP) for lighting consoles and " +
		"nodes - grandMA-class desks chase it directly. Default sends to everyone on the LAN " +
		"(broadcast, port 6454); enter host:port to target one console. Resolume ignores this - " +
		"use LTC for Resolume."
)

// extraLTCSection lists the additional LTC audio outputs (each a distinct device / virtual cable),
// with per-row on/device/level + remove, and an Add button. The one house clock drives them all.
func (u *UI) extraLTCSection(f *config.TimecodeFeature) fyne.CanvasObject {
	rows := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		rows.RemoveAll()
		devs, _ := timecodeWaveOutDevices()
		opts := append([]string{defaultDev}, devs...)
		for i := range f.LTCExtra {
			i := i
			e := f.LTCExtra[i]
			on := widget.NewCheck("", func(v bool) { f.LTCExtra[i].On = v; u.saveCfg() })
			on.SetChecked(e.On)
			dev := widget.NewSelect(opts, func(s string) {
				if s == defaultDev {
					s = ""
				}
				f.LTCExtra[i].Device = s
				u.saveCfg()
			})
			dev.PlaceHolder = "Refresh to list outputs"
			if e.Device == "" {
				dev.SetSelected(defaultDev)
			} else {
				dev.SetSelected(e.Device)
			}
			gain := newEntry()
			gain.SetText(strconv.FormatFloat(e.ResolvedGainDb(), 'g', -1, 64))
			gain.OnChanged = func(s string) {
				if v, err := strconv.ParseFloat(s, 64); err == nil && v <= 0 && v >= -40 {
					f.LTCExtra[i].GainDb = v
					u.saveCfg()
				}
			}
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.LTCExtra = append(f.LTCExtra[:i], f.LTCExtra[i+1:]...)
				u.saveCfg()
				rebuild()
			})
			rows.Add(container.NewBorder(nil, nil, on, container.NewHBox(shrinkWidth(70, gain), del), dev))
		}
		rows.Refresh()
	}
	rebuild()
	add := widget.NewButtonWithIcon("Add LTC output", theme.ContentAddIcon(), func() {
		f.LTCExtra = append(f.LTCExtra, config.TCLTCSink{On: true})
		u.saveCfg()
		rebuild()
	})
	return container.NewVBox(rows, add)
}

// extraMTCSection lists additional MTC MIDI outputs (one port per receiver).
func (u *UI) extraMTCSection(f *config.TimecodeFeature) fyne.CanvasObject {
	rows := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		rows.RemoveAll()
		devs, _ := timecodeMidiOutDevices()
		opts := append([]string{defaultDev}, devs...)
		for i := range f.MTCExtra {
			i := i
			e := f.MTCExtra[i]
			on := widget.NewCheck("", func(v bool) { f.MTCExtra[i].On = v; u.saveCfg() })
			on.SetChecked(e.On)
			dev := widget.NewSelect(opts, func(s string) {
				if s == defaultDev {
					s = ""
				}
				f.MTCExtra[i].Device = s
				u.saveCfg()
			})
			dev.PlaceHolder = "Refresh to list MIDI ports"
			if e.Device == "" {
				dev.SetSelected(defaultDev)
			} else {
				dev.SetSelected(e.Device)
			}
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.MTCExtra = append(f.MTCExtra[:i], f.MTCExtra[i+1:]...)
				u.saveCfg()
				rebuild()
			})
			rows.Add(container.NewBorder(nil, nil, on, del, dev))
		}
		rows.Refresh()
	}
	rebuild()
	add := widget.NewButtonWithIcon("Add MTC output", theme.ContentAddIcon(), func() {
		f.MTCExtra = append(f.MTCExtra, config.TCMTCSink{On: true})
		u.saveCfg()
		rebuild()
	})
	return container.NewVBox(rows, add)
}

// extraArtNetSection lists additional Art-Net TimeCode targets (one host per console).
func (u *UI) extraArtNetSection(f *config.TimecodeFeature) fyne.CanvasObject {
	rows := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		rows.RemoveAll()
		for i := range f.ArtNetExtra {
			i := i
			e := f.ArtNetExtra[i]
			on := widget.NewCheck("", func(v bool) { f.ArtNetExtra[i].On = v; u.saveCfg() })
			on.SetChecked(e.On)
			addr := newEntry()
			addr.SetPlaceHolder("host:port (blank = broadcast)")
			addr.SetText(e.Addr)
			addr.OnChanged = func(s string) { f.ArtNetExtra[i].Addr = s; u.saveCfg() }
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.ArtNetExtra = append(f.ArtNetExtra[:i], f.ArtNetExtra[i+1:]...)
				u.saveCfg()
				rebuild()
			})
			rows.Add(container.NewBorder(nil, nil, on, del, addr))
		}
		rows.Refresh()
	}
	rebuild()
	add := widget.NewButtonWithIcon("Add Art-Net target", theme.ContentAddIcon(), func() {
		f.ArtNetExtra = append(f.ArtNetExtra, config.TCArtNetSink{On: true})
		u.saveCfg()
		rebuild()
	})
	return container.NewVBox(rows, add)
}

// timecodeCard configures the house SMPTE timecode outputs: master clock (rate + start), the three
// sinks (LTC audio / MTC / Art-Net), a live TC readout, and START/STOP.
func (u *UI) timecodeCard() fyne.CanvasObject {
	f := &u.svc.Cfg.Features.Timecode
	tcs := u.svc.Timecode

	st := u.newStatus(func(s *cardStatus) {
		switch {
		case !f.Enabled:
			s.set(colMuted, "off")
		case tcs != nil && tcs.Running():
			tc, _ := tcs.Now()
			s.set(colBrandBase, "RUNNING · "+tc.String())
		default:
			s.set(colBrandMint, "armed - press START")
		}
	})
	toggle := u.moduleToggle("timecode", &f.Enabled)

	// Rate.
	const r24, r25, r2997, r30 = "24 (film)", "25 (PAL/EBU)", "29.97 drop-frame (NTSC)", "30 (non-drop)"
	rateToken := map[string]string{r24: "24", r25: "25", r2997: "29.97", r30: "30"}
	rateSel := widget.NewSelect([]string{r24, r25, r2997, r30}, func(s string) {
		f.Rate = rateToken[s]
		u.saveCfg()
	})
	switch f.ResolvedRate() {
	case "24":
		rateSel.SetSelected(r24)
	case "25":
		rateSel.SetSelected(r25)
	case "29.97":
		rateSel.SetSelected(r2997)
	default:
		rateSel.SetSelected(r30)
	}

	// Start position: time-of-day jam or a fixed timecode.
	startEntry := newEntry()
	startEntry.SetPlaceHolder("00:00:00:00")
	clockChk := widget.NewCheck("Start at time of day (jam to the wall clock)", nil)
	applyStart := func() {
		if clockChk.Checked {
			f.StartAt = "clock"
			startEntry.Disable()
		} else {
			f.StartAt = startEntry.Text
			startEntry.Enable()
		}
		u.saveCfg()
	}
	if f.StartAt == "clock" {
		clockChk.SetChecked(true)
		startEntry.Disable()
	} else {
		startEntry.SetText(f.StartAt)
	}
	clockChk.OnChanged = func(bool) { applyStart() }
	startEntry.OnChanged = func(string) {
		if !clockChk.Checked {
			f.StartAt = startEntry.Text
			u.saveCfg()
		}
	}

	// ── LTC (audio) ──
	ltcOn := widget.NewCheck("LTC audio output", func(v bool) { f.LTC.On = v; u.saveCfg() })
	ltcOn.SetChecked(f.LTC.On)
	ltcDev := widget.NewSelect(nil, func(s string) {
		if s == defaultDev {
			s = ""
		}
		f.LTC.Device = s
		u.saveCfg()
	})
	ltcDev.PlaceHolder = "Refresh to list outputs"
	ltcRefresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() {
		devs, err := timecodeWaveOutDevices()
		if err != nil {
			u.Notify("Timecode", "Audio outputs unavailable: "+err.Error())
			return
		}
		ltcDev.Options = append([]string{defaultDev}, devs...)
		if f.LTC.Device == "" {
			ltcDev.SetSelected(defaultDev)
		} else {
			ltcDev.SetSelected(f.LTC.Device)
		}
		ltcDev.Refresh()
	})
	ltcGain := newEntry()
	ltcGain.SetText(strconv.FormatFloat(f.LTC.ResolvedGainDb(), 'g', -1, 64))
	ltcGain.OnChanged = func(s string) {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v <= 0 && v >= -40 {
			f.LTC.GainDb = v
			u.saveCfg()
		}
	}

	// ── MTC (MIDI) ──
	mtcOn := widget.NewCheck("MTC MIDI output", func(v bool) { f.MTC.On = v; u.saveCfg() })
	mtcOn.SetChecked(f.MTC.On)
	mtcDev := widget.NewSelect(nil, func(s string) {
		if s == defaultDev {
			s = ""
		}
		f.MTC.Device = s
		u.saveCfg()
	})
	mtcDev.PlaceHolder = "Refresh to list MIDI ports"
	mtcRefresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() {
		devs, err := timecodeMidiOutDevices()
		if err != nil {
			u.Notify("Timecode", "MIDI outputs unavailable: "+err.Error())
			return
		}
		mtcDev.Options = append([]string{defaultDev}, devs...)
		if f.MTC.Device == "" {
			mtcDev.SetSelected(defaultDev)
		} else {
			mtcDev.SetSelected(f.MTC.Device)
		}
		mtcDev.Refresh()
	})

	// ── Art-Net (UDP) ──
	artOn := widget.NewCheck("Art-Net TimeCode output", func(v bool) { f.ArtNet.On = v; u.saveCfg() })
	artOn.SetChecked(f.ArtNet.On)
	artAddr := newEntry()
	artAddr.SetPlaceHolder(f.ArtNet.ResolvedAddr())
	artAddr.SetText(f.ArtNet.Addr)
	artAddr.OnChanged = func(s string) { f.ArtNet.Addr = s; u.saveCfg() }

	body := container.NewVBox(
		labelWithHelp("What is timecode?", tcHelpCard),
		mutedLabel("One master clock, three formats. Enable the outputs you need, press START, and set the receiving software to chase that format. Changes to rate/devices apply on the next START."),
		container.NewBorder(nil, nil, labelWithHelp("Frame rate", tcHelpRate), nil, rateSel),
		labelWithHelp("Start position", tcHelpStart),
		clockChk,
		startEntry,

		widget.NewSeparator(),
		container.NewHBox(ltcOn, helpIcon(tcHelpLTC)),
		container.NewBorder(nil, nil, widget.NewLabel("Audio output"), ltcRefresh, ltcDev),
		container.NewBorder(nil, nil, labelWithHelp("Level (dBFS)", tcHelpLTCLevel), nil, ltcGain),
		mutedLabel("Add more LTC outputs to fan the same clock into extra virtual cables / devices at once (row toggle + device + dBFS)."),
		u.extraLTCSection(f),

		widget.NewSeparator(),
		container.NewHBox(mtcOn, helpIcon(tcHelpMTC)),
		container.NewBorder(nil, nil, widget.NewLabel("MIDI port"), mtcRefresh, mtcDev),
		u.extraMTCSection(f),

		widget.NewSeparator(),
		container.NewHBox(artOn, helpIcon(tcHelpArtNet)),
		container.NewBorder(nil, nil, widget.NewLabel("Target (host:port)"), nil, artAddr),
		u.extraArtNetSection(f),

		widget.NewSeparator(),
		mutedLabel("START/STOP + the live TC readout live on the Live tab's transport strip. Also: rave-mate ctl tc-status / tc-start / tc-stop."),
	)
	return featureCard("Timecode outputs (SMPTE)", "House clock other software and gear chase - LTC audio, MTC MIDI, Art-Net.", toggle, st, body)
}
