package giokit

import (
	"image"

	"gioui.org/app"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

// WindowOpts configures NewWindow.
type WindowOpts struct {
	Title   string
	CtlName string      // ctl window-registry name (gio-snapshot/gio-tap ID base); "" = Title
	Size    image.Point // dp
	Log     *logbus.Bus // may be nil

	// OnView receives native view lifecycle events on the event-loop goroutine -
	// on Windows an app.Win32ViewEvent whose HWND parents child windows (mpv --wid).
	// An invalid event (Valid() == false) means the native view is gone.
	OnView func(app.ViewEvent)
	// OnDestroy runs once on the event-loop goroutine when the window is destroyed
	// (user close or Close()).
	OnDestroy func()
}

// Window hosts one Gio window with its event loop on a dedicated goroutine.
//
// Threading: Gio windows run fine off the process main thread on Windows and Linux
// (the loop goroutine locks its own OS thread), so giokit windows coexist with the
// Fyne main window. macOS caveat: Gio needs the process main thread there, which Fyne
// owns - Gio aux windows are unsupported on darwin until the Gio-main-window end state
// (see GIO_MIGRATION.md); callers must keep a non-Gio fallback path.
type Window struct {
	W   *app.Window
	Reg *Registry

	th    *Theme
	log   *logbus.Bus
	ctlID string
	done  chan struct{}
}

// NewWindow opens a Gio window and starts its event loop in its own goroutine. frame
// lays out the content each FrameEvent (background pre-filled with th.Bg; Registry
// Begin/EndFrame handled by the host). The window auto-registers in the ctl window
// registry (gio-snapshot/gio-tap) until destroyed.
func NewWindow(opts WindowOpts, th *Theme, frame func(gtx layout.Context)) *Window {
	w := &Window{W: new(app.Window), Reg: NewRegistry(), th: th, log: opts.Log, done: make(chan struct{})}
	w.W.Option(app.Title(opts.Title), app.Size(unit.Dp(opts.Size.X), unit.Dp(opts.Size.Y)))
	w.Reg.SetInvalidate(w.W.Invalidate)
	name := opts.CtlName
	if name == "" {
		name = opts.Title
	}
	w.ctlID = RegisterWindow(name, opts.Title, w.Reg)
	go w.loop(opts, frame)
	return w
}

// CtlID returns the window's ctl registry ID.
func (w *Window) CtlID() string { return w.ctlID }

// loop is the window event loop (dedicated goroutine; Gio locks it to an OS thread).
func (w *Window) loop(opts WindowOpts, frame func(gtx layout.Context)) {
	defer close(w.done)
	defer UnregisterWindow(w.ctlID) // covers destroy AND a panicked loop
	defer debuglog.Recover(w.log, "giokit", false)
	var ops op.Ops
	for {
		switch e := w.W.Event().(type) {
		case app.DestroyEvent:
			if w.log != nil {
				f := map[string]any{}
				if e.Err != nil {
					f["err"] = e.Err.Error()
				}
				w.log.Info("giokit", "window destroyed", f)
			}
			if opts.OnDestroy != nil {
				opts.OnDestroy()
			}
			return
		case app.ViewEvent:
			if opts.OnView != nil {
				opts.OnView(e)
			}
		case app.FrameEvent:
			w.Reg.BeginFrame()
			gtx := app.NewContext(&ops, e)
			paint.Fill(gtx.Ops, w.th.Bg)
			frame(gtx)
			w.Reg.EndFrame()
			e.Frame(gtx.Ops)
		}
	}
}

// Invalidate schedules a redraw (any goroutine).
func (w *Window) Invalidate() { w.W.Invalidate() }

// Close asks the window to close (any goroutine); Done() closes once it has.
func (w *Window) Close() { w.W.Perform(system.ActionClose) }

// Done is closed after the window is destroyed and the loop has exited.
func (w *Window) Done() <-chan struct{} { return w.done }
