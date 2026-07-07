package ui

import (
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/mpvembed"
	"rave.page/mate/internal/mpvplayer"
)

// embedTickInterval paces the reposition loop - cheap (reads geometry, MoveWindow only on change).
const embedTickInterval = 40 * time.Millisecond

// embedReadyDeadline: give up resolving the host HWND after this and fall back to mpv's own window.
const embedReadyDeadline = 5 * time.Second

// windowHWND returns win's native Win32 window handle (0 if unavailable / non-Windows).
func windowHWND(win fyne.Window) uintptr {
	nw, ok := win.(driver.NativeWindow)
	if !ok {
		return 0
	}
	var hwnd uintptr
	nw.RunNative(func(ctx any) {
		if wc, isWin := ctx.(driver.WindowsWindowContext); isWin {
			hwnd = wc.HWND
		}
	})
	return hwnd
}

// embedController parents an mpv child window into win and keeps it aligned to a Fyne placeholder
// rect (surface), so mpv's GPU video renders inline in the app. Everything except the ticker body
// runs on the UI thread; the ticker only schedules fyne.Do(tick).
type embedController struct {
	u       *UI
	win     fyne.Window
	surface *canvas.Rectangle // the reserved video region (black); we track its on-screen rect
	overlay *widget.Label     // centered note (fallback message); empty while embedded
	pl      *mpvplayer.Player
	opts    mpvplayer.Options // without WID; WID set once the host exists
	file    string
	title   string

	host     *mpvembed.Host
	stopCh   chan struct{}
	started  atomic.Bool // Open (embedded or fallback) issued
	suspend  atomic.Bool // force-hide (a modal dialog is covering the video)
	deadline time.Time
	lastRect [4]int
	shown    bool
}

// buildEmbeddedMpvPlayer builds the in-app player with mpv rendering INTO win (no popout). Video
// occupies a reserved region above the shared transport/trim controls. On any embed failure it
// degrades to mpv's own window (logged), never crashing. Returns content + a stop func.
func (u *UI) buildEmbeddedMpvPlayer(win fyne.Window, file string, markers []playerMarker, tgt trimTarget, dismiss func()) (fyne.CanvasObject, func()) {
	pl := mpvplayer.New(u.svc.Log)
	title := baseName(file)
	if dismiss != nil {
		pl.OnClose(func() { fyne.Do(dismiss) }) // mpv exited (e.g. popout-fallback window closed) → dismiss
	}

	rect := canvas.NewRectangle(colBackground)
	rect.SetMinSize(fyne.NewSize(480, 270)) // 16:9 reserve
	overlay := mutedInline("")
	surface := container.NewStack(rect, container.NewCenter(overlay))

	var opts mpvplayer.Options
	if u.svc.Cfg != nil {
		pf := u.svc.Cfg.Features.Player
		opts = mpvplayer.Options{VO: pf.ResolvedVO(), HWDec: pf.ResolvedHWDec(), Profile: pf.Profile, Extra: pf.ExtraArgs}
	}

	ec := &embedController{
		u: u, win: win, surface: rect, overlay: overlay, pl: pl, opts: opts,
		file: file, title: title, stopCh: make(chan struct{}), deadline: time.Now().Add(embedReadyDeadline),
	}
	u.registerEmbed(ec)
	ec.start()

	stop := func() {
		ec.stop()
		u.unregisterEmbed(ec)
		pl.Close()
	}
	return u.playerControls(pl, surface, file, markers, tgt, pcOpts{autoplaying: true, hasVolume: true}), stop
}

// start launches the reposition loop.
func (ec *embedController) start() {
	go func() {
		t := time.NewTicker(embedTickInterval)
		defer t.Stop()
		for {
			select {
			case <-ec.stopCh:
				return
			case <-t.C:
				fyne.Do(ec.tick)
			}
		}
	}()
}

// stop halts the loop and destroys the host window (UI thread).
func (ec *embedController) stop() {
	select {
	case <-ec.stopCh:
	default:
		close(ec.stopCh)
	}
	if ec.host != nil {
		ec.host.Hide()
		ec.host.Destroy()
		ec.host = nil
	}
}

