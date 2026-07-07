//go:build spout

package videoshare

import (
	"image"
	"image/color"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

func solidFrame(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i+0] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	return img
}

func TestSpoutSenderRegisters(t *testing.T) {
	log := logbus.New(64)
	s, err := newSender(log)
	if err != nil {
		t.Fatalf("newSender: %v", err)
	}

	frame := solidFrame(360, 120, color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xFF})
	if err := s.Send("A", frame); err != nil {
		t.Fatalf("Send: %v", err)
	}

	name := SenderName("A") // "RaveMate Deck A"
	registered := false
	for i := 0; i < 40; i++ { // ~2s for the worker to build GL + publish
		time.Sleep(50 * time.Millisecond)
		if spoutFindSender(name) {
			registered = true
			break
		}
	}
	if !registered {
		// No GL context / registry → headless host: skip rather than fail.
		if spoutSenderCount() < 0 {
			t.Skip("spout: no GL/registry available on this host (headless) - skipping")
		}
		t.Fatalf("sender %q never registered (count=%d)", name, spoutSenderCount())
	}
	t.Logf("sender %q registered; total senders=%d", name, spoutSenderCount())

	s.Close()

	deregistered := false
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if !spoutFindSender(name) {
			deregistered = true
			break
		}
	}
	if !deregistered {
		t.Fatalf("sender %q still registered after Close", name)
	}
	t.Logf("sender %q deregistered after Close", name)
}
