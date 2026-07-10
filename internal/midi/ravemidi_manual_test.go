//go:build windows && manual

package midi

// On-hardware bring-up test for the ravemidi kernel driver. Needs the driver installed
// (driver/ravemidi/build/testsign/step1+step2). Run:
//   GOWORK=off go test -tags manual -run TestRaveMIDIBringUp -v ./internal/midi/

import (
	"slices"
	"testing"
	"time"
)

func TestRaveMIDIBringUp(t *testing.T) {
	if !raveMIDIAvailable() {
		t.Skip("ravemidi control device not available (driver not installed / not running)")
	}

	const portName = "ravemidi bringup"
	p, err := openRaveMIDIOut(portName)
	if err != nil {
		t.Fatalf("create port: %v", err)
	}
	defer p.Close()

	// winmm enumeration is PnP-driven; give the interface a moment to publish.
	deadline := time.Now().Add(5 * time.Second)
	var ins []string
	for {
		ins, err = Ports() // midiIn devices = what DJ apps see
		if err != nil {
			t.Fatalf("list midiIn: %v", err)
		}
		if slices.Contains(ins, portName) || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !slices.Contains(ins, portName) {
		t.Fatalf("port %q not in midiIn list (FriendlyName stamp or capture pin broken): %v", portName, ins)
	}

	// OUT_ONLY must NOT surface a midiOut endpoint (that's the echo path we kill).
	outs, err := OutPorts()
	if err != nil {
		t.Fatalf("list midiOut: %v", err)
	}
	if slices.Contains(outs, portName) {
		t.Fatalf("port %q leaked a midiOut endpoint - OUT_ONLY filter has a render pin", portName)
	}

	// Inject a few messages; driver-side FIFO accepts them even with no reader open.
	for i := 0; i < 5; i++ {
		p.Send(0xB0, 0x10, byte(i*20)) // CC 16 ramp
	}
	t.Logf("OK: %q visible as midiIn, absent from midiOut, 5 CCs injected", portName)
}
