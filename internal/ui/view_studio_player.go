package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/cuesheet"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/musiclib"
)

// videoPlayerLaunch builds the in-pane video controls: open the enriched mediaplayer (scrub /
// jump-to-track / trim → encode) as a modal or its own window, plus an external-open fallback.
// Track markers come from a sibling .cue sidecar when present (jump rail + trim snapping).
func (sv *studioView) videoPlayerLaunch(path string) fyne.CanvasObject {
	name := filepath.Base(path)
	markers := cueMarkersFor(path)
	tgt := newTrimTarget(name)
	play := widget.NewButtonWithIcon("Play video", theme.MediaPlayIcon(), func() {
		sv.u.openPlayerModal(name, path, markers, tgt)
	})
	play.Importance = widget.HighImportance
	popout := widget.NewButtonWithIcon("Pop-out window", theme.ViewFullScreenIcon(), func() {
		sv.u.openPlayerWindow(name, path, markers, tgt)
	})
	popout.Importance = widget.LowImportance
	open := widget.NewButtonWithIcon("Open externally", theme.ComputerIcon(), func() { openFile(path) })
	open.Importance = widget.LowImportance
	box := container.NewVBox(container.NewGridWithColumns(2, play, popout), open)
	if len(markers) > 0 {
		box.Add(mutedInline(fmt.Sprintf("%d track markers from .cue - jump-to-track + trim snapping in the player.", len(markers))))
	} else {
		box.Add(mutedInline("Scrub, jump ±10s, and trim/cut to an encode job in the player."))
	}
	return box
}

// cueMarkersFor returns track-start markers parsed from a sibling "<base>.cue" sidecar (empty if
// none). Shared by the Library video player; the recorder builds its own from live MIDI offsets.
func cueMarkersFor(path string) []playerMarker {
	cuePath := strings.TrimSuffix(path, filepath.Ext(path)) + ".cue"
	sh, err := cuesheet.ParseFile(cuePath)
	if err != nil || len(sh.Tracks) == 0 {
		return nil
	}
	ms := make([]playerMarker, 0, len(sh.Tracks))
	for _, t := range sh.Tracks {
		label := strings.TrimSpace(t.Title)
		if p := strings.TrimSpace(t.Performer); p != "" {
			if label != "" {
				label = p + " - " + label
			} else {
				label = p
			}
		}
		ms = append(ms, playerMarker{offset: t.Start, label: orTrack(label)})
	}
	return ms
}

// Media detail player: an interactive waveform (peak buckets via the probe worker;
// zoom/pan/seek, cue + beatgrid overlays - see waveform.go) plus, for natively-
// decodable audio (mp3/wav/flac/ogg), an in-app transport (player.go). Key/BPM and
// the cue list render as colored pills. Other audio + all video fall back to "Open".

var mediaExts = map[string]bool{
	".mp3": true, ".wav": true, ".flac": true, ".aac": true, ".m4a": true, ".opus": true,
	".ogg": true, ".oga": true, ".aiff": true, ".aif": true, ".mp4": true, ".mov": true,
	".mkv": true, ".webm": true, ".avi": true, ".m4v": true, ".wmv": true, ".flv": true,
}

func isMediaPath(path string) bool { return mediaExts[strings.ToLower(filepath.Ext(path))] }

// videoExts are the container formats handled as video (waveform analysis would force a
// full-file decode - pointless + heavy for long recordings, so it's skipped; see playerPanel).
var videoExts = map[string]bool{
	".mp4": true, ".mov": true, ".mkv": true, ".webm": true,
	".avi": true, ".m4v": true, ".wmv": true, ".flv": true,
}

// isVideoPath reports whether path is a video container.
func isVideoPath(path string) bool { return videoExts[strings.ToLower(filepath.Ext(path))] }

// stopPlayer halts in-app playback (selection change / window close / app exit).
func (sv *studioView) stopPlayer() {
	if sv.player != nil {
		sv.player.stop()
	}
}

// trackPeaks is the persisted waveform analysis: peak buckets + exact duration.
type trackPeaks struct {
	DurSec float64 `json:"d"`
	Peaks  []byte  `json:"p"`
}

