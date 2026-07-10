//go:build cgo && windows

package webui

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	webview "github.com/webview/webview_go"
	"golang.org/x/sys/windows/registry"

	"rave.page/mate/internal/config"
)

// shellAvailable reports the webview is compiled in (cgo Windows builds only).
const shellAvailable = true

// webview2RuntimeGUID is the Evergreen WebView2 Runtime's EdgeUpdate client id.
const webview2RuntimeGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// probeWebview reports whether the WebView2 runtime is installed, WITHOUT constructing a window
// (creating+destroying a real webview is slow and can leave orphan msedgewebview2 children). We
// check the standard EdgeUpdate registry locations Microsoft documents for runtime detection:
// per-machine (HKLM, incl. the WOW6432Node view on 64-bit) and per-user (HKCU). A non-empty,
// non-zero `pv` version means the runtime is present. The seam uses this to fall back to Fyne on a
// box that lacks the runtime instead of showing a blank webview window.
func probeWebview() bool {
	type loc struct {
		root registry.Key
		path string
		wow  bool
	}
	base := `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webview2RuntimeGUID
	baseWow := `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webview2RuntimeGUID
	for _, l := range []loc{
		{registry.LOCAL_MACHINE, baseWow, true},
		{registry.LOCAL_MACHINE, base, false},
		{registry.CURRENT_USER, base, false},
		{registry.CURRENT_USER, baseWow, true},
	} {
		access := uint32(registry.QUERY_VALUE)
		if l.wow {
			access |= registry.WOW64_32KEY
		} else {
			access |= registry.WOW64_64KEY
		}
		k, err := registry.OpenKey(l.root, l.path, access)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		k.Close()
		if err == nil && pv != "" && pv != "0.0.0.0" {
			return true
		}
	}
	return false
}

// forceExitGrace: webview_go Terminate is unreliable on WebView2 (leaves msedgewebview2 children +
// the process alive). Guarantee close after this grace, like rave-app.
const forceExitGrace = 1500 * time.Millisecond

// forceExitBackstop bounds the graceful-shutdown hook once the webview wedged - hard exit after.
const forceExitBackstop = 10 * time.Second

// shutdownHook runs before the watchdog's forced exit so daemon state is flushed (module stop,
// bbolt close, stream end) instead of cut mid-write. Must be idempotent - the normal shutdown
// path may race it if the webview unwinds late.
var (
	shutdownHookMu sync.Mutex
	shutdownHook   func()
)

// SetShutdownHook registers the graceful-shutdown routine the force-exit watchdog invokes.
// app.go wires its shutdown() here.
func SetShutdownHook(fn func()) {
	shutdownHookMu.Lock()
	shutdownHook = fn
	shutdownHookMu.Unlock()
}

type cgoShell struct {
	title    string
	w, h     int
	onAction func(string)
	onReady  func()

	mu   sync.Mutex
	wv   webview.WebView
	done chan struct{}
	acts chan string // page → Go actions, drained off the UI thread (see actWorker)
}

func newShell(title string, w, h int, onAction func(string), onReady func()) (shell, bool) {
	return &cgoShell{title: title, w: w, h: h, onAction: onAction, onReady: onReady,
		done: make(chan struct{}), acts: make(chan string, 64)}, true
}

// actWorker drains page actions on its own goroutine, serialized in arrival order. The webview
// binding callback runs ON the window's UI thread - handling actions there (renders, config
// saves, device probes) froze the message pump: the whole window stalled and dragging lagged.
func (s *cgoShell) actWorker() {
	for {
		select {
		case <-s.done:
			return
		case p := <-s.acts:
			if s.onAction != nil {
				s.onAction(p)
			}
		}
	}
}