// tick (UI thread) lazily creates the host + starts mpv, then keeps the host aligned to the surface
// rect. Falls back to mpv's own window if the host HWND can't be resolved before the deadline.
func (ec *embedController) tick() {
	select {
	case <-ec.stopCh:
		return
	default:
	}

	if !ec.started.Load() {
		ec.trySetup()
		return // wait until setup issues Open before positioning
	}
	if ec.host == nil { // fell back to popout - nothing to position
		return
	}
	if ec.suspend.Load() {
		ec.hideHost()
		return
	}
	cnv := ec.win.Canvas()
	if cnv == nil {
		return
	}
	sz := ec.surface.Size()
	if !ec.surface.Visible() || sz.Width < 2 || sz.Height < 2 {
		ec.hideHost()
		return
	}
	scale := cnv.Scale()
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(ec.surface)
	x, y := int(pos.X*scale), int(pos.Y*scale)
	w, h := int(sz.Width*scale), int(sz.Height*scale)
	r := [4]int{x, y, w, h}
	if r != ec.lastRect {
		ec.host.Move(x, y, w, h)
		ec.lastRect = r
	}
	if !ec.shown {
		ec.host.Show()
		ec.shown = true
	}
}

// trySetup creates the child host + starts embedded mpv; on timeout it degrades to the popout.
func (ec *embedController) trySetup() {
	parent := windowHWND(ec.win)
	if parent != 0 {
		h, err := mpvembed.Create(parent)
		if err == nil {
			ec.host = h
			ec.opts.WID = h.WID()
			if oerr := ec.pl.OpenWith(ec.file, ec.title, ec.opts); oerr != nil {
				ec.logWarn("embedded mpv open failed", oerr)
				h.Destroy()
				ec.host = nil
				ec.fallbackPopout()
				return
			}
			ec.started.Store(true)
			return
		}
		ec.logWarn("mpv host window create failed", err)
		ec.fallbackPopout()
		return
	}
	if time.Now().After(ec.deadline) {
		ec.logWarn("mpv host HWND unresolved - using popout window", nil)
		ec.fallbackPopout()
	}
}

// fallbackPopout opens mpv in its own window (WID unset) and notes it on the reserved region.
func (ec *embedController) fallbackPopout() {
	ec.opts.WID = 0
	if err := ec.pl.OpenWith(ec.file, ec.title, ec.opts); err != nil {
		ec.overlay.SetText("Can't open media: " + err.Error())
	} else {
		ec.overlay.SetText("Playing in a separate window (in-app embed unavailable)")
	}
	ec.started.Store(true)
}

// hideHost hides the child window once (idempotent).
func (ec *embedController) hideHost() {
	if ec.shown {
		ec.host.Hide()
		ec.shown = false
	}
}

func (ec *embedController) logWarn(msg string, err error) {
	if ec.u.svc.Log == nil {
		return
	}
	fields := map[string]any{}
	if err != nil {
		fields["err"] = err.Error()
	}
	ec.u.svc.Log.Warn("player", msg, fields)
}

// registerEmbed tracks an active embed so modal dialogs can suspend (hide) it (UI thread).
func (u *UI) registerEmbed(ec *embedController) { u.embeds = append(u.embeds, ec) }

// unregisterEmbed drops a finished embed (UI thread).
func (u *UI) unregisterEmbed(ec *embedController) {
	for i, e := range u.embeds {
		if e == ec {
			u.embeds = append(u.embeds[:i], u.embeds[i+1:]...)
			return
		}
	}
}

// suspendEmbeds hides every active embedded video (its mpv child HWND floats above the Fyne canvas,
// so a modal dialog would otherwise render UNDER it) and returns a restore func to call when the
// dialog closes. UI thread. No active embeds → restore is a no-op.
func (u *UI) suspendEmbeds() (restore func()) {
	if len(u.embeds) == 0 {
		return func() {}
	}
	for _, e := range u.embeds {
		e.suspend.Store(true)
		e.hideHost()
	}
	return func() {
		for _, e := range u.embeds {
			e.suspend.Store(false)
		}
	}
}