// playerPanel builds the waveform + (for decodable audio) the native transport for one
// on-disk media file. Collection tracks contribute cues/beatgrid/key/BPM overlays.
func (sv *studioView) playerPanel(path string) fyne.CanvasObject {
	status := mutedLabel("")

	// Video: skip waveform analysis (a full-file decode of an hours-long recording) and play in
	// the enriched mediaplayer (scrub / jump-to-track / trim → encode) via a modal or pop-out.
	if isVideoPath(path) {
		return sv.videoPlayerLaunch(path)
	}

	t, inColl := sv.byPath[path]

	// seekFn is assigned once the transport (timeLbl/onTick) exists; the waveform + cue pills
	// reference it so a tap is instant (optimistic playhead) with the audio seek dispatched
	// off the UI thread - no 1–2s freeze on a slow decoder Seek.
	var seekFn func(sec float64)
	wave := newWaveformView(func(sec float64) {
		if seekFn != nil {
			seekFn(sec)
		}
	})
	if inColl {
		wave.cues, wave.grid = t.Cues, t.Beatgrid
		if t.DurationSec > 0 {
			// Known duration → enable seek/zoom/pan immediately, before the peaks finish
			// rendering (smooth navigation while the waveform is still "Analyzing…").
			wave.durSec, wave.viewDur = t.DurationSec, t.DurationSec
		}
	}
	sv.loadPeaks(path, wave, status)

	box := container.NewVBox()
	if row := trackPillRow(t, inColl); row != nil {
		box.Add(row)
	}
	box.Add(wave)
	box.Add(sv.waveControls(wave))
	if cueRow := sv.cuePillRow(t, inColl, func(sec float64) {
		if seekFn != nil {
			seekFn(sec)
		}
	}); cueRow != nil {
		box.Add(cueRow)
	}

	if !isPlayable(path) {
		note := mutedLabel("In-app playback: MP3 / WAV / FLAC / OGG. Use Open for this file.")
		note.Wrapping = fyne.TextWrapWord
		box.Add(note)
		box.Add(status)
		return box
	}

	timeLbl := mutedLabel("0:00 / 0:00")

	var playBtn *widget.Button
	toPlay := func() { playBtn.SetText("Play"); playBtn.SetIcon(theme.MediaPlayIcon()) }
	toPause := func() { playBtn.SetText("Pause"); playBtn.SetIcon(theme.MediaPauseIcon()) }

	onEnd := func() {
		toPlay()
		wave.setPlayhead(-1)
		timeLbl.SetText("0:00 / 0:00")
	}
	onTick := func(cur, total float64) {
		if total <= 0 {
			return
		}
		wave.setPlayhead(cur)
		timeLbl.SetText(fmtClock(cur) + " / " + fmtClock(total))
	}
	sv.waveSink = func(p string) { // post-seek instant sync (tap on the waveform)
		if p != path {
			return
		}
		if cur, total, ok := sv.player.position(); ok {
			onTick(cur, total)
		}
	}

	// Optimistic, non-blocking seek: jump the playhead + time label NOW (UI thread), run the
	// real audio seek off-thread, then reconcile. A slow MP3 Seek never freezes the click.
	seekFn = func(sec float64) {
		if sv.player.state().path != path {
			return
		}
		// Optimistic: jump the playhead + time NOW; the subprocess seek is fire-and-forget and the
		// next position tick (≤200ms) reconciles. No round-trip, no stall.
		wave.setPlayhead(sec)
		timeLbl.SetText(fmtClock(sec) + " / " + fmtClock(wave.durSec))
		sv.player.seek(sec)
	}

	playBtn = widget.NewButtonWithIcon("Play", theme.MediaPlayIcon(), func() {
		st := sv.player.state()
		if st.path == path && st.playing { // this file is the one playing → toggle pause
			if paused := sv.player.togglePause(); paused {
				toPlay()
			} else {
				toPause()
			}
			return
		}
		sv.player.attachUI(onTick, onEnd) // becomes the now-playing view
		if err := sv.player.play(path); err != nil {
			sv.u.Notify("rave-mate", "Play failed: "+err.Error())
			return
		}
		toPause()
	})
	playBtn.Importance = widget.HighImportance
	stopBtn := widget.NewButtonWithIcon("Stop", theme.MediaStopIcon(), func() {
		if sv.player.state().path == path {
			sv.player.stop()
		}
		onEnd()
	})

	// Reflect live state if THIS file is the one currently playing (panels rebuild on
	// selection; playback is decoupled from selection). Otherwise detach any stale sink
	// left by a previous panel so we don't drive dead widgets.
	if st := sv.player.state(); st.path == path && st.playing {
		sv.player.attachUI(onTick, onEnd)
		if st.total > 0 {
			wave.setPlayhead(st.cur)
			timeLbl.SetText(fmtClock(st.cur) + " / " + fmtClock(st.total))
		}
		if st.paused {
			toPlay()
		} else {
			toPause()
		}
	} else {
		sv.player.attachUI(nil, nil)
	}

	detachBtn := widget.NewButtonWithIcon("Detach", theme.ComputerIcon(), func() { sv.openDetachedPlayer(path) })
	detachBtn.Importance = widget.LowImportance

	box.Add(timeLbl)
	box.Add(container.NewGridWithColumns(2, playBtn, stopBtn))
	box.Add(detachBtn)
	box.Add(status)
	return box
}

// trackPillRow renders the analyzed-metadata pills (key colored, BPM, rating).
// nil when the file isn't in the collection or carries nothing to show.
func trackPillRow(t musiclib.Track, inColl bool) fyne.CanvasObject {
	if !inColl {
		return nil
	}
	var pills []fyne.CanvasObject
	if k, ok := musiclib.ParseKey(t.Key); ok {
		pills = append(pills, newPill(keyLabel(k), colBrandBase, colBackground, nil))
	} else if t.Key != "" {
		pills = append(pills, newPill(t.Key, colSecondary, colMuted, nil))
	}
	if t.BPM > 0 {
		pills = append(pills, newPill(fmt.Sprintf("%.0f BPM", t.BPM), colSecondary, colForeground, nil))
	}
	if r := musiclib.StarRating(t.Rating); r > 0 {
		pills = append(pills, newPill(strings.Repeat("★", r), colSecondary, colBrandAmber, nil))
	}
	if len(pills) == 0 {
		return nil
	}
	return WrapActions(pills...)
}

