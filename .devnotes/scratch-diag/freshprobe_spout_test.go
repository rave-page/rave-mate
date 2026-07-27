//go:build spout

package videoshare

import (
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// Scratch diagnostic: publish a KNOWN pattern under a name supplied by the caller (never
// reused - a recycled Spout name hands the next capture the DEAD texture, which reads as a
// blank frame with zero errors) and read it back over the CPU path.
// RAVE_FRESH_NAME=<unique name>, role via RAVE_FRESH_ROLE=send|recv.

func TestFreshPub(t *testing.T) {
	if os.Getenv("RAVE_FRESH_ROLE") != "send" {
		t.Skip("send role only")
	}
	name := os.Getenv("RAVE_FRESH_NAME")
	if name == "" {
		t.Skip("no name")
	}
	fs, err := newFrameSender(logbus.New(64), name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[fresh-send] no sender:", err)
		return
	}
	defer fs.Close()
	// Top half red, bottom half blue: proves content AND orientation AND channel order.
	w, h := 1920, 1080
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			if y < h/2 {
				img.Pix[i], img.Pix[i+2] = 255, 0
			} else {
				img.Pix[i], img.Pix[i+2] = 0, 255
			}
			img.Pix[i+1], img.Pix[i+3] = 0, 255
		}
	}
	fmt.Fprintf(os.Stderr, "[fresh-send] publishing %q %dx%d\n", name, w, h)
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(60 * time.Second)
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			_ = fs.Send(img)
		}
	}
}

func TestFreshRecv(t *testing.T) {
	if os.Getenv("RAVE_FRESH_ROLE") != "recv" {
		t.Skip("recv role only")
	}
	name := os.Getenv("RAVE_FRESH_NAME")
	if name == "" {
		t.Skip("no name")
	}
	deadline := time.Now().Add(15 * time.Second)
	for !spoutFindSender(name) {
		if time.Now().After(deadline) {
			t.Fatalf("sender %q never registered", name)
		}
		time.Sleep(100 * time.Millisecond)
	}
	rx, err := newFrameReceiver(logbus.New(256), name, RecvOptions{MaxFPS: 30})
	if err != nil {
		t.Fatalf("receiver: %v", err)
	}
	defer rx.Close()
	frames, content := 0, 0
	done := time.After(8 * time.Second)
	for loop := true; loop; {
		select {
		case f, ok := <-rx.Frames():
			if !ok {
				loop = false
				break
			}
			frames++
			w, h := f.Rect.Dx(), f.Rect.Dy()
			if w > 0 && h > 0 && len(f.Pix) >= w*h*4 {
				ti := (h/4*w + w/2) * 4
				bi := (h*3/4*w + w/2) * 4
				if f.Pix[ti] != 0 || f.Pix[bi+2] != 0 {
					content++
				}
				if frames <= 2 || frames%60 == 0 {
					fmt.Fprintf(os.Stderr, "[fresh-recv] f=%d %dx%d top(r=%d,b=%d) bottom(r=%d,b=%d)\n",
						frames, w, h, f.Pix[ti], f.Pix[ti+2], f.Pix[bi], f.Pix[bi+2])
				}
			}
			PutPix(f.Pix)
		case <-done:
			loop = false
		}
	}
	fmt.Fprintf(os.Stderr, "[fresh-recv] TOTAL frames=%d withContent=%d\n", frames, content)
	if frames == 0 {
		t.Fatal("no frames")
	}
	if content == 0 {
		t.Fatalf("all %d frames blank - CPU readback carries no pixels", frames)
	}
}
