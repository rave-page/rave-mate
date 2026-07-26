package webui

// procShell child side (`rave-mate feature webview`): hosts the native window and speaks PSH1
// (shell_proc_proto.go) back to the daemon over featurehost stdio. It is a pure VIEW + INPUT
// TRANSPORT - no config read, no database, no identity, no secureseal. Everything it needs arrives
// in the init payload; everything it learns leaves as an event.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/governor"
)

// childForceExitGrace re-homes the in-proc forceExitGrace: WebView2's Terminate is unreliable, so
// the child guarantees its own exit. The daemon's outer backstop is child-kill (job object + grace),
// so a wedged webview can no longer hang the daemon's shutdown.
const childForceExitGrace = 1500 * time.Millisecond

func init() {
	featurehost.Register(procFeatureName, func() featurehost.Feature { return &webviewFeature{} })
}

type webviewFeature struct {
	rt  *featurehost.Runtime
	ini procInit

	mu    sync.Mutex
	win   shell
	ready bool

	quitOnce sync.Once
	done     chan struct{}
}

// Init stores the daemon's params and builds the window host (not yet running - Start owns the
// message loop, which must have the child's main goroutine and a locked OS thread).
func (f *webviewFeature) Init(params json.RawMessage, rt *featurehost.Runtime) error {
	f.rt = rt
	f.done = make(chan struct{})
	if len(params) > 0 {
		if err := json.Unmarshal(params, &f.ini); err != nil {
			return err
		}
	}
	if f.ini.RuntimeJS == "" {
		return errors.New("webview child: init carried no runtimeJS")
	}
	// The wire bytes ARE the document-start runtime (byte-contracted with the daemon's renderers);
	// never fall back to the compiled-in copy - B6's child has none.
	webviewInitJS = f.ini.RuntimeJS
	webviewDataDir = f.ini.DataDir
	webviewAllowGPU = f.ini.AllowGPU
	governor.SetStreaming(f.ini.Streaming)
	// Bindings deliver page results to the DAEMON, not this process's waiter map. The reserved beat
	// id is consumed here: it originates on the window's UI thread, so it is the liveness proof.
	evalSink = func(id, result string) {
		if id == procBeatID {
			rt.Beat()
			return
		}
		rt.Emit(procEvEvalRes, procEvalRes{ID: id, Result: result})
	}
	onWindowHidden = func() { rt.Emit(procEvWin, procWin{Hidden: true}) }
	onWindowState = func(w procWin) { rt.Emit(procEvWin, w) }

	onAct := func(payload string) { rt.Emit(procEvAction, procAct{Payload: payload}) }
	onReady := func() {
		f.mu.Lock()
		f.ready = true
		w := f.win
		f.mu.Unlock()
		var h uint64
		if w != nil {
			h = uint64(w.hwnd())
		}
		// Show the window explicitly: this process was spawned with SW_HIDE (sysexec.Hide suppresses
		// the console window) and Windows applies that to our FIRST top-level window - so the WebView2
		// window comes up hidden. That is invisible UI AND a solid-black capture. No focus steal here;
		// raising is the daemon's call (it sends `show` on the first ready only, so a crash-restart does
		// not yank the user out of another app). StartHidden = tray-only start, honoured for real now.
		if !f.ini.Virtual && !f.ini.StartHidden {
			revealWindow(uintptr(h))
		}
		rt.Emit(procEvReady, procReady{HWND: h, Virtual: f.ini.Virtual})
		if !f.ini.Virtual {
			go f.beat()
		}
	}
	if f.ini.Virtual {
		f.win = newLoopbackWindow(onAct, onReady)
		return nil
	}
	w, ok := newNativeShell(f.ini.Title, f.ini.W, f.ini.H, onAct, onReady)
	if !ok {
		return errors.New("webview child: no native window host in this build")
	}
	f.win = w
	return nil
}

// Start runs the window's message loop until it returns (window closed) or the daemon stops us.
func (f *webviewFeature) Start(ctx context.Context) error {
	f.mu.Lock()
	w, ini := f.win, f.ini
	f.mu.Unlock()
	if w == nil {
		return errors.New("webview child: no window")
	}
	go func() { // daemon-side stop / stdin EOF must tear the window down too
		select {
		case <-ctx.Done():
			w.terminate()
		case <-f.done:
		}
	}()
	// Blocks on the OS message loop. Its return = the window is gone; report it so the daemon's
	// run() can unwind instead of waiting on a dead session.
	w.run(ini.HTML(), ini.StartHidden)
	f.rt.Emit(procEvGone, struct{}{})
	f.quitOnce.Do(func() { close(f.done) })
	return nil
}

