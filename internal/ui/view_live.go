package ui

// Live cockpit tab (UI_WORKFLOW_IA.md phase 1): the mid-set control page. Transport
// kitToolStrip on top (stream go-live, native audio record, timecode), the modular
// dashboard card list in the middle (now-playing, status, decks, streaming cockpit,
// media sync, DMX, STT, network/timing/perf), and a kitStatusStrip of every live
// signal at the bottom. Absorbs the old Dashboard + Session tabs; live controls that
// used to sit on Settings cards land here.

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/shared/auth"
	"rave.page/mate/internal/stream"
)

const (
	helpLiveStream = "Your live now-playing is published automatically whenever an OBS stream goes live " +
		"on this machine (needs sign-in + the stream bridge enabled in Settings). Flip Private to pause the " +
		"broadcast for non-DJ / private streams; it resumes when you turn Private off."
	helpLiveRec = "Record the configured audio device to disk (lossless FLAC by default), linked to the " +
		"live tracklist. Device, format + folders: Settings ▸ Recording ▸ Audio recording. It can also " +
		"auto-follow OBS recording."
	helpLiveStrip = "Every live signal at a glance: OBS connection/recording, set capture, native audio " +
		"record (left) · timecode + DMX (center) · Twitch channel + system headroom (right)."
)

// buildLive builds the Live cockpit tab.
func (u *UI) buildLive() fyne.CanvasObject {
	title := widget.NewLabelWithStyle("Live", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	subtitle := mutedLabel("Mid-set cockpit - everything happening now, one page")

	// Modular card list (dashCardDefs; toggle/reorder via the edit popover).
	defs := dashCardDefs()
	byID := make(map[string]dashCardDef, len(defs))
	for _, d := range defs {
		byID[d.id] = d
	}
	body := container.NewVBox()
	built := map[string]fyne.CanvasObject{} // cards build once (tickers/subs register once)
	render := func() {
		objs := make([]fyne.CanvasObject, 0, len(defs))
		for _, id := range resolveDashCards(u.dashCardIDs(), defs) {
			card, ok := built[id]
			if !ok {
				d := byID[id]
				if content := d.build(u); content != nil {
					card = cardWithHelp(d.title, d.sub, d.help, content)
				}
				built[id] = card
			}
			if card != nil {
				objs = append(objs, card)
			}
		}
		body.Objects = objs
		body.Refresh()
	}
	render()

	var editBtn *kitButton
	editBtn = newKitButtonWithIcon("", theme.SettingsIcon(), func() {
		u.showDashEditor(editBtn, defs, render)
	})

	head := container.NewVBox(
		container.NewBorder(nil, nil, nil, container.NewVBox(editBtn), container.NewVBox(title, subtitle)),
		u.buildLiveTransport(),
	)
	return container.NewBorder(head, u.buildLiveStatusStrip(), nil, nil,
		container.NewVScroll(body),
	)
}

// buildLiveTransport is the cockpit's transport strip: stream go-live/end, native audio
// record toggle, timecode START/STOP + live readout. Each group carries a ?-help.
func (u *UI) buildLiveTransport() fyne.CanvasObject {
	items := []fyne.CanvasObject{}
	items = append(items, u.liveStreamGroup()...)
	if rec := u.liveRecordGroup(); rec != nil {
		items = append(items, kitToolSep())
		items = append(items, rec...)
	}
	if tc := u.liveTimecodeGroup(); tc != nil {
		items = append(items, kitToolSep())
		items = append(items, tc...)
	}
	return kitToolStrip(items...)
}

// liveStreamGroup is the read-only auto-live status + the private-stream pause switch. Publishing is
// automatic (the OBS stream signal drives it in the daemon) - there is no manual go-live/end.
func (u *UI) liveStreamGroup() []fyne.CanvasObject {
	state := widget.NewLabel("")
	var isLive, signedIn bool
	refresh := func() {
		switch {
		case isLive:
			state.SetText("● Broadcasting now-playing to rave.page (metadata only)")
		case u.svc.Cfg != nil && u.svc.Cfg.Features.StreamBridge.PauseLiveSignal:
			state.SetText("Paused - not broadcasting (private stream)")
		case !signedIn:
			state.SetText("Sign in to broadcast now-playing")
		default:
			state.SetText("Not streaming - auto-broadcasts when OBS goes live")
		}
	}
	applyStream := func(s stream.Status) {
		isLive = s.IsLive
		refresh()
	}
	applyStream(u.svc.Stream.Status())
	stCh, unsub := u.svc.Stream.SubscribeStatus()
	u.closers = append(u.closers, unsub)
	goUI("live", func() {
		for s := range stCh {
			s := s
			fyne.Do(func() { applyStream(s) })
		}
	})
	if u.svc.Auth != nil {
		signedIn = u.svc.Auth.SignedIn()
		refresh()
		u.svc.Auth.OnChange(func(st auth.State) {
			fyne.Do(func() {
				signedIn = st.SignedIn
				refresh()
			})
		})
	}
	pause := widget.NewCheck("Private (pause broadcast)", func(on bool) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.StreamBridge.PauseLiveSignal = on
		u.saveCfg()
		refresh()
	})
	if u.svc.Cfg != nil {
		pause.SetChecked(u.svc.Cfg.Features.StreamBridge.PauseLiveSignal)
	}

	return []fyne.CanvasObject{
		smallCaps("STREAM"),
		state,
		mutedInline("now-playing metadata only - no audio/video"),
		pause,
		helpIcon(helpLiveStream),
	}
}

