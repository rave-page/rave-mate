//go:build spout

package videoshare

import (
	"fmt"
	"os"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// TestSpoutLiveProbe receives from an EXISTING named sender and reports pixel content.
// Diagnostic harness, opt-in: RAVE_SPOUT_PROBE=<sender name>. Scratch tool, not CI.
func TestSpoutLiveProbe(t *testing.T) {
	name := os.Getenv("RAVE_SPOUT_PROBE")
	if name == "" {
		t.Skip("set RAVE_SPOUT_PROBE=<sender name>")
	}
	if !spoutFindSender(name) {
		t.Fatalf("sender %q not registered", name)
	}
	rx, err := newFrameReceiver(logbus.New(256), name, RecvOptions{MaxFPS: 30})
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	defer rx.Close()

	frames, nonzero := 0, 0
	var maxPix byte
	done := time.After(10 * time.Second)
	for loop := true; loop; {
		select {
		case f, ok := <-rx.Frames():
			if !ok {
				loop = false
				break
			}
			frames++
			nz := 0
			for i := 0; i < len(f.Pix); i += 4097 { // sparse scan, RGBA-phase-breaking stride
				if f.Pix[i] != 0 {
					nz++
					if f.Pix[i] > maxPix {
						maxPix = f.Pix[i]
					}
				}
			}
			if nz > 0 {
				nonzero++
			}
			if frames <= 3 || frames%30 == 0 {
				fmt.Fprintf(os.Stderr, "[probe] frame=%d bytes=%d nzSamples=%d maxPix=%d rect=%v\n",
					frames, len(f.Pix), nz, maxPix, f.Rect)
			}
			PutPix(f.Pix)
		case <-done:
			loop = false
		}
	}
	fmt.Fprintf(os.Stderr, "[probe] TOTAL frames=%d framesWithContent=%d maxPix=%d\n", frames, nonzero, maxPix)
	if frames == 0 {
		t.Fatal("no frames received from live sender")
	}
}
