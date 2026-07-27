//go:build windows && cgo && spout

package mfenc

// Live zero-copy gate: a REAL Spout sender in a REAL second process, captured by the encoder
// child straight out of its DX11 shared texture. Two processes on purpose - a same-process Spout
// send+receive deadlocks in the driver's keyed mutex (the same reason mediaroute refuses same-PC
// routes and the reason the 4K soak harness re-execs itself).
//
//	go test -tags spout ./internal/mfenc -run TestZeroCopyLive -v
//
// What this proves that no mock can: OpenSharedResource on a foreign process's texture,
// CreateVideoProcessorInputView over it, the access-mutex handshake, and that AUs come out with
// the source's pixels in them. Orientation + colour are checked by DECODING the bitstream when
// ffmpeg is present (risk R5: the readback path flips on the CPU, so a zero-copy path that
// silently inverted rows would look "fine" in every counter).

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

const (
	zcSender = "rave-mate zerocopy gate"
	zcW      = 1280
	zcH      = 720
)

// zcPattern is the probe image: top half RED, bottom half BLUE, so a vertical flip and a
// red/blue swizzle are both visible in ONE decoded frame.
func zcPattern() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, zcW, zcH))
	for y := 0; y < zcH; y++ {
		r, b := byte(255), byte(0)
		if y >= zcH/2 {
			r, b = 0, 255
		}
		for x := 0; x < zcW; x++ {
			p := img.Pix[y*img.Stride+x*4:]
			p[0], p[1], p[2], p[3] = r, 0, b, 255
		}
	}
	return img
}

// TestZeroCopyLivePublisher is the child role: publishes the probe pattern until killed.
func TestZeroCopyLivePublisher(t *testing.T) {
	if os.Getenv("RAVE_ZC_ROLE") != "send" {
		t.Skip("child role only (set by TestZeroCopyLiveSession)")
	}
	fs, err := videoshare.NewFrameSender(logbus.New(64), zcSender)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[zc-send] no sender:", err)
		return
	}
	defer fs.Close()
	img := zcPattern()
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(90 * time.Second) // backstop; the parent kills us
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			_ = fs.Send(img)
		}
	}
}

func TestZeroCopyLiveSession(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pub := exec.Command(exe, "-test.run=TestZeroCopyLivePublisher", "-test.v")
	pub.Env = append(os.Environ(), "RAVE_ZC_ROLE=send")
	pub.Stderr = os.Stderr
	if err := pub.Start(); err != nil {
		t.Fatalf("spawn publisher: %v", err)
	}
	defer func() { _ = pub.Process.Kill(); _, _ = pub.Process.Wait() }()

	// Wait for the registry to carry a usable shared texture (handle + format + dims).
	var handle uint64
	var dxgi uint32
	deadline := time.Now().Add(20 * time.Second)
	for {
		h, f, w, hh, ok := videoshare.SenderShare(zcSender)
		if ok && w == zcW && hh == zcH {
			handle, dxgi = h, f
			break
		}
		if time.Now().After(deadline) {
			t.Skip("publisher never registered a shared texture (no GPU / no SpoutLibrary.dll)")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("sender %q: handle=%#x dxgiFormat=%d %dx%d", zcSender, handle, dxgi, zcW, zcH)

	s, err := OpenProcSessionOpts(ProcOpts{
		LUID: 0, InW: zcW, InH: zcH, OutW: zcW, OutH: zcH, FPS: 60, Kbps: 8000, Gop: 60,
		Spout: &SpoutSource{Name: zcSender, Resolve: func() (uint64, uint32, int, int, bool) {
			return videoshare.SenderShare(zcSender)
		}},
	})
	if err != nil {
		t.Fatalf("zero-copy open: %v", err)
	}
	if !s.IsZeroCopy() {
		t.Fatal("session did not come up zero-copy")
	}
	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			aus = append(aus, au)
		}
		close(done)
	}()
	// The CHILD paces itself - we submit nothing. The rate fields ride ratewin's sliding window, so
	// they need >= ratewin.MinSpan of OBSERVATION, not just two reads (statsAfterRateWindow).
	time.Sleep(500 * time.Millisecond)
	st := statsAfterRateWindow(s)
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}

	t.Logf("zero-copy live: aus=%d capFrames=%d capFPS=%.1f skips=%d mtxTimeouts=%d srcErrors=%d "+
		"staleMs=%.0f encBusy=%.2fms capFmt=%d capFlags=%#x cpu=%.1f%%",
		len(aus), st.CapFrames, st.CapFPS, st.CapSkips, st.MtxTimeouts, st.SrcErrors,
		st.CapStaleMs, st.EncBusyMs, st.CapFmt, st.CapFlags, st.ChildCPUPct)

	if st.CapFlags&1 == 0 {
		t.Fatalf("capFlags %#x: zero-copy bit not set by the child", st.CapFlags)
	}
	if st.CapFrames < 30 {
		t.Fatalf("capFrames=%d over 1.5 s at 60 fps, want >= 30", st.CapFrames)
	}
	if st.SrcErrors != 0 {
		t.Fatalf("srcErrors=%d on a healthy sender", st.SrcErrors)
	}
	if st.CapFPS < 30 {
		t.Fatalf("capFPS=%.1f, want a paced ~60 fps capture", st.CapFPS)
	}
	if st.EncBusyMs <= 0 || st.EncBusyMs > 25 {
		t.Fatalf("encBusyMs=%.2f, want a plausible per-frame capture+encode cost", st.EncBusyMs)
	}
	if st.CapStaleMs > 200 {
		t.Fatalf("capStaleMs=%.0f on a live sender: the freshness oracle would false-positive", st.CapStaleMs)
	}
	if len(aus) < 20 {
		s.child.mu.Lock()
		tail := string(s.child.stderrTail)
		s.child.mu.Unlock()
		t.Fatalf("aus=%d want >= 20; child stderr: %s", len(aus), tail)
	}
	if !aus[0].Keyframe {
		t.Fatal("first AU is not a keyframe")
	}
	// PTS must be on the parent's wall clock (the receiver's jitter buffer + transit telemetry
	// compare it against the sender's clock, so a raw QPC value would be a silent regression).
	if drift := time.Since(time.Unix(0, aus[0].PTSNs)); drift < 0 || drift > time.Minute {
		t.Fatalf("first AU pts %d is not wall-clock ns (drift %v)", aus[0].PTSNs, drift)
	}
	// Orientation + colour: decode and check the probe pattern survived the shared-texture read.
	assertProbePattern(t, aus)
}

// assertProbePattern is the orientation + swizzle check for the zero-copy path (R5): top half red,
// bottom half blue. The decode + band sampling itself lives in assertProbeBands
// (zerocopy_risks_windows_test.go), which the risk gates share - one content instrument, not two.
func assertProbePattern(t *testing.T, aus []AU) {
	t.Helper()
	assertProbeBands(t, aus, zcW, zcH, [3]byte{255, 0, 0}, [3]byte{0, 0, 255})
}
