//go:build spout

package videoshare

// recvcontent_spout_test.go - the CONTENT gate for the receive path.
//
// The black-frame P0 shipped because every gate was a METADATA gate: frames counted, geometry
// correct, no errors, "frame-new" true - and not one pixel delivered. These tests assert PIXELS:
// a known pattern goes in one process, the same pattern must come out of NewFrameReceiver in this
// one. Orientation and channel order are checked too, so a flipped or swizzled readback fails.
//
//	go test -tags spout ./internal/videoshare -run TestRecvContent -v
//
// Sender names are PER-ATTEMPT unique: Spout keeps a registry entry alive briefly after a publisher
// dies, and a reused name hands the next reader the DEAD texture - a blank frame with zero errors,
// which is the very failure mode being tested for.

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

const (
	rcW = 1280
	rcH = 720
)

// rcPattern: top half RED, bottom half BLUE, with a GREEN column on the left quarter. Catches a
// vertical flip, a horizontal mirror and an R/B swizzle in one frame.
func rcPattern() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, rcW, rcH))
	for y := 0; y < rcH; y++ {
		for x := 0; x < rcW; x++ {
			r, g, b := byte(255), byte(0), byte(0)
			if y >= rcH/2 {
				r, b = 0, 255
			}
			if x < rcW/4 {
				r, g, b = 0, 255, 0
			}
			p := img.Pix[y*img.Stride+x*4:]
			p[0], p[1], p[2], p[3] = r, g, b, 255
		}
	}
	return img
}

// TestRecvContentPublisher is the child role: publish rcPattern under RAVE_RC_SENDER.
func TestRecvContentPublisher(t *testing.T) {
	name := os.Getenv("RAVE_RC_SENDER")
	if os.Getenv("RAVE_RC_ROLE") != "send" || name == "" {
		t.Skip("child role only")
	}
	fs, err := NewFrameSender(logbus.New(32), name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[rc-send] no sender:", err)
		return
	}
	defer fs.Close()
	img := rcPattern()
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(90 * time.Second)
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			_ = fs.Send(img)
		}
	}
}

// TestRecvContentCarriesPixels is THE regression gate: the receive path must deliver the pattern.
func TestRecvContentCarriesPixels(t *testing.T) {
	if os.Getenv("RAVE_RC_ROLE") == "send" {
		t.Skip("publisher role")
	}
	name := fmt.Sprintf("rave-mate recvcontent %d", time.Now().UnixNano())
	startContentPublisher(t, name)
	waitForSender(t, name, rcW, rcH)

	rx, err := NewFrameReceiver(logbus.New(32), name)
	if err != nil {
		t.Skipf("no receiver: %v", err)
	}
	defer rx.Close()

	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no frame with CONTENT arrived within 20s")
		case img, ok := <-rx.Frames():
			if !ok {
				t.Fatal("receiver closed")
			}
			b := img.Bounds()
			if b.Dx() != rcW || b.Dy() != rcH {
				t.Fatalf("frame geometry %dx%d, want %dx%d", b.Dx(), b.Dy(), rcW, rcH)
			}
			at := func(x, y int) (int, int, int) {
				p := img.Pix[y*img.Stride+x*4:]
				return int(p[0]), int(p[1]), int(p[2])
			}
			// Sample inside each region (chroma-free here: this is the raw readback, not a codec).
			tr, tg, tb := at(rcW*5/8, rcH/4)   // top-right area: RED
			br, bg, bb := at(rcW*5/8, rcH*3/4) // bottom-right area: BLUE
			gr, gg, gb := at(rcW/8, rcH/2)     // left column: GREEN
			if tr == 0 && tg == 0 && tb == 0 && br == 0 && bb == 0 {
				continue // still the pre-content frame: keep waiting, the gate is the deadline
			}
			t.Logf("received: top(%d,%d,%d) bottom(%d,%d,%d) left(%d,%d,%d)", tr, tg, tb, br, bg, bb, gr, gg, gb)
			if tr < 200 || tb > 60 {
				t.Fatalf("top band = (%d,%d,%d), want RED - rows or channels are wrong", tr, tg, tb)
			}
			if bb < 200 || br > 60 {
				t.Fatalf("bottom band = (%d,%d,%d), want BLUE - rows or channels are wrong", br, bg, bb)
			}
			if gg < 200 || gr > 60 || gb > 60 {
				t.Fatalf("left column = (%d,%d,%d), want GREEN - columns are mirrored or swizzled", gr, gg, gb)
			}
			return
		}
	}
}

// TestRecvContentStaysNonZero: not one lucky frame - a run of frames must all carry content, so a
// path that delivers one good frame and then blanks (stale texture) still fails.
func TestRecvContentStaysNonZero(t *testing.T) {
	if os.Getenv("RAVE_RC_ROLE") == "send" {
		t.Skip("publisher role")
	}
	name := fmt.Sprintf("rave-mate recvsteady %d", time.Now().UnixNano())
	startContentPublisher(t, name)
	waitForSender(t, name, rcW, rcH)
	rx, err := NewFrameReceiver(logbus.New(32), name)
	if err != nil {
		t.Skipf("no receiver: %v", err)
	}
	defer rx.Close()

	const want = 30
	got, blank := 0, 0
	deadline := time.After(25 * time.Second)
	for got < want {
		select {
		case <-deadline:
			t.Fatalf("only %d/%d frames in 25s (%d blank)", got, want, blank)
		case img, ok := <-rx.Frames():
			if !ok {
				t.Fatalf("receiver closed after %d frames", got)
			}
			p := img.Pix[(rcH/4)*img.Stride+(rcW*5/8)*4:]
			if p[0] == 0 && p[1] == 0 && p[2] == 0 {
				blank++
				continue
			}
			got++
		}
	}
	if blank > want/2 {
		t.Fatalf("%d blank frames alongside %d good ones - the readback is intermittent", blank, got)
	}
	t.Logf("%d frames with content (%d blank while the sender warmed up)", got, blank)
}

func startContentPublisher(t *testing.T, name string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestRecvContentPublisher", "-test.v")
	cmd.Env = append(os.Environ(), "RAVE_RC_ROLE=send", "RAVE_RC_SENDER="+name)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn publisher: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
}
