// Package playerwin is the first production Gio surface of the Fyne→Gio migration: a
// media player window. mpv keeps its GPU decode→present path and renders INTO the Gio
// window - on Windows the window's native HWND (app.Win32ViewEvent) parents an
// mpvembed child window passed to mpv via --wid; a reserved video rect sits above a
// dense giokit transport bar (play/pause, seek + time, ±10s, volume, trim IN/OUT +
// export-cut hook) and a status strip. Non-Windows (or embed failure) falls back to
// mpv's own popout window with the transport still driving it - never a crash.
package playerwin

import (
	"fmt"
	"image"
	"sync"
	"time"

	"gioui.org/app"

	"rave.page/mate/internal/deckclock"
	"rave.page/mate/internal/giokit"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mpvembed"
	"rave.page/mate/internal/mpvplayer"
)

// hwndDeadline: give up waiting for the native view handle and fall back to popout.
const hwndDeadline = 5 * time.Second

// Config configures Open.
type Config struct {
	File  string
	Title string
	Mpv   mpvplayer.Options // VO/HWDec/Profile/Extra; WID is managed by playerwin
	Log   *logbus.Bus       // may be nil

	// OnExportCut receives the trim range (outSec < 0 = to end) when the user hits
	// Export. Runs on the window's event-loop goroutine - hop threads inside if needed
	// (e.g. fyne.Do). nil hides the export button.
	OnExportCut func(inSec, outSec float64)
	// LoadPeaks resolves the track's waveform peaks (the shared probe-analysis source)
	// when the strip first opens. Must return fast and deliver via done from any
	// goroutine (analysis runs once; video files aren't decoded until the user asks).
	// nil hides the waveform toggle.
	LoadPeaks func(done func(peaks []byte, durSec float64, err error))
	// OnClosed runs once when the window is gone (user close, Close(), or mpv exit).
	OnClosed func()
}

// Window is one Gio player window driving one mpv instance.
type Window struct {
	cfg Config
	th  *giokit.Theme
	kw  *giokit.Window
	pl  *mpvplayer.Player

	created time.Time

	mu         sync.Mutex
	hwnd       uintptr
	host       *mpvembed.Host
	deadHost   *mpvembed.Host // failed embed host; destroyed on the loop thread next frame
	opened     bool           // mpv launch issued
	creating   bool           // host-window creation in flight (off-frame goroutine)
	embedFail  bool           // embedded launch failed → retry as popout
	note       string
	cur, total float64
	paused     bool

	// Waveform strip (mu): collapsed by default; peaks load once on first open.
	waveOpen     bool
	peaks        []byte
	peaksLoading bool
	peaksErr     string

	// Loop-goroutine-only state.
	lastRect [4]int
	shown    bool
	clk      deckclock.Clock // velocity-PLL playhead smoothing (single-goroutine)

	trim trimState

	playBtn, backBtn, fwdBtn      giokit.Button
	inBtn, outBtn, clrBtn, expBtn giokit.Button
	waveBtn                       giokit.Button
	seek, vol                     giokit.Slider
	wave                          giokit.Wave
}

// Open creates the player window and starts playback (mpv launch is async - the
// window shows immediately). Fails only when no mpv binary can be resolved.
func Open(cfg Config) (*Window, error) {
	if !mpvplayer.Available() {
		return nil, fmt.Errorf("mpv not found (install it in Settings)")
	}
	w := &Window{cfg: cfg, th: giokit.NewTheme(), pl: mpvplayer.New(cfg.Log), created: time.Now()}
	w.trim.clear()
	w.buildWidgets()

	w.pl.OnTick(func(cur, total float64) {
		w.mu.Lock()
		w.cur, w.total = cur, total
		w.mu.Unlock()
		w.kw.Invalidate()
	})
	w.pl.OnClose(func() { w.kw.Close() }) // mpv exited (e.g. popout closed) → window follows

	title := cfg.Title
	if title == "" {
		title = "Player"
	}
	w.kw = giokit.NewWindow(giokit.WindowOpts{
		Title:     title + " - rave-mate (Gio)",
		CtlName:   "player",
		Size:      image.Pt(960, 600),
		Log:       cfg.Log,
		OnView:    w.onView,
		OnDestroy: w.onDestroy,
	}, w.th, w.frame)
	return w, nil
}

// Close asks the window to close (any goroutine); mpv is torn down on destroy.
func (w *Window) Close() { w.kw.Close() }

// Done is closed once the window is destroyed and mpv stopped.
func (w *Window) Done() <-chan struct{} { return w.kw.Done() }

// Registry exposes the window's control-plane registry (ctl snapshot/tap seam).
func (w *Window) Registry() *giokit.Registry { return w.kw.Reg }

