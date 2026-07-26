//go:build zigui && windows

package webui

// B6 gates: the SAME B5 transport/ctl/shutdown suite, driven against the Zig rave-shell exe
// (native/zigui src/shell) instead of the Go re-exec child. The Zig child implements the loopback
// page model + the deaf/crash/slow test modes, so every gate below runs a REAL foreign-language
// process behind the unchanged daemon-side PSH1 code. Skips cleanly when the exe is not built
// (scripts/build-zig.sh). The one real-window smoke is opt-in via RAVE_MATE_WEBVIEW_SMOKE=1 and
// asserts >1% non-black pixels (the B5 hidden-window lesson).

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/sysexec"
)

func zigShellTestExe(t *testing.T) string {
	t.Helper()
	if p := os.Getenv(zigShellExeEnv); p != "" {
		return p
	}
	p := filepath.Join("..", "..", "native", "zigui", "zig-out", "bin", "rave-shell.exe")
	if _, err := os.Stat(p); err != nil {
		t.Skip("rave-shell not built (bash scripts/build-zig.sh)")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// zigTestUI is procTestUI over the Zig child: same loopback fixture, same modes (via the
// RAVE_MATE_WEBVIEW_TEST_MODE env the Zig exe honors), production-parity sysexec.Hide spawn.
func zigTestUI(t *testing.T, mode string) (*UI, *procShell) {
	t.Helper()
	exe := zigShellTestExe(t)
	return procTestUIWith(t, func() *exec.Cmd {
		cmd := exec.Command(exe, "feature", procFeatureName)
		cmd.Env = append(os.Environ(), procTestModeEnv+"="+mode)
		sysexec.Hide(cmd)
		return cmd
	})
}

func TestZigShellCtlSuiteThroughChild(t *testing.T) {
	u, ps := zigTestUI(t, procModeNormal)
	runCtlSuiteGate(t, u, ps)
}

func TestZigShellOrderedLaneIsFIFO(t *testing.T) {
	u, _ := zigTestUI(t, procModeNormal)
	runOrderedFIFOGate(t, u)
}

func TestZigShellDirectLaneUnblockedByBusyChild(t *testing.T) {
	u, ps := zigTestUI(t, procModeSlowApply)
	runDirectLaneBusyGate(t, u, ps)
}

func TestZigShellWedgedChildTerminatesInGrace(t *testing.T) {
	u, ps := zigTestUI(t, procModeDeafStdin)
	runWedgedTerminateGate(t, u, ps)
}

func TestZigShellCrashedChildRestartsAndReattaches(t *testing.T) {
	u, ps := zigTestUI(t, procModeCrash)
	runCrashRestartGate(t, u, ps)
}

func TestZigShellCleanQuit(t *testing.T) {
	_, ps := zigTestUI(t, procModeNormal)
	runCleanQuitGate(t, ps)
}

func TestZigShellCtlRoundTripCost(t *testing.T) {
	u, _ := zigTestUI(t, procModeNormal)
	runRoundTripCostGate(t, u)
}

// TestZigShellWindowedSmoke is the B6 real-window gate: genuine WebView2 window in the Zig child,
// spawned hidden exactly as production does, captures asserted >1% non-black (never "non-empty
// PNG" - a solid-black file passes that; ZIG_UI_GUIDE.md §5).
func TestZigShellWindowedSmoke(t *testing.T) {
	if os.Getenv(smokeEnv) == "" {
		t.Skip("set " + smokeEnv + "=1 to run the real windowed smoke (opens a WebView2 window)")
	}
	exe := zigShellTestExe(t)
	runWindowedSmoke(t, func() *exec.Cmd {
		cmd := exec.Command(exe, "feature", procFeatureName)
		cmd.Env = os.Environ()
		sysexec.Hide(cmd) // production parity: the child's first window inherits SW_HIDE
		return cmd
	})
}