// beat proves the WINDOW's UI thread is still pumping (not just the process alive): the ping is
// dispatched onto that thread, so a wedged webview stops beating and the daemon's Host kills +
// relaunches the child. That is the re-homed "webview wedged" verdict - never a daemon hang.
func (f *webviewFeature) beat() {
	t := time.NewTicker(procBeatInterval)
	defer t.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-t.C:
		}
		f.mu.Lock()
		w := f.win
		f.mu.Unlock()
		if w == nil {
			return
		}
		// Runs on the window's UI thread; the binding routes it to rt.Beat via evalSink.
		w.eval("window.__rave_evalResult(" + jsQuote(procBeatID) + ",'1');")
	}
}

// procChildExit is the child's hard exit (the re-homed force-exit backstop). Package-level so the
// wedge tests can observe the path without ending the test process.
var procChildExit = func() { os.Exit(0) }

// Handle serves no requests - PSH1 is event-only past the featurehost init/stop handshake.
func (f *webviewFeature) Handle(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("webview child: unknown method " + method)
}

// HandleEvent applies one parent→child PSH1 event. Called INLINE on the child's stdin reader
// (featurehost runtime.go), which is what makes the ordered lane FIFO end-to-end: arrival order is
// application order, and every window call below is a non-blocking post onto the UI thread.
func (f *webviewFeature) HandleEvent(event string, data json.RawMessage) {
	f.mu.Lock()
	w := f.win
	f.mu.Unlock()
	if w == nil {
		return
	}
	switch event {
	case procEvDoc:
		var m procDoc
		if json.Unmarshal(data, &m) == nil {
			w.setHTML(m.HTML)
		}
	case procEvEval:
		var m procEval
		if json.Unmarshal(data, &m) == nil {
			w.eval(m.JS)
		}
	case procEvXEval:
		var m procXEval
		if json.Unmarshal(data, &m) == nil {
			w.eval(m.JS)
		}
	case procEvAct:
		var m procAct
		if json.Unmarshal(data, &m) == nil {
			w.post(m.Payload)
		}
	case procEvResize:
		var m procResize
		if json.Unmarshal(data, &m) == nil {
			w.resize(m.W, m.H)
		}
	case procEvShow:
		w.show()
	case procEvStream:
		var m procStream
		if json.Unmarshal(data, &m) == nil {
			governor.SetStreaming(m.On)
		}
	case procEvShot:
		var m procShot
		if json.Unmarshal(data, &m) != nil {
			return
		}
		// Own goroutine: PrintWindow + PNG encode is tens of ms, and this handler runs ON the stdin
		// reader - blocking it would stall the ordered lane behind a screenshot.
		go func() {
			res := procShotRes{RID: m.RID}
			if err := captureHWND(w.hwnd(), m.Path, m.X, m.Y, m.W, m.H); err != nil {
				res.Err = err.Error()
			}
			f.rt.Emit(procEvShotRes, res)
		}()
	case procEvQuit:
		var m procQuit
		_ = json.Unmarshal(data, &m)
		grace := childForceExitGrace
		if m.GraceMS > 0 {
			grace = time.Duration(m.GraceMS) * time.Millisecond
		}
		w.terminate()
		go f.forceExit(grace)
	}
}

// forceExit is the child's own backstop: if the message loop has not unwound within grace, exit
// anyway. The child holds no daemon state, so there is nothing to flush - the whole reason the
// in-proc shutdownHook dance existed.
func (f *webviewFeature) forceExit(grace time.Duration) {
	select {
	case <-f.done:
	case <-time.After(grace):
		f.quitOnce.Do(func() { close(f.done) })
		procChildExit()
	}
}

// HTML is the child's initial document. The daemon always follows with a doc frame on reattach, so
// an empty init document is legal (and is what a restarted child gets before the re-render lands).
func (i procInit) HTML() string { return i.InitialHTML }
