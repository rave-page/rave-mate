package ui

import (
	"context"
	"fmt"
	"image"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/mediaplayer"
	"rave.page/mate/internal/mpvembed"
	"rave.page/mate/internal/mpvplayer"
	"rave.page/mate/internal/setedit"
	"rave.page/mate/internal/transcode"
)

// playerMarker is one jump point on the player timeline (a track start within a recording).
type playerMarker struct {
	offset time.Duration
	label  string
}

// trimTarget carries context for a trimmed export: the source set name (shown in the dialog) and
// auto-trim hints (track bounds + duration) that drive the "Trim to tracks / music" buttons.
// Zero value = a standalone file (export still works; auto-trim by tracks is hidden, music-detect
// still available). Unknown bounds are < 0.
type trimTarget struct {
	name            string
	firstTrackSec   float64 // first track start (music start); < 0 = unknown
	lastTrackEndSec float64 // last track end; < 0 = unknown
	lastFaderSec    float64 // last fader-down (true set end); < 0 = unknown / no fader data
	durSec          float64 // total duration (silence tail window); <= 0 = probe/position fallback
}

// newTrimTarget builds a trimTarget with no track hints (standalone files).
func newTrimTarget(name string) trimTarget {
	return trimTarget{name: name, firstTrackSec: -1, lastTrackEndSec: -1, lastFaderSec: -1}
}

// vplayer is the transport the player UI drives - implemented by both the mpv engine
// (mpvplayer.Player, preferred: hardware-accelerated, smooth) and the ffmpeg fallback
// (mediaplayer.Player). Lets the shared controls drive either.
type vplayer interface {
	Play()
	TogglePause() bool
	Seek(sec float64)
	SetVolume(pct int)
	Position() (cur, total float64)
	OnTick(fn func(cur, total float64))
	Close()
}

// buildVideoPlayer creates an in-app player for file, hosted in win: transport (play/pause,
// scrubbing seek, time), a track-jump rail (when markers given), and a collapsible Trim editor (set
// IN/OUT, snap-to-track, export via the transcode presets). Engine preference:
//  1. mpv embedded INTO win (Windows) - GPU decode/present, video inline, no popout;
//  2. mpv in its own window (non-Windows, or embed disabled/unavailable);
//  3. the in-Fyne ffmpeg decoder (no mpv at all).
//
// dismiss (may be nil) is called if the engine's window is closed externally. Returns the content
// + a stop func the caller MUST call on dismiss.
func (u *UI) buildVideoPlayer(win fyne.Window, file string, markers []playerMarker, tgt trimTarget, dismiss func()) (fyne.CanvasObject, func()) {
	if mpvplayer.Available() {
		if u.embedPlayerEnabled() {
			return u.buildEmbeddedMpvPlayer(win, file, markers, tgt, dismiss)
		}
		return u.buildMpvPlayer(file, markers, tgt, dismiss)
	}
	return u.buildFfmpegPlayer(file, markers, tgt)
}

// embedPlayerEnabled reports whether mpv should render inline in the app window (Windows +
// supported + config opt-in). Off → mpv keeps its own popout window.
func (u *UI) embedPlayerEnabled() bool {
	if runtime.GOOS != "windows" || !mpvembed.Supported() {
		return false
	}
	return u.svc.Cfg == nil || u.svc.Cfg.Features.Player.Embed
}

// buildMpvPlayer drives an mpv render window (hardware-accelerated, smooth) via IPC; our Fyne pane
// is just the controller (no frame rendering through Fyne).
func (u *UI) buildMpvPlayer(file string, markers []playerMarker, tgt trimTarget, dismiss func()) (fyne.CanvasObject, func()) {
	pl := mpvplayer.New(u.svc.Log)
	title := filepath.Base(file)
	if err := pl.Open(file, title); err != nil {
		pl.Close()
		return container.NewCenter(mutedLabel("Can't open media: " + err.Error())), func() {}
	}
	if dismiss != nil {
		pl.OnClose(func() { fyne.Do(dismiss) }) // mpv window closed by the user → dismiss the controller
	}
	surface := container.NewCenter(container.NewVBox(
		widget.NewIcon(theme.MediaPlayIcon()),
		mutedLabel("Playing in the mpv video window"),
		mutedInline("Seek, jump tracks, and trim with the controls below."),
	))
	return u.playerControls(pl, surface, file, markers, tgt, pcOpts{autoplaying: true, hasVolume: true}), pl.Close
}

