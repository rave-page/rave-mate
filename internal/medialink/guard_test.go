package medialink

import "testing"

// TestGuardRecoversPanic verifies a panicking route goroutine is contained (recovered + logged),
// so one route's Go-level fault can't crash the whole daemon.
func TestGuardRecoversPanic(t *testing.T) {
	rm := &RouteManager{} // warnf is nil-log safe
	ran := false
	rm.guard("panicky", func() { ran = true; panic("boom") })
	if !ran {
		t.Fatal("guarded fn did not run")
	}
	// Reaching here means the panic was recovered (no crash). A clean fn must also pass through.
	got := 0
	rm.guard("clean", func() { got = 42 })
	if got != 42 {
		t.Fatalf("clean guarded fn: got %d", got)
	}
}
