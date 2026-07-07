package ui

import (
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/musiclib"
)

// openDetachedPlayer pops the now-playing transport into its own window so the controls + waveform
// stay visible while the user browses elsewhere. One window; it FOLLOWS the playing track (rebuilds
// its waveform when the play target changes) and updates via a proxy observer - independent of the
// in-panel AttachUI sink, so both stay live. seed is the panel's track (shown when nothing plays yet).
func (sv *studioView) openDetachedPlayer(seed string) {
	if sv.detachWin != nil { // single window - raise the existing one
		sv.detachWin.Show()
		sv.detachWin.RequestFocus()
		return
	}
	w := fyne.CurrentApp().NewWindow("rave-mate - Now Playing")
	w.SetIcon(sv.u.icon)
	w.Resize(fyne.NewSize(560, 340))
	sv.detachWin = w

	body := container.NewStack()
	curPath := "" // the track the body is currently built for

	// Per-build widgets the observer drives; reassigned on every rebuild.
	var wave *waveformView
	var timeLbl *widget.Label
	var playBtn *widget.Button

	syncPlayIcon := func(path string) {
		if playBtn == nil {
			return
		}
		st := sv.player.state()
		if st.path == path && st.playing && !st.paused {
			playBtn.SetText("Pause")
			playBtn.SetIcon(theme.MediaPauseIcon())
		} else {
			playBtn.SetText("Play")
			playBtn.SetIcon(theme.MediaPlayIcon())
		}
	}

	build := func(path string) fyne.CanvasObject {
		t, inColl := sv.byPath[path]

		wave = newWaveformView(func(sec float64) { // optimistic, fire-and-forget seek (same as in-panel)
			if sv.player.state().path != path {
				return
			}
			wave.setPlayhead(sec)
			timeLbl.SetText(fmtClock(sec) + " / " + fmtClock(wave.durSec))
			sv.player.seek(sec)
		})
		if inColl {
			wave.cues, wave.grid = t.Cues, t.Beatgrid
			if t.DurationSec > 0 { // known duration → seek/zoom live before peaks render
				wave.durSec, wave.viewDur = t.DurationSec, t.DurationSec
			}
		}
		status := mutedLabel("")
		sv.loadPeaks(path, wave, status)

		timeLbl = mutedLabel("0:00 / 0:00")
		playBtn = widget.NewButtonWithIcon("Play", theme.MediaPlayIcon(), func() {
			st := sv.player.state()
			if st.path == path && st.playing { // this file → toggle pause
				sv.player.togglePause()
			} else if err := sv.player.play(path); err != nil {
				sv.u.Notify("rave-mate", "Play failed: "+err.Error())
				return
			}
			syncPlayIcon(path)
		})
		playBtn.Importance = widget.HighImportance
		stopBtn := widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
			if sv.player.state().path == path {
				sv.player.stop()
			}
		})

		// Reflect live position if this file is the one playing.
		if st := sv.player.state(); st.path == path && st.total > 0 {
			wave.setPlayhead(st.cur)
			timeLbl.SetText(fmtClock(st.cur) + " / " + fmtClock(st.total))
		}
		syncPlayIcon(path)

		title := boldLabel(detachTrackTitle(t, inColl, path))
		title.Wrapping = fyne.TextWrapWord
		transport := container.NewVBox(
			timeLbl,
			container.NewGridWithColumns(2, playBtn, stopBtn),
			sv.waveControls(wave),
			status,
		)
		return container.NewBorder(
			container.NewVBox(smallCaps("NOW PLAYING"), title),
			transport, nil, nil, wave)
	}

	rebuild := func(path string) {
		curPath = path
		if path == "" {
			wave, timeLbl, playBtn = nil, nil, nil
			body.Objects = []fyne.CanvasObject{container.NewCenter(mutedLabel("Nothing playing. Press Play on a track."))}
			body.Refresh()
			return
		}
		body.Objects = []fyne.CanvasObject{build(path)}
		body.Refresh()
	}

	onTick := func(cur, total float64) {
		if p := sv.player.state().path; p != curPath { // play target changed → swap the waveform
			rebuild(p)
			return
		}
		if wave != nil {
			wave.setPlayhead(cur)
		}
		if timeLbl != nil && total > 0 {
			timeLbl.SetText(fmtClock(cur) + " / " + fmtClock(total))
		}
	}
	onEnd := func() { rebuild(sv.player.state().path) }
	removeObs := sv.player.addObserver(onTick, onEnd)

	// Seed with whatever is playing, else the panel's track so the window isn't empty.
	start := sv.player.state().path
	if start == "" {
		start = seed
	}
	rebuild(start)

	w.SetContent(container.NewPadded(body))
	w.SetCloseIntercept(func() {
		removeObs()
		sv.detachWin = nil
		w.Close()
	})
	w.Show()
}

// detachTrackTitle is the now-playing window header: "Artist - Title" for a collection track,
// else the file name.
func detachTrackTitle(t musiclib.Track, inColl bool, path string) string {
	if inColl {
		title, artist := strings.TrimSpace(t.Title), strings.TrimSpace(t.Artist)
		switch {
		case title != "" && artist != "":
			return artist + " - " + title
		case title != "":
			return title
		}
	}
	return baseName(path)
}