// liveRecordGroup is the native audio-record toggle (moved off the Settings audio-record
// card); nil when the recorder is unavailable.
func (u *UI) liveRecordGroup() []fyne.CanvasObject {
	ar := u.svc.AudioRec
	if ar == nil {
		return nil
	}
	recBtn := newKitButtonWithIcon("Record audio", theme.MediaRecordIcon(), nil)
	state := mutedInline("-> local FLAC file")
	apply := func() {
		s := ar.Status()
		if s.Recording {
			recBtn.SetIcon(theme.MediaStopIcon())
			recBtn.SetText("Stop")
			src := "manual"
			if s.Auto {
				src = "OBS-synced"
			}
			if s.Path != "" {
				src += " · " + filepath.Base(s.Path)
			}
			state.SetText("● recording audio " + src)
		} else {
			recBtn.SetIcon(theme.MediaRecordIcon())
			recBtn.SetText("Record audio")
			state.SetText("-> local FLAC file")
		}
	}
	recBtn.OnTapped = func() {
		var err error
		if ar.Status().Recording {
			err = ar.StopManual()
		} else {
			err = ar.StartManual()
		}
		if err != nil {
			u.Notify("rave-mate", "Audio record: "+err.Error())
		}
		apply()
	}
	apply()
	// Poll so OBS-synced start/stop reflects here too.
	tick := time.NewTicker(1500 * time.Millisecond)
	u.closers = append(u.closers, tick.Stop)
	goUI("live-rec", func() {
		for range tick.C {
			fyne.Do(apply)
		}
	})
	// right-click the state → copy the recording's full path (display shows the base name)
	stateC := newKitCopyable("file path", state, func() string {
		if s := ar.Status(); s.Path != "" {
			return s.Path
		}
		return state.Text
	})
	return []fyne.CanvasObject{smallCaps("REC"), recBtn, stateC, helpIcon(helpLiveRec)}
}

// liveTimecodeGroup is the house-timecode transport + readout (moved off the Settings
// timecode card); nil when the service is unavailable.
func (u *UI) liveTimecodeGroup() []fyne.CanvasObject {
	tcs := u.svc.Timecode
	if tcs == nil {
		return nil
	}
	enabled := func() bool { return u.svc.Cfg == nil || u.svc.Cfg.Features.Timecode.Enabled }
	now := widget.NewLabelWithStyle("--:--:--:--", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true, Bold: true})
	refresh := func() {
		tc, running := tcs.Now()
		if running {
			now.SetText(tc.String())
		} else {
			now.SetText(tc.String() + " ◼")
		}
	}
	refresh()
	stop := make(chan struct{})
	u.closers = append(u.closers, func() { close(stop) })
	goUI("live-tc", func() {
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fyne.Do(refresh)
			}
		}
	})
	start := newKitButtonWithIcon("START", theme.MediaPlayIcon(), func() {
		if !enabled() {
			u.Notify("Timecode", "Enable the timecode feature first (Settings ▸ Streaming & remote).")
			return
		}
		goUI("live-tc-start", func() {
			if err := tcs.StartClock(); err != nil {
				u.Notify("Timecode", "Start failed: "+err.Error())
			}
		})
	})
	start.SetVariant(kitBtnBrand)
	stopBtn := newKitButtonWithIcon("STOP", theme.MediaStopIcon(), func() {
		goUI("live-tc-stop", func() { tcs.StopClock() })
	})
	nowC := newKitCopyable("timecode", now, func() string { return strings.TrimSuffix(now.Text, " ◼") })
	return []fyne.CanvasObject{smallCaps("TC"), nowC, start, stopBtn, helpIcon(tcHelpCard)}
}