// buildFfmpegPlayer is the no-mpv fallback: ffmpeg decodes frames that we push into a Fyne canvas
// (works everywhere, but heavier - see mpv preference above).
func (u *UI) buildFfmpegPlayer(file string, markers []playerMarker, tgt trimTarget) (fyne.CanvasObject, func()) {
	pl := mediaplayer.New(u.svc.Log)
	if err := pl.Open(file, 1280, 720); err != nil {
		pl.Close()
		return container.NewCenter(mutedLabel("Can't open media: " + err.Error())), func() {}
	}
	st := pl.State()

	frame := canvas.NewImageFromImage(image.NewNRGBA(image.Rect(0, 0, 16, 9)))
	frame.FillMode = canvas.ImageFillContain
	frame.SetMinSize(fyne.NewSize(480, 270))
	var surface fyne.CanvasObject = frame
	if !st.HasVideo {
		surface = container.NewCenter(container.NewVBox(
			widget.NewIcon(theme.MediaMusicIcon()), mutedLabel("Audio only")))
	}

	// Phase-locked repaint: the player calls back once per displayed frame; coalesce so at most one
	// canvas refresh is queued on the UI thread at a time (no fixed-rate full-frame upload storm).
	if st.HasVideo {
		var pending atomic.Bool
		pl.OnFrame(func() {
			if pending.Swap(true) {
				return
			}
			fyne.Do(func() {
				pending.Store(false)
				if f := pl.Frame(); f != nil {
					frame.Image = f
					canvas.Refresh(frame)
				}
			})
		})
	}
	pl.Play()
	return u.playerControls(pl, surface, file, markers, tgt, pcOpts{autoplaying: true, hasVolume: true}), pl.Close
}

// playerControls assembles the shared transport + now-track readout + trim editor + jump rail
// pcOpts carries the audio/video variance for the shared transport: video engines auto-start with a
// volume control and a video surface; the native audio engine starts idle, has no volume yet, and
// drives a waveform playhead. Zero value = a paused/idle, no-volume, no-wave player.
type pcOpts struct {
	autoplaying   bool          // engine already playing (video Open) vs idle (audio editor)
	hasVolume     bool          // show the volume control (video only for now)
	wave          *waveformView // if set, move its playhead on each tick (audio)
	preDispatched bool          // engine delivers tick/end ON the UI thread already (the audio proxy
	// dispatches via fyne.Do); video engines fire from a decode/IPC goroutine, so playerControls must
	// fyne.Do them. Guards against a double-dispatch (nested fyne.Do drops the update).
}

// uiDo runs fn on the Fyne UI thread. preDispatched engines already deliver on it; others fire from
// a background goroutine and need fyne.Do. Avoids a nested fyne.Do (which silently drops the call).
func uiDo(preDispatched bool, fn func()) {
	if preDispatched {
		fn()
		return
	}
	fyne.Do(fn)
}

