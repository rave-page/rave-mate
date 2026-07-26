package webui

// B5 gates: the whole ctl suite driven through a REAL child process, plus the transport contracts
// the phase plan calls out - ordering, lane isolation, caps, reattach, and every shutdown path
// proven BY EXECUTION (a wedged child, a crashed child, a clean quit).
//
// The child here is the test binary re-exec'd as `feature webview` in loopback-page mode
// (procInit.Virtual): real process, real pipes, real featurehost supervision, no WebView2. One real
// WINDOWED smoke lives in shell_proc_smoke_test.go for the orchestrator to run at merge.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/config"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/ui"
)

// procTestChildEnv makes TestMain (shell_proc_main_test.go) host the webview feature instead of
// running tests. RAVE_MATE_WEBVIEW_TEST_MODE selects the child's behaviour.
const (
	procTestChildEnv  = "RAVE_MATE_WEBVIEW_CHILD"
	procTestModeEnv   = "RAVE_MATE_WEBVIEW_TEST_MODE"
	procModeNormal    = ""
	procModeDeafStdin = "deaf"  // reads nothing from stdin after init → wedges the daemon's writes
	procModeCrash     = "crash" // exits hard shortly after ready → exercises supervised restart
	procModeSlowApply = "slow"  // reads stdin promptly, applies ordered evals slowly
)

// lbDoc is the fixture the loopback child answers ctl queries from.
var lbDoc = lbMakeFixtureDoc(lbFixture{
	Snapshot: "main\n  button {tab=library} \"Library\"\n  div [row] \"Track A\"",
	Reads:    map[string]string{"volume": "0.80", "search": ""},
	Clicks:   []string{"Library", "tab=library", "Track A"},
})

