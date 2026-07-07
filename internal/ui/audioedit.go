package ui

import (
	"context"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
)

// openAudioEditModal is the reusable audio-set editor. It drives the SAME shared transport as the
// video player (playerControls) via a native-audio→vplayer adapter: waveform surface + play/seek +
// per-track jump rail + trim editor (auto-trim to tracks / detected music / last fader, export the
// cut). markers place track boundaries (now a jump rail for audio too); tgt carries the trim bounds.
func (u *UI) openAudioEditModal(title, path string, markers []playerMarker, tgt trimTarget) {
	player := u.setAudioPlayer()

	var wave *waveformView
	var adapter *nativeVPlayer
	wave = newWaveformView(func(sec float64) {
		if adapter != nil {
			adapter.Seek(sec)
			wave.setPlayhead(sec)
		}
	})
	adapter = newNativeVPlayer(player, path, u.svc.Log, u.Notify)

	// Waveform peaks (async - shared analysis cache; a miss decodes once via the probe worker).
	status := mutedInline("Analyzing waveform…")
	debuglog.Go(u.svc.Log, "audioedit-peaks", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		tp, err := u.resolvePeaks(ctx, path)
		fyne.Do(func() {
			if err != nil {
				status.SetText("Waveform unavailable")
				return
			}
			status.SetText("")
			wave.setData(tp.Peaks, tp.DurSec, wave.cues, wave.grid)
		})
	})

	// Surface = waveform + zoom controls + analysis status; the shared transport (playerControls)
	// drives it - same play/seek/jump-rail/trim widget as the video player.
	surface := container.NewBorder(nil, container.NewVBox(waveZoomRow(wave), status), nil, nil, wave)
	st := player.state()
	playing := st.path == path && st.playing
	body := u.playerControls(adapter, surface, path, markers, tgt, pcOpts{wave: wave, autoplaying: playing, preDispatched: true})
	if playing {
		adapter.reattach() // redirect the already-running engine's ticks to this transport
	}

	d := dialog.NewCustom(title, "Close", body, u.win)
	d.Resize(fyne.NewSize(940, 620))
	d.Show()
}

// waveZoomRow is a standalone zoom control row for a waveformView (zoom in/out + fit).
func waveZoomRow(wave *waveformView) fyne.CanvasObject {
	zin := widget.NewButtonWithIcon("", theme.ZoomInIcon(), func() { wave.zoomAt(0.5, 0.5) })
	zout := widget.NewButtonWithIcon("", theme.ZoomOutIcon(), func() { wave.zoomAt(2, 0.5) })
	fit := widget.NewButtonWithIcon("", theme.ViewRestoreIcon(), func() { wave.fit() })
	return container.NewHBox(zin, zout, fit, mutedInline("tap = seek · wheel = zoom · double-tap = fit"))
}
