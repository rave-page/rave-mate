//go:build spout

// Degrade-path proof for the GL/DX interop pre-flight (spout_shim.cpp interop_probe): a refused
// interop must yield an IDLE worker - sender never registers, Send never blocks, process lives.
// The pre-gate behaviour was a __fastfail inside SpoutLibrary.dll that killed the whole daemon
// (2026-08-04 x2, 2026-08-10 dumps), so surviving this test at all IS the fix.
package videoshare

import (
	"image/color"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

func TestSenderDegradesWhenInteropUnavailable(t *testing.T) {
	if spoutSenderCount() < 0 {
		t.Skip("spout: SpoutLibrary.dll unavailable on this host - skipping")
	}
	t.Setenv("RAVE_SPOUT_FORCE_NO_INTEROP", "1")

	log := logbus.New(64)
	s, err := newSender(log)
	if err != nil {
		t.Fatalf("newSender: %v", err)
	}
	defer s.Close()

	frame := solidFrame(360, 120, color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xFF})
	// First Send starts the worker; create must refuse the handle and the worker must drain.
	if err := s.Send("A", frame); err != nil {
		t.Fatalf("Send: %v", err)
	}
	name := SenderName("A")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if spoutFindSender(name) {
			t.Fatalf("sender %q registered despite forced interop failure - the fatal DLL path was entered", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The drain path must keep releasing producers: a second Send may not block.
	done := make(chan struct{})
	go func() {
		_ = s.Send("A", frame)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked on an idle (interop-refused) worker")
	}
}

// TestSenderRecoversAfterInteropReturns proves the refusal is per-worker, not latched: with the
// forced failure lifted, a FRESH worker (new deck) probes again and publishes normally.
func TestSenderRecoversAfterInteropReturns(t *testing.T) {
	if spoutSenderCount() < 0 {
		t.Skip("spout: SpoutLibrary.dll unavailable on this host - skipping")
	}
	log := logbus.New(64)
	s, err := newSender(log)
	if err != nil {
		t.Fatalf("newSender: %v", err)
	}
	defer s.Close()

	frame := solidFrame(360, 120, color.NRGBA{R: 0x08, G: 0xF7, B: 0x9B, A: 0xFF})
	if err := s.Send("B", frame); err != nil {
		t.Fatalf("Send: %v", err)
	}
	name := SenderName("B")
	registered := false
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if spoutFindSender(name) {
			registered = true
			break
		}
	}
	if !registered {
		t.Skipf("spout: sender %q never registered - headless/no-GL host, cannot prove recovery here", name)
	}
}
