package featurehost

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// TestMain re-execs as a feature child when the env marker is set (the host spawns the
// test binary itself - no separate exe needed).
func TestMain(m *testing.M) {
	if name := os.Getenv("RAVE_MATE_TEST_FEATURE"); name != "" {
		os.Exit(RunFeature(name))
	}
	os.Exit(m.Run())
}

// newTestHost builds a Host that re-execs this test binary as the crash feature.
func newTestHost(t *testing.T, log *logbus.Bus, onEvent map[string]func(json.RawMessage)) *Host {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{
		Name:    "crash",
		Log:     log,
		Init:    func() any { return crashInit{TickMS: 20} },
		OnEvent: onEvent,
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

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestHostStartCallStop(t *testing.T) {
	log := logbus.New(500)
	var ticks atomic.Int64
	h := newTestHost(t, log, map[string]func(json.RawMessage){
		"tick": func(json.RawMessage) { ticks.Add(1) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()

	waitFor(t, "ready", 10*time.Second, h.Running)

	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()
	res, err := h.Call(cctx, "echo", map[string]int{"v": 7})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if string(res) != `{"v":7}` {
		t.Fatalf("echo result %s", res)
	}

	waitFor(t, "tick events", 5*time.Second, func() bool { return ticks.Load() > 2 })

	// Child logbus forwarding: "log" method emits a child entry → daemon bus, with proc tag.
	if _, err := h.Call(cctx, "log", nil); err != nil {
		t.Fatalf("log call: %v", err)
	}
	waitFor(t, "forwarded log entry", 5*time.Second, func() bool {
		for _, e := range log.Snapshot() {
			if e.Source == "crash" && e.Msg == "requested log line" && e.Fields["proc"] == "crash" {
				return true
			}
		}
		return false
	})

	h.Stop()
	if h.Running() {
		t.Fatal("still running after Stop")
	}
}

func TestHostRestartsAfterPanic(t *testing.T) {
	old := backoffSchedule
	backoffSchedule = []time.Duration{50 * time.Millisecond}
	defer func() { backoffSchedule = old }()

	log := logbus.New(500)
	h := newTestHost(t, log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	waitFor(t, "ready", 10*time.Second, h.Running)

	// Panic in a handler kills the child (after answering) → host restarts it.
	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	_, err := h.Call(cctx, "panic", nil)
	ccancel()
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("want panic error, got %v", err)
	}

	waitFor(t, "restart", 15*time.Second, func() bool {
		r, _ := h.Stats()
		return r >= 1 && h.Running()
	})

	// Feature is functional again post-restart.
	cctx, ccancel = context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()
	if _, err := h.Call(cctx, "echo", "ok"); err != nil {
		t.Fatalf("echo after restart: %v", err)
	}
	if _, lastErr := h.Stats(); lastErr == "" {
		t.Fatal("Stats lastErr empty after a crash")
	}
}

func TestHostHardExitFailsPendingAndRestarts(t *testing.T) {
	old := backoffSchedule
	backoffSchedule = []time.Duration{50 * time.Millisecond}
	defer func() { backoffSchedule = old }()

	log := logbus.New(500)
	h := newTestHost(t, log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.Stop()
	waitFor(t, "ready", 10*time.Second, h.Running)

	// "exit" never responds - the Call must fail via child-death pending cleanup, not hang.
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	_, err := h.Call(cctx, "exit", nil)
	ccancel()
	if err == nil {
		t.Fatal("want error from exited child")
	}
	waitFor(t, "restart after exit", 15*time.Second, h.Running)
}

func TestHostStopWhileBackoff(t *testing.T) {
	old := backoffSchedule
	backoffSchedule = []time.Duration{time.Hour} // park in backoff
	defer func() { backoffSchedule = old }()

	log := logbus.New(500)
	h := newTestHost(t, log, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := h.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "ready", 10*time.Second, h.Running)

	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	_, _ = h.Call(cctx, "exit", nil)
	ccancel()
	waitFor(t, "down", 10*time.Second, func() bool { return !h.Running() })

	done := make(chan struct{})
	go func() { h.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop hung during backoff")
	}
}