// run creates the window on a locked OS thread and blocks on the message loop until close.
func (s *cgoShell) run(initialHTML string, _ bool) {
	runtime.LockOSThread()
	// Persistent WebView2 profile dir (owner-only). Go holds all app state, so this only avoids
	// ephemeral temp churn / DevTools noise; harmless elsewhere.
	if dir, err := config.DataPath("webview2"); err == nil {
		if os.MkdirAll(dir, 0o700) == nil {
			_ = os.Setenv("WEBVIEW2_USER_DATA_FOLDER", dir)
		}
	}
	// Good-neighbour default: run WebView2 (Chromium/Edge) WITHOUT GPU compositing so rave-mate's
	// window never competes with a live NVENC/GPU encoder (OBS) for GPU/VRAM/PCIe bandwidth - the
	// prime suspect for a stream's bitrate collapsing while rave-mate is open. The UI is a light,
	// Go-patched DOM (no video, cheap SVG), so software compositing is imperceptible here. The
	// WebView2 loader reads WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS when the app passes default env
	// options (we do). A power user can opt back into GPU via features.ui.webviewGpu=true.
	if !webviewAllowGPU {
		if os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") == "" {
			_ = os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
				"--disable-gpu --disable-gpu-compositing --disable-software-rasterizer")
		}
	}
	// Runtime safety net: even past the seam's probeWebview() check, WebView2 creation can still
	// fail (broken/partial runtime). webview.New surfaces that as a panic or a nil handle - recover
	// so a missing runtime degrades to "no window, daemon survives in the tray" instead of a crash.
	// The seam (app.go) already prefers Fyne when probeWebview() is false, so this only fires on a
	// runtime that lied about being present.
	w := func() (wv webview.WebView) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "webui: WebView2 construction failed (%v) - renderer unavailable\n", r)
				wv = nil
			}
		}()
		return webview.New(false)
	}()
	if w == nil {
		fmt.Fprintln(os.Stderr, "webui: WebView2 runtime unavailable - webview renderer not started")
		close(s.done)
		return
	}
	s.mu.Lock()
	s.wv = w
	s.mu.Unlock()
	defer w.Destroy()
	w.SetTitle(s.title)
	w.SetSize(s.w, s.h, webview.HintNone)
	setWindowIcon(uintptr(w.Window()))       // brand icon in title bar / Alt-Tab (see windowicon_windows.go)
	installSizeMoveHook(uintptr(w.Window())) // WM_ENTER/EXITSIZEMOVE → pause live ticks while dragging
	go s.actWorker()
	// Page → Go: the single action channel. Payload is JSON ({act,val,form,id}). The callback runs
	// on the UI thread - enqueue only, never handle here (a slow handler freezes the message pump).
	_ = w.Bind("rave", func(payload string) {
		select {
		case s.acts <- payload:
		default: // queue full (a handler is wedged) - drop rather than block the UI thread
		}
	})
	// ctl eval round-trip result sink (see shell.go).
	_ = w.Bind("__rave_evalResult", func(id, result string) { deliverEval(id, result) })
	w.Init(runtimeJS)
	w.SetHtml(initialHTML)
	if s.onReady != nil {
		go s.onReady()
	}
	w.Run()
	close(s.done)
}

func (s *cgoShell) setHTML(html string) { s.dispatch(func(w webview.WebView) { w.SetHtml(html) }) }
func (s *cgoShell) eval(js string)      { s.dispatch(func(w webview.WebView) { w.Eval(js) }) }
func (s *cgoShell) resize(w, h int) {
	s.dispatch(func(wv webview.WebView) { wv.SetSize(w, h, webview.HintNone) })
}

func (s *cgoShell) terminate() {
	s.mu.Lock()
	w := s.wv
	s.mu.Unlock()
	if w != nil {
		w.Dispatch(func() { w.Terminate() })
	}
	go func() {
		select {
		case <-s.done:
		case <-time.After(forceExitGrace):
			// Webview didn't unwind, so the daemon's normal shutdown (blocked in w.Run) never
			// runs. Flush state via the injected hook before exiting; hard backstop if it wedges.
			shutdownHookMu.Lock()
			hook := shutdownHook
			shutdownHookMu.Unlock()
			if hook != nil {
				go func() {
					time.Sleep(forceExitBackstop)
					os.Exit(0) // shutdown wedged - guarantee close
				}()
				hook()
			}
			os.Exit(0) // guarantee close
		}
	}()
}

// show raises the window. The webview is already visible on create; foreground-raise via the OS
// window handle is a per-platform follow-up (see screenshot_windows.go for the HWND helpers).
func (s *cgoShell) show() { showWindow(s.hwnd()) }

func (s *cgoShell) hwnd() uintptr {
	s.mu.Lock()
	w := s.wv
	s.mu.Unlock()
	if w == nil {
		return 0
	}
	return uintptr(w.Window())
}

// dispatch runs fn on the webview UI thread (Eval/SetHtml must not be called off it).
func (s *cgoShell) dispatch(fn func(webview.WebView)) {
	s.mu.Lock()
	w := s.wv
	s.mu.Unlock()
	if w == nil {
		return
	}
	w.Dispatch(func() { fn(w) })
}