// waveControls is the zoom row under the waveform.
func (sv *studioView) waveControls(wave *waveformView) fyne.CanvasObject {
	zin := widget.NewButtonWithIcon("", theme.ZoomInIcon(), func() { wave.zoomAt(0.5, 0.5) })
	zout := widget.NewButtonWithIcon("", theme.ZoomOutIcon(), func() { wave.zoomAt(2, 0.5) })
	fit := widget.NewButtonWithIcon("", theme.ZoomFitIcon(), wave.fit)
	for _, b := range []*widget.Button{zin, zout, fit} {
		b.Importance = widget.LowImportance
	}
	hint := mutedInline("tap = seek · wheel = zoom · drag = pan")
	return container.NewBorder(nil, nil, container.NewHBox(zin, zout, fit), nil, hint)
}

// cuePillRow lists the track's cue points as kind-colored pills (tap = seek there via the
// shared optimistic seekFn). Grid anchors are omitted (they're beatgrid, not nav targets).
func (sv *studioView) cuePillRow(t musiclib.Track, inColl bool, seek func(sec float64)) fyne.CanvasObject {
	if !inColl || len(t.Cues) == 0 {
		return nil
	}
	var pills []fyne.CanvasObject
	for _, c := range t.Cues {
		if c.Kind == musiclib.CueGrid {
			continue
		}
		label := cuePillLabel(c)
		startSec := c.StartMs / 1000
		pills = append(pills, newPill(label, cueKindColor(c.Kind), colBackground, func() {
			seek(startSec)
		}))
	}
	if len(pills) == 0 {
		return nil
	}
	return WrapActions(pills...)
}

// cuePillLabel renders "1 · Intro 0:32" (hotcue slot, name, position).
func cuePillLabel(c musiclib.CuePoint) string {
	parts := make([]string, 0, 3)
	if c.Kind == musiclib.CueHot && c.Hotcue >= 0 {
		parts = append(parts, fmt.Sprintf("%d", c.Hotcue+1))
	}
	name := strings.TrimSpace(c.Name)
	if name != "" && name != "n.n." { // Traktor's unnamed-cue placeholder
		parts = append(parts, name)
	} else if c.Kind != musiclib.CueHot {
		parts = append(parts, string(c.Kind))
	}
	parts = append(parts, fmtClock(c.StartMs/1000))
	return strings.Join(parts, " · ")
}

// fmtClock formats seconds as m:ss (or h:mm:ss past an hour) for the player position.
func fmtClock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// loadPeaks feeds wave from the per-path peaks cache, else computes once in the probe
// worker (detached; deduped so re-renders of the same file never re-spawn ffmpeg).
func (sv *studioView) loadPeaks(path string, wave *waveformView, status *widget.Label) {
	apply := func(tp trackPeaks) {
		wave.setData(tp.Peaks, tp.DurSec, wave.cues, wave.grid)
		status.SetText("")
	}
	sv.waveMu.Lock()
	if tp, ok := sv.peakCache[path]; ok { // already computed → instant, no worker
		sv.waveMu.Unlock()
		apply(tp)
		return
	}
	if sv.waveLoading[path] || sv.u.svc.Workers == nil { // in-flight or no worker
		sv.waveMu.Unlock()
		return
	}
	sv.waveLoading[path] = true
	sv.waveMu.Unlock()

	status.SetText("Analyzing waveform…")
	go func() {
		defer debuglog.Recover(sv.u.svc.Log, "waveform", false)
		defer func() {
			sv.waveMu.Lock()
			delete(sv.waveLoading, path)
			sv.waveMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		// Shared resolver: persisted cache (mtime-keyed) else one probe-worker run.
		tp, err := sv.u.resolvePeaks(ctx, path)
		if err != nil {
			fyne.Do(func() { status.SetText("") })
			return
		}
		sv.cachePeaks(path, tp)
		// Paint this panel's waveform; if the panel was rebuilt, refreshing the detached
		// widget is a harmless no-op and the new panel paints from cache instantly.
		fyne.Do(func() { apply(tp) })
	}()
}

// cachePeaks stores computed peaks in the in-memory per-path cache.
func (sv *studioView) cachePeaks(path string, tp trackPeaks) {
	sv.waveMu.Lock()
	sv.peakCache[path] = tp
	sv.waveMu.Unlock()
}

// fileMtime returns a file's modification time in unix-nano (the analysis-cache invalidation
// key), or 0 if it can't be stat'd.
func fileMtime(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime().UnixNano()
	}
	return 0
}
