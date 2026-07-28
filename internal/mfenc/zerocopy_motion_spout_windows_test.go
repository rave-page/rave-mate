//go:build windows && cgo && spout

package mfenc

// Live zero-copy MOTION gate: does the captured picture FOLLOW the sender, or is it frozen?
//
//	go test -tags spout ./internal/mfenc -run TestZeroCopyLiveFollowsSender -v
//
// This is the test the frozen-4K-picture bug needed and did not have. TestZeroCopyLiveSession
// publishes ONE static image and asserts capFrames >= 30, srcErrors == 0, capFPS ~60, capStaleMs
// fresh, aus >= 20 - every assertion a "frames are flowing" check, so it passes with a permanently
// frozen first frame, and its static source could not have exposed one anyway. The R1 oracle has
// the same blind spot: it fires on a CHANGED share handle or a capture clock that stopped, while
// the field had a stable handle and a healthy 59 fps.
//
// So: publish a sender whose content ALTERNATES, and require both phases to come out the far end.
// With the pre-fix code (VideoProcessorBlt queued, sender mutex released before the read was even
// submitted, no flush on the named-mutex path) every decoded frame is the same colour and this
// fails. Colour, not bytes/frame: a synthetic pattern compresses so well that a MOVING 720p probe
// measured 184 B/AU, barely above a static one, so byte volume cannot be the motion oracle here.

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
	zmSender    = "rave-mate zerocopy motion gate"
	zmW         = 1280
	zmH         = 720
	zmPhaseHold = 10 // frames per colour phase (~6 switches/sec at 60 fps)
)

// zmPhase builds a FULL-FRAME solid colour: phase 0 red, phase 1 green. Full-frame so a decode
// downscaled to 8x8 still classifies unambiguously, and red/green because NV12 chroma keeps them
// far apart after CSC.
func zmPhase(phase int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, zmW, zmH))
	r, g := byte(255), byte(0)
	if phase == 1 {
		r, g = 0, 255
	}
	for i := 0; i+3 < len(img.Pix); i += 4 {
		img.Pix[i+0], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, 0, 255
	}
	return img
}

// TestZeroCopyMotionPublisher is the child role: alternates the two phases until killed.
func TestZeroCopyMotionPublisher(t *testing.T) {
	if os.Getenv("RAVE_ZM_ROLE") != "send" {
		t.Skip("child role only (set by TestZeroCopyLiveFollowsSender)")
	}
	fs, err := videoshare.NewFrameSender(logbus.New(64), zmSender)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[zm-send] no sender:", err)
		return
	}
	defer fs.Close()
	frames := [2]*image.NRGBA{zmPhase(0), zmPhase(1)}
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(90 * time.Second) // backstop; the parent kills us
	n := 0
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			_ = fs.Send(frames[(n/zmPhaseHold)%2])
			n++
		}
	}
}

func TestZeroCopyLiveFollowsSender(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	ffmpeg, haveFF := mediatools.Resolve("ffmpeg")
	if !haveFF {
		t.Skip("ffmpeg not found - cannot decode the captured bitstream to judge motion")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pub := exec.Command(exe, "-test.run=TestZeroCopyMotionPublisher", "-test.v")
	pub.Env = append(os.Environ(), "RAVE_ZM_ROLE=send")
	pub.Stderr = os.Stderr
	if err := pub.Start(); err != nil {
		t.Fatalf("spawn publisher: %v", err)
	}
	defer func() { _ = pub.Process.Kill(); _, _ = pub.Process.Wait() }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		_, _, w, h, ok := videoshare.SenderShare(zmSender)
		if ok && w == zmW && h == zmH {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("publisher never registered a shared texture (no GPU / no SpoutLibrary.dll)")
		}
		time.Sleep(100 * time.Millisecond)
	}

	s, err := OpenProcSessionOpts(ProcOpts{
		LUID: 0, InW: zmW, InH: zmH, OutW: zmW, OutH: zmH, FPS: 60, Kbps: 8000, Gop: 30,
		Spout: &SpoutSource{Name: zmSender, Resolve: func() (uint64, uint32, int, int, bool) {
			return videoshare.SenderShare(zmSender)
		}},
	})
	if err != nil {
		t.Fatalf("zero-copy open: %v", err)
	}
	if !s.IsZeroCopy() {
		t.Fatal("session did not come up zero-copy, so it proves nothing")
	}
	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			aus = append(aus, au)
		}
		close(done)
	}()
	time.Sleep(2500 * time.Millisecond) // ~15 phase switches
	st := s.Stats()
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}

	// No capFPS here: the rate fields ride a sliding window that needs statsAfterRateWindow, and a
	// bare read prints 0.0 - a misleading number in the output of a test about frozen pictures.
	t.Logf("capture: capFlags=%#x (%s) capFrames=%d mtxTimeouts=%d srcErrors=%d aus=%d",
		st.CapFlags, capSyncName(st.CapFlags), st.CapFrames, st.MtxTimeouts, st.SrcErrors, len(aus))
	if st.CapFlags&0x1 == 0 {
		t.Fatalf("capFlags %#x: this run was not zero-copy, so it proves nothing", st.CapFlags)
	}
	if len(aus) < 20 {
		t.Fatalf("aus=%d, want >= 20 to judge motion", len(aus))
	}

	reds, greens := classifyPhases(t, ffmpeg, aus)
	t.Logf("decoded phases: red=%d green=%d of %d frames", reds, greens, reds+greens)
	// The whole point: BOTH phases must survive the capture. A frozen shared-texture read yields
	// one phase for the entire run while every counter above still reads healthy.
	if reds == 0 || greens == 0 {
		t.Fatalf("captured picture never changed (red=%d green=%d): the sender alternated ~15 times, "+
			"so the shared-texture read is frozen - check that the Blt is flushed before the sender's "+
			"mutex is released (capSync=%s)", reds, greens, capSyncName(st.CapFlags))
	}
}

// classifyPhases decodes every frame at 8x8 and counts how many are red-dominant vs green-dominant.
// Downscaled because a full-size decode of a 2.5 s 720p run is hundreds of MB for a 1-bit answer.
func classifyPhases(t *testing.T, ffmpeg string, aus []AU) (reds, greens int) {
	t.Helper()
	var in bytes.Buffer
	for _, au := range aus {
		in.Write(au.Data)
	}
	const n = 8
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
		"-vf", fmt.Sprintf("scale=%d:%d", n, n), "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	cmd.Stdin = &in
	out, err := cmd.Output()
	frame := n * n * 3
	if err != nil || len(out) < frame {
		t.Fatalf("captured bitstream does not decode (%v): got %d bytes", err, len(out))
	}
	for off := 0; off+frame <= len(out); off += frame {
		var sr, sg int
		for i := off; i < off+frame; i += 3 {
			sr += int(out[i])
			sg += int(out[i+1])
		}
		switch {
		case sr > sg*2:
			reds++
		case sg > sr*2:
			greens++
		}
	}
	return reds, greens
}

// capSyncName mirrors mediapipe's capSyncLabel for test output (bit1 keyed, bit2 named, bit3 unsync).
func capSyncName(flags uint32) string {
	switch {
	case flags&0x2 != 0:
		return "keyed"
	case flags&0x4 != 0:
		return "named"
	case flags&0x8 != 0:
		return "unsync"
	}
	return "none"
}
