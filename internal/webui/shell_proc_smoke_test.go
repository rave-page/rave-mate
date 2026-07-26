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
// snapshot/click/read round-tripped, a page act came back, the capture holds real PIXELS (not the
// solid black a never-shown window returns), and the quit path closed the window inside grace.
//
// The child is spawned with sysexec.Hide exactly as production does - that is what made its window
// come up hidden, so without it this smoke would not reproduce the conditions it must gate.

import (
	"image/png"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/sysexec"
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
		// MUST mirror production's spawn (featurehost newCmd): sysexec.Hide is what made the child's
		// WebView2 window come up hidden - invisible UI and a solid-black capture. Without this the
		// smoke runs under conditions the real app never has, and cannot catch the regression it exists
		// to catch. (Named/hardlink naming is skipped: irrelevant to rendering, and hardlinking the
		// test binary is pure noise.)
		sysexec.Hide(cmd)
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
	// Registered BEFORE run: a failing assertion must still close the window and reap the child (a
	// live child keeps its WebView2 profile dir locked, which then fails TempDir cleanup).
	var quitAt time.Time
	t.Cleanup(func() {
		quitAt = time.Now()
		ps.terminate()
		select {
		case <-ps.done:
		case <-time.After(procQuitGrace + 15*time.Second):
			t.Error("terminate() never unblocked run() against the real window")
		}
		t.Logf("real windowed smoke: hwnd=%#x, quit in %v", ps.hwnd(), time.Since(quitAt).Truncate(time.Millisecond))
		close(u.stop)
		releaseUIState(u)
	})
	go ps.run(smokeDoc, false)
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("the real child never reported ready")
	}

	// The child must report a real window handle: procShell gates the capture request on it, and
	// ctl/diagnostics name the window by it.
	deadline := time.Now().Add(15 * time.Second)
	for ps.hwnd() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if ps.hwnd() == 0 {
		t.Fatal("no HWND reported - screenshots would be refused")
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
	// The capture must contain PIXELS, not just bytes: a solid-black PNG is non-empty, which is why the
	// original "non-empty file" assertion sailed straight past the merge-time defect. Captured only now,
	// after the round trips above proved the page is live, so the threshold is not racing the first paint.
	dir := t.TempDir()
	shot := dir + "/smoke.png"
	if err := u.Screenshot(shot); err != nil {
		t.Fatalf("Screenshot of the child's window: %v", err)
	}
	lit, total := shotLitFraction(t, shot)
	t.Logf("child-side capture: %d/%d non-black px (%.1f%%)", lit, total, 100*float64(lit)/float64(total))
	if total == 0 {
		t.Fatal("capture decoded to zero pixels")
	}
	if frac := float64(lit) / float64(total); frac < 0.01 {
		t.Fatalf("capture is effectively black (%.3f%% non-black) - the window did not render into it", 100*frac)
	}
	// Region capture must also carry content (ctl screenshot-region).
	region := dir + "/region.png"
	if err := u.ScreenshotRegion(region, 0, 0, 200, 120); err != nil {
		t.Errorf("ScreenshotRegion: %v", err)
	} else if rlit, rtotal := shotLitFraction(t, region); rtotal == 0 || float64(rlit)/float64(rtotal) < 0.01 {
		t.Errorf("region capture is effectively black (%d/%d non-black)", rlit, rtotal)
	}
	var worst time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		if err := u.Screenshot(dir + "/cost.png"); err != nil {
			t.Fatalf("repeat capture %d: %v", i, err)
		}
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	t.Logf("child-side capture cost: worst %v of 5 (ScreenshotAll per-tab settle is 300ms, shot budget %v)",
		worst.Truncate(time.Millisecond), procShotTimeout)

	// Diagnostic, NOT an assertion: the same window captured the OLD way, from the daemon. Cross-process
	// PrintWindow does work once the window is shown - it was never the root cause - so this is recorded
	// as a number rather than pinned as a contract. The capture lives in the child because same-process
	// is the proven-good path, not because the daemon-side one cannot ever work.
	daemonShot := dir + "/daemon-side.png"
	if err := u.captureRegionLocal(daemonShot, 0, 0, 0, 0); err != nil {
		t.Logf("daemon-side (cross-process) capture errored: %v", err)
	} else {
		dlit, dtotal := shotLitFraction(t, daemonShot)
		t.Logf("daemon-side (cross-process) capture: %d/%d non-black px (%.1f%%) - black here is the defect",
			dlit, dtotal, 100*float64(dlit)/float64(dtotal))
	}

	u.Resize(1000, 700)
	u.Show()

}

// shotLitFraction decodes a PNG and counts pixels that are not pure black. "Solid black" was the
// merge-time capture defect, so this is the assertion that a screenshot actually holds a rendering.
func shotLitFraction(t *testing.T, path string) (lit, total int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			total++
			if r|g|bl != 0 {
				lit++
			}
		}
	}
	return lit, total
}
