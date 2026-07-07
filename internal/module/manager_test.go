package module

import (
	"context"
	"testing"

	"rave.page/mate/internal/logbus"
)

// TestStartEnabledContainsPanic: a module that panics on start must be contained - it isn't
// left "running", and other modules still start. This is the "a module can't crash the app".
func TestStartEnabledContainsPanic(t *testing.T) {
	m := NewManager(logbus.New(16), context.Background())
	started := false
	m.Add(&Service{Name: "bad", Enabled: func() bool { return true },
		Start: func(context.Context) error { panic("boom") }})
	m.Add(&Service{Name: "good", Enabled: func() bool { return true },
		Start: func(context.Context) error { started = true; return nil }})

	m.StartEnabled() // must not panic

	if m.IsRunning("bad") {
		t.Fatal("panicking module must not be left running")
	}
	if !started || !m.IsRunning("good") {
		t.Fatal("a sibling module must still start despite the panicking one")
	}
}

// TestCallStopRecovers: a panic in Stop must not propagate (shutdown stays clean).
func TestCallStopRecovers(t *testing.T) {
	m := NewManager(logbus.New(16), context.Background())
	m.Add(&Service{Name: "x", Enabled: func() bool { return true },
		Start: func(context.Context) error { return nil },
		Stop:  func() { panic("teardown boom") }})
	m.StartEnabled()
	m.StopAll() // must not panic
	if m.IsRunning("x") {
		t.Fatal("module should be stopped")
	}
}
