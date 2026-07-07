package sysactivity

import "testing"

func TestProcessNameMatching(t *testing.T) {
	set := map[string]bool{}
	addProcessNames(set, "Traktor.exe")
	addProcessNames(set, "OBS64.EXE")
	addProcessNames(set, "  ")

	for _, name := range []string{"Traktor", "traktor", "TRAKTOR.EXE", "obs64", "OBS64.exe"} {
		if !Running(set, name) {
			t.Errorf("Running(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "notepad", "trakto"} {
		if Running(set, name) {
			t.Errorf("Running(%q) = true, want false", name)
		}
	}
}

// TestNewReturnsActivity ensures the platform constructor wires up (real or no-op).
func TestNewReturnsActivity(t *testing.T) {
	a := New()
	if a == nil {
		t.Fatal("New() = nil")
	}
	// Calls must not panic on any platform; ok may be false on non-Windows.
	_, _ = a.IdleDuration()
	_, _ = a.RunningProcesses()
}