// procTestUI builds a UI whose shell is a procShell over a real loopback child, started and ready.
func procTestUI(t *testing.T, mode string) (*UI, *procShell) {
	t.Helper()
	log := logbus.New(512)
	shellLog = log
	procVirtualChild = true
	procChildCmd = func() *exec.Cmd {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=TestProcChildNoop")
		cmd.Env = append(os.Environ(), procTestChildEnv+"=1", procTestModeEnv+"="+mode)
		return cmd
	}
	t.Cleanup(func() { procVirtualChild, procChildCmd, shellLog = false, nil, nil })

	u := &UI{svc: ui.Services{Cfg: &config.Config{}, Log: log}, log: log, active: "live",
		started: time.Now(), stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	sh, ok := newProcShell("rave-mate test", 800, 600, u.onAction, nil)
	if !ok {
		t.Fatal("newProcShell failed")
	}
	ps := sh.(*procShell)
	u.shell = ps
	ps.onReattach = u.reattach
	ps.onDrop = u.dropFragCache
	go u.evalFlusher()

	ready := make(chan struct{})
	ps.onReady = func() { close(ready) }
	go ps.run(lbDoc, false)
	select {
	case <-ready:
	case <-time.After(20 * time.Second):
		t.Fatal("child never reported ready")
	}
	t.Cleanup(func() {
		ps.terminate()
		select {
		case <-ps.done:
		case <-time.After(15 * time.Second):
			t.Error("procShell.terminate did not unblock run()")
		}
		close(u.stop)
		releaseUIState(u)
	})
	return u, ps
}

// TestProcChildNoop is the -test.run target the child process is launched with: TestMain hands the
// process to RunFeature before any test executes, so this never runs in the child. It exists so the
// spawn line names a real (empty) test in the parent's eyes.
func TestProcChildNoop(t *testing.T) {}

// ── gate 1: the whole ctl suite through the child ──

func TestProcShellCtlSuiteThroughChild(t *testing.T) {
	u, ps := procTestUI(t, procModeNormal)

	if got := u.Snapshot(); !strings.Contains(got, "Track A") {
		t.Fatalf("Snapshot through the child = %q", got)
	}
	if !u.Click("Library") {
		t.Error("Click(Library) missed")
	}
	if u.Click("nothing-like-this") {
		t.Error("Click of an absent label reported a hit")
	}
	if !u.Tap(10, 20) {
		t.Error("Tap missed")
	}
	if !u.TapSecondary(10, 20) {
		t.Error("TapSecondary missed")
	}
	if !u.Type("hello") {
		t.Error("Type missed")
	}
	if v, ok := u.Read("volume"); !ok || v != "0.80" {
		t.Errorf("Read(volume) = %q,%v", v, ok)
	}
	if _, ok := u.Read("no-such-field"); ok {
		t.Error("Read of an absent field reported a hit")
	}
	if !u.Set("volume", "0.50") {
		t.Error("Set missed")
	}
	if !u.Scroll(120) {
		t.Error("Scroll failed")
	}
	u.Resize(1024, 768)

	// tab + act ride the ordered lane through window.rave, and the child replays them as actions:
	// the reply barrier proves the act came back and the switch ran on the act worker.
	acts := procDrainActs(u)
	if !u.Act("__probe-act", "v1") {
		t.Fatal("Act refused")
	}
	procWaitFor(t, "act replay through the child", func() bool {
		for _, a := range acts() {
			if strings.Contains(a, "__probe-act") {
				return true
			}
		}
		return false
	})
	u.setTabViaActs("logs")
	if u.activeTab() != "logs" {
		t.Errorf("setTabViaActs left the tab at %q", u.activeTab())
	}

	// screenshot / screenshot-all: no HWND behind a loopback child, so the sweep must still complete
	// and report the failure per tab rather than hanging or panicking.
	if err := u.Screenshot(t.TempDir() + "/x.png"); err == nil {
		t.Error("Screenshot without a window handle should error")
	}
	dir := t.TempDir()
	head, err := u.ScreenshotAll(dir)
	if err != nil {
		t.Fatalf("ScreenshotAll: %v", err)
	}
	if !strings.Contains(head, "tabs,") {
		t.Errorf("ScreenshotAll head = %q", head)
	}
	rep, err := os.ReadFile(dir + "/report.txt")
	if err != nil || !strings.Contains(string(rep), "live") {
		t.Errorf("report.txt = %q (%v)", rep, err)
	}
	if gens, dropped, restarts, lastErr := ps.Stats(); gens != 1 || dropped != 0 || restarts != 0 {
		t.Errorf("shell stats after the ctl suite: gens=%d dropped=%d restarts=%d err=%q",
			gens, dropped, restarts, lastErr)
	}
}

// ── gate 2: ordering + lane isolation ──

// The ordered lane is FIFO end-to-end: the daemon's enqueue order is the child's application order.
// Proven through a real process by tagging each batch with an act the child replays back.
func TestProcShellOrderedLaneIsFIFO(t *testing.T) {
	u, _ := procTestUI(t, procModeNormal)
	acts := procDrainActs(u)
	const n = 60
	for i := 0; i < n; i++ {
		u.eval("window.rave(" + jsQuote(fmt.Sprintf(`{"act":"ord","val":"%d"}`, i)) + ")")
	}
	procWaitFor(t, "all ordered frames applied", func() bool { return len(procOrdVals(acts())) >= n })
	got := procOrdVals(acts())
	for i, v := range got[:n] {
		if v != i {
			t.Fatalf("ordered lane reordered: position %d carried %d (%v)", i, v, got[:n])
		}
	}
}

// The writer drains the DIRECT lane first: a ctl frame overtakes a full ordered queue on the shared
// pipe. Observed at the writer, because that is where the policy lives.
func TestProcShellWriterDrainsDirectLaneFirst(t *testing.T) {
	s := &procShell{ord: make(chan procFrame, procOrdQueueCap), dir: make(chan procFrame, procDirQueueCap),
		done: make(chan struct{}), stopW: make(chan struct{}), log: logbus.New(16)}
	var mu sync.Mutex
	var order []string
	sent := make(chan struct{}, 64)
	s.sendFn = func(ev string, _ any) error {
		mu.Lock()
		order = append(order, ev)
		mu.Unlock()
		sent <- struct{}{}
		return nil
	}
	for i := 0; i < procOrdQueueCap; i++ { // ordered lane at its cap before the ctl frame exists
		s.eval(fmt.Sprintf("js-%d", i))
	}
	s.evalDirect("return 1")
	go s.writer()
	t.Cleanup(func() { close(s.stopW) })
	for i := 0; i < procOrdQueueCap+1; i++ {
		select {
		case <-sent:
		case <-time.After(5 * time.Second):
			t.Fatal("writer stalled")
		}
	}
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if got[0] != procEvXEval {
		t.Fatalf("writer sent %v first; the direct lane must overtake a full ordered queue (%v)", got[0], got)
	}
	for _, ev := range got[1:] {
		if ev != procEvEval {
			t.Fatalf("unexpected frame after the direct one: %v", got)
		}
	}
}

// End to end: with a busy child UI thread (slow applies, reader free) a ctl round-trip still returns
// inside budget - the ordered lane never heads it, and the ≤1-un-acked-batch rule bounds what the
// child's own apply queue can be holding.
func TestProcShellDirectLaneUnblockedByBusyChild(t *testing.T) {
	u, ps := procTestUI(t, procModeSlowApply)
	for i := 0; i < 40; i++ {
		u.eval(fmt.Sprintf("window.__patch('flood-%d','x')", i))
	}
	start := time.Now()
	v, ok := u.evalValue("return window.__read(" + jsQuote("volume") + ")")
	took := time.Since(start)
	if !ok || v != "0.80" {
		t.Fatalf("direct lane answered %v,%v after %v", v, ok, took)
	}
	if took > evalTimeout/2 {
		t.Errorf("direct lane took %v against a busy child (budget %v)", took, evalTimeout)
	}
	t.Logf("direct-lane round trip against a busy child: %v (ordered drops: %d)",
		took.Truncate(time.Microsecond), ps.dropped.Load())
}

// Ordered-lane overflow drops the OLDEST and wipes the fragment caches, so a dropped patch re-emits
// instead of sticking stale. Driven at the shell, with the queue deliberately not drained.
func TestProcShellOrderedOverflowDropsOldestAndWipesCache(t *testing.T) {
	drops := 0
	s := &procShell{ord: make(chan procFrame, procOrdQueueCap), dir: make(chan procFrame, procDirQueueCap),
		done: make(chan struct{}), stopW: make(chan struct{}), log: logbus.New(16)}
	s.onDrop = func() { drops++ }
	for i := 0; i < procOrdQueueCap+5; i++ {
		s.eval(fmt.Sprintf("js-%d", i))
	}
	if drops != 5 || s.dropped.Load() != 5 {
		t.Fatalf("drops = %d (counter %d), want 5", drops, s.dropped.Load())
	}
	if len(s.ord) != procOrdQueueCap {
		t.Fatalf("queue length %d, want the cap %d", len(s.ord), procOrdQueueCap)
	}
	first := (<-s.ord).data.(procEval)
	if first.JS != "js-5" {
		t.Fatalf("oldest survivor = %q, want js-5 (drop-OLDEST)", first.JS)
	}
	// The direct lane refuses instead of dropping: a ctl round trip must never get a stale answer.
	for i := 0; i < procDirQueueCap; i++ {
		if !s.sendDirect(procEvShow, struct{}{}) {
			t.Fatalf("direct lane refused below its cap at %d", i)
		}
	}
	if s.sendDirect(procEvShow, struct{}{}) {
		t.Error("direct lane accepted a frame past its cap")
	}
}

// ── gate 3: shutdown paths, each proven by execution ──

// A wedged child (stops reading stdin) must not hang the daemon: terminate() completes within grace
// and run() returns, so app.go's shutdown() always executes.
func TestProcShellWedgedChildTerminatesInGrace(t *testing.T) {
	u, ps := procTestUI(t, procModeDeafStdin)
	// Fill the pipe so the writer is stuck mid-write on a child that will never read again.
	big := strings.Repeat("x", 128*1024)
	for i := 0; i < 8; i++ {
		u.eval("window.__patch('wedge'," + jsQuote(big) + ")")
	}
	start := time.Now()
	ps.terminate()
	select {
	case <-ps.done:
	case <-time.After(procQuitGrace + 10*time.Second):
		t.Fatal("terminate() never unblocked run() against a wedged child")
	}
	t.Logf("wedged-child terminate completed in %v (grace %v)", time.Since(start).Truncate(time.Millisecond), procQuitGrace)
}

// A crashed child must not kill the daemon: it is restarted and the page is rebuilt from state
// (reattach = fresh document + full patchMain).
func TestProcShellCrashedChildRestartsAndReattaches(t *testing.T) {
	var reattached atomic.Int64
	u, ps := procTestUI(t, procModeCrash)
	ps.onReattach = func() { reattached.Add(1); u.reattach() }
	procWaitForLong(t, "supervised restart after a crash", func() bool {
		gens, _, restarts, _ := ps.Stats()
		return gens >= 2 && restarts >= 1
	})
	procWaitFor(t, "page rebuilt after the restart", func() bool { return reattached.Load() >= 1 })
	gens, _, restarts, lastErr := ps.Stats()
	t.Logf("child generations=%d restarts=%d lastErr=%q reattaches=%d", gens, restarts, lastErr, reattached.Load())
	if lastErr == "" {
		t.Error("the crash was not recorded on the host (ctl must be able to report it)")
	}
}

// A clean quit: the child unwinds on its own, well inside grace.
func TestProcShellCleanQuit(t *testing.T) {
	_, ps := procTestUI(t, procModeNormal)
	start := time.Now()
	ps.terminate()
	select {
	case <-ps.done:
	case <-time.After(procQuitGrace + 5*time.Second):
		t.Fatal("clean quit did not unblock run()")
	}
	if took := time.Since(start); took > procQuitGrace {
		t.Errorf("clean quit took %v - past grace, so the kill path ran instead of the child unwinding", took)
	}
}

// ── gate 4: the ctl hop cost against the budgets in control.go ──

func TestProcShellCtlRoundTripCost(t *testing.T) {
	u, _ := procTestUI(t, procModeNormal)
	const n = 200
	var total time.Duration
	worst := time.Duration(0)
	for i := 0; i < n; i++ {
		start := time.Now()
		if _, ok := u.evalValue("return window.__read(" + jsQuote("volume") + ")"); !ok {
			t.Fatalf("round trip %d timed out", i)
		}
		d := time.Since(start)
		total += d
		if d > worst {
			worst = d
		}
	}
	avg := total / n
	t.Logf("ctl round trip over PSH1: avg %v, worst %v (evalTimeout %v, ScreenshotAll settle 300ms)",
		avg.Truncate(time.Microsecond), worst.Truncate(time.Microsecond), evalTimeout)
	if worst > evalTimeout/3 {
		t.Errorf("worst hop %v eats more than a third of the %v budget - raise it explicitly", worst, evalTimeout)
	}
}

// ── gate 5: the runtime JS crosses the wire verbatim ──

// The child injects the DAEMON's runtime bytes, not a compiled-in copy: __rt's SVG ids and __mse's
// data-mse contract are byte-agreements with the Go renderers, and B6's Zig child has no copy at all.
func TestProcShellRuntimeJSCrossesVerbatim(t *testing.T) {
	_, ps := procTestUI(t, procModeNormal)
	raw, err := json.Marshal(ps.initParams())
	if err != nil {
		t.Fatal(err)
	}
	var ini procInit
	if err := json.Unmarshal(raw, &ini); err != nil {
		t.Fatal(err)
	}
	if ini.RuntimeJS != runtimeJS {
		t.Fatalf("init runtimeJS differs from the in-proc bytes (%d vs %d bytes)", len(ini.RuntimeJS), len(runtimeJS))
	}
	// The byte-contracted surfaces: __rt's element ids, __mse's data-mse attributes, and the ctl
	// introspection entry points the daemon's evalValue wrapper calls into.
	for _, needle := range []string{"window.__rt=", "function __mseScan()", "data-mse",
		"window.__snapshot", "window.__patch", "-veil"} {
		if !strings.Contains(ini.RuntimeJS, needle) {
			t.Errorf("wire runtimeJS lost %q", needle)
		}
	}
	// And the child's injection point returns exactly those bytes, with no fallback to the const.
	saved := webviewInitJS
	t.Cleanup(func() { webviewInitJS = saved })
	webviewInitJS = ini.RuntimeJS
	if shellInitJS() != runtimeJS {
		t.Error("shellInitJS() did not inject the wire bytes verbatim")
	}
	webviewInitJS = ""
	if shellInitJS() != runtimeJS {
		t.Error("shellInitJS() must fall back to the compiled-in runtime in the daemon")
	}
}

// The child is a pure view: nothing in its init payload asks it to read config or open a database.
func TestProcShellChildGetsEverythingInInit(t *testing.T) {
	_, ps := procTestUI(t, procModeNormal)
	p := ps.initParams().(procInit)
	if p.DataDir == "" {
		t.Error("child was not given a resolved WebView2 profile dir (it must not read config)")
	}
	if p.RuntimeJS == "" || p.Title == "" || p.W == 0 || p.H == 0 {
		t.Errorf("init payload incomplete: %+v", procInit{Title: p.Title, W: p.W, H: p.H})
	}
	if !p.Virtual {
		t.Error("test child must run the loopback page model")
	}
}

// ── helpers ──

// procDrainActs collects every act the child replays into the daemon.
func procDrainActs(u *UI) func() []string {
	var mu sync.Mutex
	var got []string
	ps := u.shell.(*procShell)
	prev := ps.onAction
	ps.onAction = func(p string) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		if prev != nil {
			prev(p)
		}
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// procOrdVals extracts the ordered-lane probe values, in arrival order.
func procOrdVals(acts []string) []int {
	var out []int
	for _, a := range acts {
		var m struct {
			Act string `json:"act"`
			Val string `json:"val"`
		}
		if json.Unmarshal([]byte(a), &m) != nil || m.Act != "ord" {
			continue
		}
		var v int
		if _, err := fmt.Sscanf(m.Val, "%d", &v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func procWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	procWaitUntil(t, what, 15*time.Second, cond)
}

func procWaitForLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	procWaitUntil(t, what, 40*time.Second, cond) // restart backoff starts at 1s
}

func procWaitUntil(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