// buildWidgets wires the transport controls (stable registry IDs).
func (w *Window) buildWidgets() {
	w.playBtn = giokit.Button{ID: "play", Primary: true, OnClick: func() {
		paused := w.pl.TogglePause()
		w.mu.Lock()
		w.paused = paused
		w.mu.Unlock()
	}}
	w.backBtn = giokit.Button{Label: "-10s", ID: "back", OnClick: func() { w.seekRel(-10) }}
	w.fwdBtn = giokit.Button{Label: "+10s", ID: "fwd", OnClick: func() { w.seekRel(10) }}
	w.inBtn = giokit.Button{Label: "Set IN", ID: "trim.in", OnClick: func() {
		cur, _ := w.pl.Position()
		w.trim.setIn(cur)
	}}
	w.outBtn = giokit.Button{Label: "Set OUT", ID: "trim.out", OnClick: func() {
		cur, _ := w.pl.Position()
		w.trim.setOut(cur)
	}}
	w.clrBtn = giokit.Button{Label: "Clear", ID: "trim.clear", OnClick: func() { w.trim.clear() }}
	w.expBtn = giokit.Button{Label: "Export cut", ID: "trim.export", OnClick: func() {
		if w.cfg.OnExportCut != nil {
			w.cfg.OnExportCut(w.trim.in, w.trim.out)
		}
	}}
	w.waveBtn = giokit.Button{Label: "Wave", ID: "wave.toggle", OnClick: w.toggleWave}
	w.seek = giokit.Slider{ID: "seek", OnCommit: func(v float32) {
		_, total := w.pl.Position()
		if total > 0 {
			w.pl.Seek(float64(v) * total)
		}
	}}
	w.vol = giokit.Slider{ID: "vol", OnCommit: func(v float32) { w.pl.SetVolume(int(v * 100)) }}
	w.vol.Float.Value = 1
	w.wave = giokit.Wave{ID: "wave", OnSeek: func(frac float32) {
		_, total := w.pl.Position()
		if total > 0 {
			w.pl.Seek(float64(frac) * total)
		}
	}}
}

// toggleWave shows/hides the waveform strip, lazily starting the shared peaks analysis
// on first open (event-loop goroutine via Button.OnClick or a ctl tap).
func (w *Window) toggleWave() {
	w.mu.Lock()
	w.waveOpen = !w.waveOpen
	start := w.waveOpen && w.peaks == nil && !w.peaksLoading && w.peaksErr == "" && w.cfg.LoadPeaks != nil
	if start {
		w.peaksLoading = true
	}
	w.mu.Unlock()
	if !start {
		return
	}
	w.cfg.LoadPeaks(func(peaks []byte, _ float64, err error) { // duration: mpv owns it; seek maps by fraction
		w.mu.Lock()
		w.peaksLoading = false
		if err != nil {
			w.peaksErr = "Waveform analysis failed: " + err.Error()
		} else {
			w.peaks = peaks
		}
		w.mu.Unlock()
		w.kw.Invalidate()
	})
}

// seekRel jumps by delta seconds (clamped at 0).
func (w *Window) seekRel(delta float64) {
	cur, _ := w.pl.Position()
	if cur += delta; cur < 0 {
		cur = 0
	}
	w.pl.Seek(cur)
}

// onView captures the native view handle (event-loop goroutine).
func (w *Window) onView(e app.ViewEvent) {
	w.mu.Lock()
	w.hwnd = viewHWND(e)
	w.mu.Unlock()
	w.kw.Invalidate()
}

// onDestroy stops mpv and reports closure (event-loop goroutine). The mpv child
// window died with its parent; Destroy on the stale handle is a harmless no-op.
func (w *Window) onDestroy() {
	w.mu.Lock()
	host := w.host
	w.host = nil
	w.mu.Unlock()
	if host != nil {
		host.Destroy()
	}
	w.pl.Close()
	if w.cfg.OnClosed != nil {
		w.cfg.OnClosed()
	}
}

// ensureOpen lazily launches mpv once the embed target is known (event-loop goroutine,
// so mpvembed.Create runs on the window's creation thread). The launch itself is async
// - the IPC dial (up to 8s) must never block frames.
func (w *Window) ensureOpen() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.opened || w.creating {
		return
	}
	opts := w.cfg.Mpv
	opts.WID = 0
	if mpvembed.Supported() && !w.embedFail {
		if w.hwnd == 0 {
			if time.Since(w.created) < hwndDeadline {
				return // ViewEvent pending
			}
			w.logWarn("player HWND unresolved - using popout window", nil)
		} else {
			// OFF-FRAME, pumped-thread host (CreateHosted): creating the child inside the frame
			// handler deadlocked the window live - CreateWindowExW synchronizes with the parent's
			// (gio's) event thread, which is blocked waiting for this very frame to return, and a
			// caller-thread host would hang every later cross-thread Move the same way.
			w.creating = true
			hwnd := w.hwnd
			go func() {
				host, err := mpvembed.CreateHosted(hwnd)
				w.mu.Lock()
				w.creating = false
				o := w.cfg.Mpv
				o.WID = 0
				if err == nil {
					w.host = host
					o.WID = host.WID()
				} else {
					w.logWarn("mpv host window create failed", err)
					w.note = "Playing in a separate window (in-app embed unavailable)"
				}
				w.opened = true
				w.mu.Unlock()
				go w.openMpv(o)
				w.kw.Invalidate()
			}()
			return
		}
	}
	if opts.WID == 0 {
		w.note = "Playing in a separate window (in-app embed unavailable)"
	}
	w.opened = true
	go w.openMpv(opts)
}

// openMpv launches mpv (own goroutine). An embedded-launch failure retries as popout.
func (w *Window) openMpv(opts mpvplayer.Options) {
	if err := w.pl.OpenWith(w.cfg.File, w.cfg.Title, opts); err != nil {
		w.mu.Lock()
		if opts.WID != 0 { // embedded failed → next frame tears the host down + retries popout
			w.embedFail = true
			w.opened = false
			w.deadHost, w.host = w.host, nil
		} else {
			w.note = "Can't open media: " + err.Error()
		}
		w.mu.Unlock()
		w.logWarn("mpv open failed", err)
	}
	w.kw.Invalidate()
}

func (w *Window) logWarn(msg string, err error) {
	if w.cfg.Log == nil {
		return
	}
	f := map[string]any{}
	if err != nil {
		f["err"] = err.Error()
	}
	w.cfg.Log.Warn("gioplayer", msg, f)
}
