package featurehost

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// newHBHost builds a heartbeat-monitored Host that re-execs this test binary as the crash feature.
func newHBHost(t *testing.T, log *logbus.Bus, timeout time.Duration) *Host {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{
		Name:             "crash",
		Log:              log,
		Init:             func() any { return crashInit{TickMS: 20} },
		HeartbeatTimeout: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.command = func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "RAVE_MATE_TEST_FEATURE=crash")
		return cmd
	}
	return h
}

// A feature that stays alive but stops beating (wedged loop) is force-restarted by the host.
func TestHostRestartsHungFeature(t *testing.T) {
	old := backoffSchedule
	backoffSchedule = []time.Duration{50 * time.Millisecond}
	defer func() { backoffSchedule = old }()

	log := logbus.New(500)
	h := newHBHost(t, log, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	waitFor(t, "ready", 10*time.Second, h.Running)
	r0, _ := h.Stats()

	// Wedge the child's work loop: process stays alive but stops beating → monitor kills → restart.
	cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
	if _, err := h.Call(cctx, "wedge", nil); err != nil {
		t.Fatalf("wedge: %v", err)
	}
	ccancel()

	waitFor(t, "restart after hang", 15*time.Second, func() bool {
		r, _ := h.Stats()
		return r > r0
	})
}

// A healthy, beating feature is NOT restarted by the heartbeat monitor.
func TestHostKeepsHealthyFeature(t *testing.T) {
	log := logbus.New(500)
	h := newHBHost(t, log, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	waitFor(t, "ready", 10*time.Second, h.Running)

	time.Sleep(3 * time.Second) // > timeout: beats must keep it alive
	if r, _ := h.Stats(); r != 0 {
		t.Fatalf("healthy feature restarted %d times", r)
	}
	if !h.Running() {
		t.Fatal("healthy feature not running")
	}
}