// around surface, driving pl. Video callers pass autoplaying=true (Open already started); the audio
// editor passes a waveform + hasVolume=false and starts idle.
func (u *UI) playerControls(pl vplayer, surface fyne.CanvasObject, file string, markers []playerMarker, tgt trimTarget, opts pcOpts) fyne.CanvasObject {
	_, total0 := pl.Position()
	timeLbl := mutedInline("0:00 / 0:00")
	seek := widget.NewSlider(0, 1)
	seek.Step = 0.1
	if total0 > 0 {
		seek.Max = total0
	}
	var lastInput time.Time
	curIdx := -1              // current track index in the jump rail (-1 = before track 1)
	var jumpList *widget.List // assigned below; OnTick highlights/auto-scrolls it

	var playBtn *widget.Button
	toPlay := func() { playBtn.SetText("Play"); playBtn.SetIcon(theme.MediaPlayIcon()) }
	toPause := func() { playBtn.SetText("Pause"); playBtn.SetIcon(theme.MediaPauseIcon()) }

	nowTrack := mutedInline("")
	if len(markers) == 0 {
		nowTrack.Hide()
	}

	pl.OnTick(func(cur, total float64) {
		uiDo(opts.preDispatched, func() {
			if total > 0 && seek.Max != total {
				seek.Max = total
			}
			if time.Since(lastInput) > 600*time.Millisecond {
				seek.SetValue(cur)
			}
			if opts.wave != nil {
				opts.wave.setPlayhead(cur)
			}
			timeLbl.SetText(fmtClock(cur) + " / " + fmtClock(total))
			if len(markers) > 0 {
				nowTrack.SetText(currentMarkerLabel(markers, cur))
				if idx := markerIndexAt(markers, cur); idx != curIdx && jumpList != nil {
					curIdx = idx
					jumpList.Refresh() // re-highlight the now-playing row
					if idx >= 0 {
						jumpList.ScrollTo(idx) // keep the current track in view (track-based scroll)
					}
				}
			}
		})
	})
	seek.OnChanged = func(float64) { lastInput = time.Now() }
	seek.OnChangeEnded = func(val float64) { pl.Seek(val) }

	playBtn = widget.NewButtonWithIcon("Pause", theme.MediaPauseIcon(), func() {
		if pl.TogglePause() {
			toPlay()
		} else {
			toPause()
		}
	})
	playBtn.Importance = widget.HighImportance
	if !opts.autoplaying { // audio editor opens idle
		toPlay()
	}
	// Reset the transport when playback finishes (optional capability - the native audio adapter).
	if pe, ok := pl.(interface{ OnEnd(func()) }); ok {
		pe.OnEnd(func() {
			uiDo(opts.preDispatched, func() {
				toPlay()
				if opts.wave != nil {
					opts.wave.setPlayhead(-1)
				}
				timeLbl.SetText("0:00 / 0:00")
			})
		})
	}
	// Prev/next jump to track starts when markers exist, else ±10 s.
	prevFn := func() { seekRel(pl, -10) }
	nextFn := func() { seekRel(pl, 10) }
	if len(markers) > 0 {
		prevFn = func() { seekAdjacentMarker(pl, markers, -1) }
		nextFn = func() { seekAdjacentMarker(pl, markers, 1) }
	}
	back := widget.NewButtonWithIcon("", theme.MediaSkipPreviousIcon(), prevFn)
	fwd := widget.NewButtonWithIcon("", theme.MediaSkipNextIcon(), nextFn)

	// Volume: 0–100, starts at full (video only - the native audio engine has none yet). mpv applies
	// it instantly; the ffmpeg fallback scales PCM.
	rightBox := container.NewHBox()
	if opts.hasVolume {
		volIcon := widget.NewIcon(theme.VolumeUpIcon())
		vol := widget.NewSlider(0, 100)
		vol.Value = 100
		vol.OnChanged = func(v float64) { pl.SetVolume(int(v)) }
		volBox := container.NewGridWrap(fyne.NewSize(120, 34), vol)
		rightBox.Add(volIcon)
		rightBox.Add(volBox)
	}
	rightBox.Add(timeLbl)

	transport := container.NewBorder(nil, nil,
		container.NewHBox(back, playBtn, fwd), rightBox, seek)

	bottom := container.NewVBox(transport, u.trimEditor(pl, file, markers, tgt))
	main := container.NewBorder(nowTrack, bottom, nil, nil, surface)
	if len(markers) == 0 {
		return main
	}
	jumpList = widget.NewList(
		func() int { return len(markers) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Truncation = fyne.TextTruncateEllipsis
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i < 0 || i >= len(markers) {
				return
			}
			lbl := o.(*widget.Label)
			txt := fmtClock(markers[i].offset.Seconds()) + "  " + markers[i].label
			if i == curIdx {
				lbl.TextStyle = fyne.TextStyle{Bold: true}
				txt = "▶ " + txt
			} else {
				lbl.TextStyle = fyne.TextStyle{}
			}
			lbl.SetText(txt)
		},
	)
	jumpList.OnSelected = func(i widget.ListItemID) {
		if i >= 0 && i < len(markers) {
			pl.Seek(markers[i].offset.Seconds())
			lastInput = time.Now()
			seek.SetValue(markers[i].offset.Seconds())
			jumpList.UnselectAll() // selection is a one-shot jump, not persistent state
		}
	}
	rail := container.NewBorder(smallCaps("TRACKS - TAP TO JUMP"), nil, nil, nil, jumpList)
	return container.New(newAdaptiveSplit(0.68), main, shrinkWidth(220, rail))
}

