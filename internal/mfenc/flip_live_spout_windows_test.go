//go:build windows && cgo && spout

package mfenc

// Live orientation gate for RAVE_SPOUT_FLIP (zigmedia increment 4).
//
// The send path used to apply EVERY flip on the CPU: a per-pixel 4-byte memcpy inside a nested loop,
// 8.3 M libc calls per 4K frame. The vertical component now rides Spout's own bInvert, which flips
// inside the GL/DX interop copy on the GPU. That is only a valid substitution if the two produce the
// SAME picture - and nothing in the repo ever verified what RAVE_SPOUT_FLIP actually does, so this
// test establishes it for all four modes.
//
//	go test -tags spout ./internal/mfenc -run TestFlipLive -v
//
// Method: publish a 4-quadrant probe from a CHILD process (the flip is read from the environment at
// init, so one mode per process), capture the sender's shared texture zero-copy, encode it, decode
// the bitstream with ffmpeg and check which quadrant each colour ended up in.

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/videoshare"
)

const (
	flipSender = "rave-mate flip gate"
	flipW      = 640
	flipH      = 360
)

// flipName derives a PER-MODE sender name. Spout keeps a registry entry alive briefly after a
// publisher dies, so reusing one name made every SECOND mode fail to create its sender (the modes
// alternated pass/skip) - the same trap the increment-3 measurements hit.
func flipName(mode string, attempt int) string {
	return fmt.Sprintf("%s %s %d", flipSender, mode, attempt)
}

// quadrant colours, chosen so 4:2:0 chroma + hardware quantisation cannot confuse them.
var (
	qRed   = [3]int{255, 0, 0}
	qGreen = [3]int{0, 255, 0}
	qBlue  = [3]int{0, 0, 255}
	qWhite = [3]int{255, 255, 255}
)

// flipPattern paints TL red, TR green, BL blue, BR white (top-row-first RGBA).
func flipPattern() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, flipW, flipH))
	for y := 0; y < flipH; y++ {
		for x := 0; x < flipW; x++ {
			var c [3]int
			switch {
			case y < flipH/2 && x < flipW/2:
				c = qRed
			case y < flipH/2:
				c = qGreen
			case x < flipW/2:
				c = qBlue
			default:
				c = qWhite
			}
			p := img.Pix[y*img.Stride+x*4:]
			p[0], p[1], p[2], p[3] = byte(c[0]), byte(c[1]), byte(c[2]), 255
		}
	}
	return img
}

// TestFlipLivePublisher is the child role: publish the probe with the flip from RAVE_SPOUT_FLIP.
func TestFlipLivePublisher(t *testing.T) {
	if os.Getenv("RAVE_FLIP_ROLE") != "send" {
		t.Skip("child role only (set by TestFlipLiveOrientation)")
	}
	fs, err := videoshare.NewFrameSender(logbus.New(64), os.Getenv("RAVE_FLIP_SENDER"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "[flip-send] no sender:", err)
		return
	}
	defer fs.Close()
	img := flipPattern()
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(60 * time.Second)
	reported := false
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			_ = fs.Send(img)
			if !reported {
				// Diagnostic: does OUR sender carry a DX11 shared texture at all? A mode that
				// publishes nothing usable must say so instead of timing the parent out.
				time.Sleep(200 * time.Millisecond)
				h, f, w, hh, ok := videoshare.SenderShare(os.Getenv("RAVE_FLIP_SENDER"))
				fmt.Fprintf(os.Stderr, "[flip-send] flip=%q share=%#x fmt=%d %dx%d ok=%v\n",
					os.Getenv("RAVE_SPOUT_FLIP"), h, f, w, hh, ok)
				reported = true
			}
		}
	}
}

// classify maps a sampled RGB to the nearest probe colour, or "?" when nothing is close.
func classify(r, g, b int) string {
	hi := func(v int) bool { return v > 140 }
	lo := func(v int) bool { return v < 110 }
	switch {
	case hi(r) && hi(g) && hi(b):
		return "white"
	case hi(r) && lo(g) && lo(b):
		return "red"
	case lo(r) && hi(g) && lo(b):
		return "green"
	case lo(r) && lo(g) && hi(b):
		return "blue"
	}
	return fmt.Sprintf("?(%d,%d,%d)", r, g, b)
}

