//go:build cgo && windows

package webui

// The one REAL WINDOWED smoke: a genuine WebView2 window in a genuine child process, driven through
// the ctl primitives against the genuine runtime JS. Opt-in - it opens a visible window and needs the
// WebView2 runtime, so `go test ./...` must never trip over it:
//
//	RAVE_MATE_WEBVIEW_SMOKE=1 go test ./internal/webui -run TestProcShellWindowedSmoke -v -count=1
//
// The orchestrator runs it at merge. Everything it asserts is asserted through the transport, so a
// pass means the daemon really drove a foreign-process window: document loaded, runtime injected,
// snapshot/click/read round-tripped, a page act came back, the HWND is usable for OS capture, and the
// quit path closed the window inside grace.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/ui"
)

const smokeEnv = "RAVE_MATE_WEBVIEW_SMOKE"

// smokeDoc is a minimal page with the addressing the ctl primitives need: a labelled field, a
// clickable act, and text for the snapshot outline.
const smokeDoc = `<!doctype html><html><head><meta charset=utf-8><title>B5 smoke</title></head><body>
<main id=main>
<button data-act="smoke-click" id=b1>Smoke Button</button>
<div data-label="smoke field"><input data-act="set:smoke" value="42"></div>
<div id=out>SMOKE READY</div>
</main><div id=__modal></div></body></html>`

func TestProcShellWindowedSmoke(t *testing.T) {
	if os.Getenv(smokeEnv) == "" {
		t.Skip("set " + smokeEnv + "=1 to run the real windowed smoke (opens a WebView2 window)")
	}
	if !probeWebview() {
		t.Skip("no WebView2 runtime on this box")
	}
	log := logbus.New(512)
	shellLog = log
	procVirtualChild = false // a REAL window in the child
	// Isolated WebView2 profile: never share the user's real one (it is per-process locked, and a
	// running rave-mate must not be disturbed). The daemon hands the child this path in init.
	saveDir := webviewDataDir
	webviewDataDir = t.TempDir()
	t.Cleanup(func() { webviewDataDir = saveDir })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	procChildCmd = func() *exec.Cmd {
		cmd := exec.Command(exe, "-test.run=TestProcChildNoop")
		cmd.Env = append(os.Environ(), procTestChildEnv+"=1", procTestModeEnv+"=", smokeEnv+"=1")
		return cmd
	}
	t.Cleanup(func() { procChildCmd, shellLog = nil, nil })

	u := &UI{svc: ui.Services{Cfg: &config.Config{}, Log: log}, log: log, active: "live",
		started: time.Now(), stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	sh, ok := newProcShell("rave-mate B5 smoke", 900, 640, u.onAction, nil)
	if !ok {
		t.Fatal("newProcShell failed")
	}
	ps := sh.(*procShell)
	u.shell = ps
	ps.onReattach = u.reattach
	ps.onDrop = u.dropFragCache
	go u.evalFlusher()

	acts := make(chan string, 32)
	ps.onAction = func(p string) { acts <- p }

	ready := make(chan struct{})
	ps.onReady = func() { close(ready) }
	go ps.run(smokeDoc, false)
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("the real child never reported ready")
	}

	// The window handle must be usable from the DAEMON: OS capture is cross-process by design
	// (PrintWindow on a foreign HWND), which is why no screenshot bytes cross the protocol.
	deadline := time.Now().Add(15 * time.Second)
	for ps.hwnd() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if ps.hwnd() == 0 {
		t.Fatal("no HWND reported - OS screenshots would be impossible")
	}
	shot := t.TempDir() + "/smoke.png"
	if err := u.Screenshot(shot); err != nil {
		t.Errorf("Screenshot of the child's window: %v", err)
	} else if fi, err := os.Stat(shot); err != nil || fi.Size() == 0 {
		t.Errorf("screenshot is empty (%v)", err)
	}

	// The injected runtime + ctl round trips over the real page.
	if snap := u.Snapshot(); !strings.Contains(snap, "Smoke Button") {
		t.Fatalf("snapshot through the real window = %q", snap)
	}
	if v, ok := u.Read("smoke field"); !ok || v != "42" {
		t.Errorf("Read = %q,%v", v, ok)
	}
	if !u.Set("smoke field", "77") {
		t.Error("Set missed")
	}
	if v, _ := u.Read("smoke field"); v != "77" {
		t.Errorf("Set did not stick: %q", v)
	}
	if !u.Click("Smoke Button") {
		t.Fatal("Click missed")
	}
	// Drain until the click's act shows up: Set above already produced a `set:smoke` change act, so
	// the click is not necessarily the first payload in the channel.
	clicked, wait := false, time.After(5*time.Second)
	for !clicked {
		select {
		case p := <-acts:
			clicked = strings.Contains(p, "smoke-click")
		case <-wait:
			t.Fatal("the page act never reached the daemon")
		}
	}
	// An ordered-lane patch must land in the real DOM.
	u.eval("window.__patch('out'," + jsQuote("PATCHED") + ")")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := u.evalString("return document.getElementById('out').textContent"); v == "PATCHED" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v, _ := u.evalString("return document.getElementById('out').textContent"); v != "PATCHED" {
		t.Errorf("ordered-lane patch did not reach the DOM (#out = %q)", v)
	}
	u.Resize(1000, 700)
	u.Show()

	start := time.Now()
	ps.terminate()
	select {
	case <-ps.done:
	case <-time.After(procQuitGrace + 15*time.Second):
		t.Fatal("terminate() never unblocked run() against the real window")
	}
	t.Logf("real windowed smoke: hwnd=%#x, quit in %v", ps.hwnd(), time.Since(start).Truncate(time.Millisecond))
	close(u.stop)
	releaseUIState(u)
}