// markerIndexAt returns the index of the track playing at cur (last marker at/before cur), or -1.
func markerIndexAt(markers []playerMarker, cur float64) int {
	idx := -1
	for i, m := range markers {
		if m.offset.Seconds() <= cur+0.001 {
			idx = i
		} else {
			break
		}
	}
	return idx
}

// trimPlayer is the minimal player surface the trim editor needs (current + total seconds) - both
// the video vplayer and the native audio player (via an adapter) satisfy it.
type trimPlayer interface {
	Position() (cur, total float64)
}

// trimEditor builds the collapsible trim/cut controls: set IN/OUT on the playhead, snap to track
// boundaries, and export the kept range as an encoding job via the existing transcode presets.
func (u *UI) trimEditor(pl trimPlayer, file string, markers []playerMarker, tgt trimTarget) fyne.CanvasObject {
	inSec, outSec := 0.0, -1.0 // outSec<0 = to end
	inLbl := mutedInline("IN 0:00")
	outLbl := mutedInline("OUT end")
	resultLbl := mutedInline("")
	update := func() {
		inLbl.SetText("IN " + fmtClock(inSec))
		if outSec < 0 {
			outLbl.SetText("OUT end")
		} else {
			outLbl.SetText("OUT " + fmtClock(outSec))
		}
		end := outSec
		if end < 0 {
			_, end = pl.Position()
		}
		resultLbl.SetText("→ keeps " + fmtClock(max(end-inSec, 0)))
	}
	setIn := widget.NewButton("Set IN", func() {
		inSec, _ = pl.Position()
		if outSec >= 0 && outSec <= inSec {
			outSec = -1
		}
		update()
	})
	setOut := widget.NewButton("Set OUT", func() {
		o, _ := pl.Position()
		if o <= inSec {
			o = -1
		}
		outSec = o
		update()
	})
	clr := widget.NewButton("Clear", func() { inSec, outSec = 0, -1; update() })
	exportBtn := widget.NewButtonWithIcon("Export cut…", theme.DocumentSaveIcon(), func() {
		u.exportTrim(file, tgt, inSec, outSec)
	})
	exportBtn.Importance = widget.HighImportance

	row := container.NewHBox(setIn, setOut, clr, inLbl, outLbl, resultLbl)
	if len(markers) > 0 {
		snapIn := widget.NewButton("IN ⟸ track", func() { cur, _ := pl.Position(); inSec = markerStartAt(markers, cur); update() })
		snapOut := widget.NewButton("OUT ⟸ next", func() {
			cur, _ := pl.Position()
			outSec = nextMarkerStart(markers, cur)
			if outSec <= inSec {
				outSec = -1
			}
			update()
		})
		row.Add(snapIn)
		row.Add(snapOut)
	}

	// Auto-trim row: snap IN/OUT to the tracklist bounds (first track start → last track end), to the
	// detected music region (leading/trailing silence), or to the last fader-down. The last-track end
	// is the true set end unless the DJ talked after the mix - then "Trim to last fader" (the instant
	// the final channel fader dropped, from recorded MIDI / Traktor on-air level) is the accurate end.
	autoRow := container.NewHBox(mutedInline("Auto:"))
	if tgt.firstTrackSec >= 0 || tgt.lastTrackEndSec >= 0 {
		autoRow.Add(widget.NewButton("Trim to tracks", func() {
			if tgt.firstTrackSec >= 0 {
				inSec = tgt.firstTrackSec
			}
			if tgt.lastTrackEndSec > 0 {
				outSec = tgt.lastTrackEndSec
			}
			update()
		}))
	}
	if tgt.lastFaderSec > 0 {
		autoRow.Add(widget.NewButton("Trim to last fader", func() {
			if tgt.firstTrackSec >= 0 {
				inSec = tgt.firstTrackSec
			}
			outSec = tgt.lastFaderSec
			update()
		}))
	}
	music := widget.NewButton("Trim to music", nil)
	music.OnTapped = func() {
		total := tgt.durSec
		if total <= 0 {
			_, total = pl.Position()
		}
		music.SetText("Detecting…")
		music.Disable()
		debuglog.Go(u.svc.Log, "trim-silence", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			s, err := setedit.DetectSilence(ctx, file, total)
			fyne.Do(func() {
				music.SetText("Trim to music")
				music.Enable()
				if err != nil {
					u.Notify("Trim", "Silence detection failed: "+err.Error())
					return
				}
				inSec = s.LeadEndSec
				if s.TailStartSec > 0 && (total <= 0 || s.TailStartSec < total) {
					outSec = s.TailStartSec
				}
				update()
			})
		})
	}
	autoRow.Add(music)
	update()

	section := container.NewVBox(widget.NewSeparator(), container.NewHScroll(row), container.NewHScroll(autoRow), container.NewHBox(exportBtn))
	section.Hide()
	toggle := widget.NewButtonWithIcon("Trim / cut", theme.ContentCutIcon(), func() {
		if section.Visible() {
			section.Hide()
		} else {
			section.Show()
		}
	})
	return container.NewVBox(toggle, section)
}