func TestFlipLiveOrientation(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg needed to decode the captured bitstream")
	}
	// Expected quadrant contents per mode. none = as painted; v reverses rows; h mirrors columns.
	cases := []struct {
		mode           string
		tl, tr, bl, br string
	}{
		{"none", "red", "green", "blue", "white"},
		{"v", "blue", "white", "red", "green"},
		{"h", "green", "red", "white", "blue"},
		{"hv", "white", "blue", "green", "red"},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			// A BLANK capture is the stale-texture symptom (R1: OpenSharedResource succeeds on a
			// dead texture), not a wrong flip - retry with a fresh sender. A real flip bug shows a
			// WRONG pattern, never a blank one, so retrying cannot hide one.
			var tl, tr, bl, br string
			for attempt := 0; attempt < 4; attempt++ {
				tl, tr, bl, br = flipQuadrants(t, ffmpeg, c.mode, attempt)
				if !allBlank(tl, tr, bl, br) {
					break
				}
				t.Logf("attempt %d captured a blank frame (stale sender texture) - retrying", attempt)
			}
			t.Logf("RAVE_SPOUT_FLIP=%s → TL=%s TR=%s BL=%s BR=%s", c.mode, tl, tr, bl, br)
			if tl != c.tl || tr != c.tr || bl != c.bl || br != c.br {
				t.Fatalf("flip %q gave TL=%s TR=%s BL=%s BR=%s, want TL=%s TR=%s BL=%s BR=%s",
					c.mode, tl, tr, bl, br, c.tl, c.tr, c.bl, c.br)
			}
		})
	}
}

// flipQuadrants publishes with the given flip mode, captures zero-copy, decodes and classifies the
// four quadrants of the decoded frame.
func flipQuadrants(t *testing.T, ffmpeg, mode string, attempt int) (tl, tr, bl, br string) {
	t.Helper()
	name := flipName(mode, attempt)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pub := exec.Command(exe, "-test.run=TestFlipLivePublisher", "-test.v")
	pub.Env = append(os.Environ(), "RAVE_FLIP_ROLE=send", "RAVE_SPOUT_FLIP="+mode,
		"RAVE_FLIP_SENDER="+name)
	pub.Stderr = os.Stderr
	if err := pub.Start(); err != nil {
		t.Fatalf("spawn publisher: %v", err)
	}
	defer func() { _ = pub.Process.Kill(); _, _ = pub.Process.Wait() }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		_, _, w, h, ok := videoshare.SenderShare(name)
		if ok && w == flipW && h == flipH {
			break
		}
		if time.Now().After(deadline) {
			t.Skipf("publisher never registered a shared texture for %q", name)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The registry entry appears on the publisher's FIRST SendImage, but a single GL/DX interop
	// write is not yet visible to a foreign D3D11 device - it is not flushed until further GL work
	// is submitted. Opening the capture the instant the name resolves therefore races the flush, and
	// under GPU load the first captured frame is blank. Let the publisher get several frames out.
	time.Sleep(400 * time.Millisecond)
	s, err := OpenProcSessionOpts(ProcOpts{
		LUID: 0, InW: flipW, InH: flipH, OutW: flipW, OutH: flipH, FPS: 30, Kbps: 6000, Gop: 30,
		Spout: &SpoutSource{Name: name, Resolve: func() (uint64, uint32, int, int, bool) {
			return videoshare.SenderShare(name)
		}},
	})
	if err != nil {
		t.Skipf("zero-copy open: %v", err)
	}
	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			aus = append(aus, AU{Data: append([]byte(nil), au.Data...), Keyframe: au.Keyframe})
		}
		close(done)
	}()
	time.Sleep(900 * time.Millisecond)
	s.Close()
	<-done
	if len(aus) < 3 {
		t.Fatalf("captured %d AUs", len(aus))
	}
	var in bytes.Buffer
	for _, au := range aus {
		in.Write(au.Data)
	}
	// Decode the WHOLE capture and sample the LAST frame, not the first. The first frame is the one
	// captured immediately after the session opened, i.e. the one that can still predate the
	// publisher's flush; a frame from the end of a 900 ms capture cannot. This is the same discipline
	// the R1 gate needed: never sample at the boundary you are racing.
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
		"-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	cmd.Stdin = &in
	out, err := cmd.Output()
	frame := flipW * flipH * 3
	if err != nil || len(out) < frame {
		t.Fatalf("decode failed (%v), %d bytes want >= %d", err, len(out), frame)
	}
	last := out[(len(out)/frame-1)*frame:]
	at := func(x, y int) string {
		i := (y*flipW + x) * 3
		return classify(int(last[i]), int(last[i+1]), int(last[i+2]))
	}
	// Sample well inside each quadrant: chroma subsampling smears the seams.
	qx, qy := flipW/4, flipH/4
	return at(qx, qy), at(flipW-qx, qy), at(qx, flipH-qy), at(flipW-qx, flipH-qy)
}

// allBlank reports whether every quadrant classified as pure black (the stale-texture symptom).
func allBlank(q ...string) bool {
	for _, v := range q {
		if v != "?(0,0,0)" {
			return false
		}
	}
	return true
}