// buildLiveStatusStrip is the bottom kitStatusStrip: OBS/capture/record (left),
// timecode + DMX (center), Twitch + headroom (right). 1 s tick.
func (u *UI) buildLiveStatusStrip() fyne.CanvasObject {
	strip := newKitStatusStrip()
	update := func() {
		strip.SetLeft(u.liveStatusLeft())
		strip.SetCenter(u.liveStatusCenter())
		strip.SetRight(u.liveStatusRight())
	}
	update()
	tick := time.NewTicker(time.Second)
	u.closers = append(u.closers, tick.Stop)
	goUI("live-status", func() {
		for range tick.C {
			fyne.Do(update)
		}
	})
	return container.NewBorder(nil, nil, nil, container.NewCenter(helpIcon(helpLiveStrip)), strip.Object())
}

// liveStatusLeft renders the OBS / capture / native-record zone.
func (u *UI) liveStatusLeft() string {
	var parts []string
	if u.svc.OBS != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.OBS.Enabled {
		o := u.svc.OBS.Status()
		switch {
		case o.Recording:
			parts = append(parts, "OBS ⏺ "+time.Since(o.RecStartedAt).Truncate(time.Second).String())
		case o.Connected:
			parts = append(parts, "OBS ✓")
		default:
			parts = append(parts, "OBS ✕")
		}
	}
	if u.svc.SetCapture != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.SetCapture.Enabled {
		c := u.svc.SetCapture.Snapshot()
		switch {
		case c.Connected:
			parts = append(parts, "CAP "+humanBytes(c.Bytes))
		case c.Reconnecting:
			parts = append(parts, "CAP reconnecting")
		case c.Listening:
			parts = append(parts, "CAP listening")
		}
	}
	if u.svc.AudioRec != nil && u.svc.AudioRec.Status().Recording {
		parts = append(parts, "REC ⏺")
	}
	return strings.Join(parts, " · ")
}

// liveStatusCenter renders the timecode + DMX zone.
func (u *UI) liveStatusCenter() string {
	var parts []string
	if u.svc.Timecode != nil {
		if tc, running := u.svc.Timecode.Now(); running {
			parts = append(parts, "TC "+tc.String())
		}
	}
	if u.svc.DMX != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.DMX.Enabled {
		snap := u.svc.DMX.Status()
		receiving := false
		for _, un := range snap.Universes {
			if un.PPS > 0 {
				receiving = true
			}
		}
		switch {
		case !snap.Running:
			parts = append(parts, "DMX ✕")
		case receiving:
			parts = append(parts, "DMX ⚡")
		default:
			parts = append(parts, "DMX idle")
		}
	}
	return strings.Join(parts, " · ")
}

// liveStatusRight renders the Twitch + system-headroom zone.
func (u *UI) liveStatusRight() string {
	var parts []string
	if m := u.svc.Twitch; m != nil && u.svc.Cfg != nil && u.svc.Cfg.Features.Twitch.Enabled {
		switch {
		case m.SignedIn() && m.Self().Login != "":
			parts = append(parts, "Twitch "+m.Self().Login)
		case m.SignedIn():
			parts = append(parts, "Twitch …")
		}
	}
	if u.svc.Perf != nil {
		if ss := u.svc.Perf.Snapshot(); len(ss) > 0 {
			last := ss[len(ss)-1]
			if last.SysOK {
				parts = append(parts, fmt.Sprintf("free %.0f%% CPU · %.1f GB",
					max(0, 100-last.SysCPUPct), (last.SysMemTotalMB-last.SysMemUsedMB)/1024))
			}
		}
	}
	return strings.Join(parts, " · ")
}