// markerStartAt returns the start offset (seconds) of the track playing at cur.
func markerStartAt(markers []playerMarker, cur float64) float64 {
	s := 0.0
	for _, m := range markers {
		if o := m.offset.Seconds(); o <= cur+0.001 {
			s = o
		} else {
			break
		}
	}
	return s
}

// nextMarkerStart returns the start offset of the next track after cur (-1 if none).
func nextMarkerStart(markers []playerMarker, cur float64) float64 {
	for _, m := range markers {
		if o := m.offset.Seconds(); o > cur+0.1 {
			return o
		}
	}
	return -1
}

// exportTrim asks for an encode preset (default the instant Lossless Remux) and queues the cut.
func (u *UI) exportTrim(file string, tgt trimTarget, inSec, outSec float64) {
	if u.svc.Workers == nil {
		u.Notify("rave-mate", "Worker pool unavailable")
		return
	}
	var custom []transcode.Preset
	if u.svc.Cfg != nil {
		custom = u.svc.Cfg.Features.Transcode.Presets
	}
	presets := transcode.AllPresets(custom)
	labels := make([]string, len(presets))
	def := 0
	for i, p := range presets {
		labels[i] = p.Label
		if p.ID == "remux" {
			def = i
		}
	}
	sel := widget.NewSelect(labels, nil)
	sel.SetSelectedIndex(def)
	info := mutedLabel("\"Lossless Remux\" is instant (no re-encode) but cuts snap to keyframes. Pick an encode preset for frame-exact cuts.")
	info.Wrapping = fyne.TextWrapWord
	body := container.NewVBox(widget.NewLabel("Encode preset for the cut:"), sel, info)
	title := "Export cut"
	if tgt.name != "" {
		title += " - " + tgt.name
	}
	// The embedded video's HWND floats above the Fyne canvas; hide it while this dialog is up so
	// the preset picker isn't covered. Restored on confirm/cancel.
	restore := u.suspendEmbeds()
	dialog.ShowCustomConfirm(title, "Export", "Cancel", body, func(ok bool) {
		restore()
		if !ok {
			return
		}
		i := sel.SelectedIndex()
		if i < 0 || i >= len(presets) {
			return
		}
		u.runTrim(file, presets[i], inSec, outSec)
	}, u.win)
}

// runTrim queues the cut as a transcode.run job (TrimStart/TrimEnd) off-thread, output beside the
// source. Reuses the existing worker + preset pipeline (HW encode + loudness all apply).
func (u *UI) runTrim(file string, preset transcode.Preset, inSec, outSec float64) {
	out := trimOutPath(file, preset)
	end := outSec
	if end < 0 {
		end = 0 // transcode.run treats trimEnd<=start as "to end"
	}
	params := map[string]any{"input": file, "output": out, "preset": preset, "trimStart": inSec, "trimEnd": end}
	u.Notify("rave-mate", "Exporting cut ("+preset.Label+")…")
	debuglog.Go(u.svc.Log, "trim-export", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
		defer cancel()
		if _, err := u.svc.Workers.RunStream(ctx, "transcode", "transcode.run", params, nil); err != nil {
			u.Notify("rave-mate", "Export failed: "+err.Error())
			return
		}
		u.Notify("rave-mate", "Cut exported → "+filepath.Base(out))
	})
}

