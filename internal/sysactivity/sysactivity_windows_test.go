//go:build windows

package sysactivity

import "testing"

// TestWindowsLive sanity-checks the real syscalls: idle is reported, and the snapshot finds the
// test process itself (go test runs as some .exe).
func TestWindowsLive(t *testing.T) {
	a := New()
	if _, ok := a.IdleDuration(); !ok {
		t.Error("IdleDuration ok=false on Windows")
	}
	set, ok := a.RunningProcesses()
	if !ok || len(set) == 0 {
		t.Fatalf("RunningProcesses ok=%v len=%d", ok, len(set))
	}
	// The system process is always present.
	if !Running(set, "System") && !Running(set, "svchost") {
		t.Errorf("expected a core system process in the snapshot (%d entries)", len(set))
	}
}
