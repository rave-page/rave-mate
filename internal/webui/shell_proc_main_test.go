package webui

// TestMain doubles as the B5 window-child entrypoint: with the child env marker set, this process
// hosts the `webview` feature over stdio instead of running tests (the same trick
// internal/featurehost's own tests use - a real process with real pipes, no exe to ship).

import (
	"os"
	"testing"
	"time"

	"rave.page/mate/internal/featurehost"
)

// procCrashAfter is how long the crash-mode child lives past spawn: long enough to complete the init
// handshake + ready, short enough to keep the restart gate quick.
const procCrashAfter = 1200 * time.Millisecond

func TestMain(m *testing.M) {
	if os.Getenv(procTestChildEnv) == "" {
		os.Exit(m.Run())
	}
	switch os.Getenv(procTestModeEnv) {
	case procModeDeafStdin:
		lbStall = true // block inside the stdin reader: a child that stopped consuming
	case procModeSlowApply:
		lbApplyDelay = 2 * time.Millisecond // busy UI thread; the reader stays free
	case procModeCrash:
		go func() {
			time.Sleep(procCrashAfter)
			os.Exit(3) // hard crash: the daemon must survive, restart us, and rebuild the page
		}()
	}
	os.Exit(featurehost.RunFeature(procFeatureName))
}