// trimOutPath builds a unique "<base>-cut[-n].<container>" path beside the source.
func trimOutPath(file string, preset transcode.Preset) string {
	dir := filepath.Dir(file)
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	ext := preset.Container
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(file), ".")
	}
	out := filepath.Join(dir, base+"-cut."+ext)
	for i := 2; fileExists(out); i++ {
		out = filepath.Join(dir, fmt.Sprintf("%s-cut-%d.%s", base, i, ext))
	}
	return out
}

// currentMarkerLabel returns "♪ <track>" for the last marker at or before cur ("" if none yet).
func currentMarkerLabel(markers []playerMarker, cur float64) string {
	lbl := ""
	for _, m := range markers {
		if m.offset.Seconds() <= cur+0.001 {
			lbl = m.label
		} else {
			break // markers are time-ordered
		}
	}
	if lbl == "" {
		return ""
	}
	return "♪ " + lbl
}

// seekAdjacentMarker jumps to the previous (dir<0) or next (dir>0) track-start marker.
func seekAdjacentMarker(pl vplayer, markers []playerMarker, dir int) {
	cur, _ := pl.Position()
	if dir > 0 {
		for _, m := range markers {
			if m.offset.Seconds() > cur+0.1 {
				pl.Seek(m.offset.Seconds())
				return
			}
		}
		return
	}
	target := 0.0
	for _, m := range markers { // last marker strictly before now (1.5s grace = "restart this track")
		if o := m.offset.Seconds(); o < cur-1.5 {
			target = o
		}
	}
	pl.Seek(target)
}

// seekRel jumps the player by delta seconds from the current position (clamped at 0).
func seekRel(pl vplayer, delta float64) {
	cur, _ := pl.Position()
	pos := cur + delta
	if pos < 0 {
		pos = 0
	}
	pl.Seek(pos)
}

// openPlayerModal shows the player in a dialog (video embedded inline where supported) with a
// Pop-out button (re-opens in its own window).
func (u *UI) openPlayerModal(title, file string, markers []playerMarker, tgt trimTarget) {
	var d dialog.Dialog
	content, stop := u.buildVideoPlayer(u.win, file, markers, tgt, func() {
		if d != nil {
			d.Hide()
		}
	})
	popout := widget.NewButtonWithIcon("Pop out", theme.ViewFullScreenIcon(), func() {
		d.Hide() // fires SetOnClosed → stops this player; then re-open in a window
		u.openPlayerWindow(title, file, markers, tgt)
	})
	body := container.NewBorder(nil, container.NewHBox(popout), nil, nil, content)
	d = dialog.NewCustom(title, "Close", body, u.win)
	d.SetOnClosed(stop)
	d.Resize(fyne.NewSize(960, 640))
	d.Show()
}

// openPlayerWindow shows the player in its own resizable window (the pop-out target).
// Default engine is the Gio player window (tri-state features.player.gioWindow); legacy
// Fyne window on explicit opt-out, darwin, or when mpv is missing (Gio path needs it -
// the Fyne player still has its own decoder fallback). Markers are Fyne-only until the
// Gio jump-marker rail lands (GIO_MIGRATION.md phase 3).
func (u *UI) openPlayerWindow(title, file string, markers []playerMarker, tgt trimTarget) {
	if u.useGioPlayer() {
		if err := u.openGioPlayerWindow(title, file, tgt); err == nil {
			return
		}
	}
	w := u.app.NewWindow(title)
	content, stop := u.buildVideoPlayer(w, file, markers, tgt, func() { w.Close() })
	w.SetContent(content)
	w.Resize(fyne.NewSize(900, 600))
	w.SetOnClosed(stop)
	w.Show()
}
