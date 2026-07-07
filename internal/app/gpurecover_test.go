package app

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/gpuwatch"
	"rave.page/mate/internal/logbus"
)

// newTestRec builds a gpuRecovery wired to counters + a temp history file (no real relaunch/exit).
func newTestRec(t *testing.T, histPath string, relaunches *int) *gpuRecovery {
	t.Helper()
	return &gpuRecovery{
		log:              logbus.New(16),
		relaunch:         func() error { *relaunches++; return nil },
		scheduleHardExit: func() {}, // don't os.Exit the test process
		quit:             func() {},
		historyPath:      histPath,
	}
}

// A UI hang triggers exactly one relaunch per process; the budget (persisted across instances)
// pauses auto-recovery after gpuMaxRestarts within the window.
func TestGPURecoveryBudget(t *testing.T) {
	hist := filepath.Join(t.TempDir(), "gpu-restart-history.json")
	total := 0
	hung := gpuwatch.Fault{Kind: gpuwatch.FaultHungWindow, Detail: "test"}

	// Each simulated instance is a fresh gpuRecovery sharing the persisted history file.
	for i := 1; i <= gpuMaxRestarts; i++ {
		rec := newTestRec(t, hist, &total)
		rec.onFault(hung)
		if total != i {
			t.Fatalf("instance %d: relaunches=%d, want %d", i, total, i)
		}
	}
	// Budget spent - the next instance must NOT relaunch.
	rec := newTestRec(t, hist, &total)
	rec.onFault(hung)
	if total != gpuMaxRestarts {
		t.Fatalf("over-budget relaunch fired: relaunches=%d, want %d", total, gpuMaxRestarts)
	}
}

// Two faults in one process relaunch only once (guarded by `restarting`).
func TestGPURecoveryOncePerProcess(t *testing.T) {
	hist := filepath.Join(t.TempDir(), "h.json")
	total := 0
	rec := newTestRec(t, hist, &total)
	hung := gpuwatch.Fault{Kind: gpuwatch.FaultHungWindow}
	rec.onFault(hung)
	rec.onFault(hung)
	if total != 1 {
		t.Fatalf("relaunches=%d, want 1 (re-entry must be suppressed)", total)
	}
}

// A logged TDR alone never restarts, but does fan out to registered reset consumers.
func TestGPURecoveryTDRNoRestart(t *testing.T) {
	total := 0
	rec := newTestRec(t, filepath.Join(t.TempDir(), "h.json"), &total)
	var mu sync.Mutex
	got := ""
	rec.OnGPUReset(func(d string) { mu.Lock(); got = d; mu.Unlock() })
	rec.onFault(gpuwatch.Fault{Kind: gpuwatch.FaultTDR, Detail: "nvlddmkm (event 4101)"})
	if total != 0 {
		t.Fatalf("TDR triggered a restart (relaunches=%d, want 0)", total)
	}
	mu.Lock()
	defer mu.Unlock()
	if got != "nvlddmkm (event 4101)" {
		t.Fatalf("reset consumer got %q", got)
	}
}

// pruneHistory drops entries older than the window (so the budget resets after a healthy stretch).
func TestPruneHistory(t *testing.T) {
	now := time.Now()
	in := []time.Time{now.Add(-10 * time.Minute), now.Add(-1 * time.Minute), now}
	out := pruneHistory(in, gpuRestartWindow)
	if len(out) != 2 {
		t.Fatalf("pruned=%d, want 2", len(out))
	}
}
