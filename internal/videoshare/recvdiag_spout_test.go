//go:build spout

package videoshare

// THE decisive diagnostic for the black-receive regression: the receive path reports success +
// frame-new and delivers all-zero frames. Two very different causes look identical in every counter:
//
//	A) ReceiveImage copies, but the source texture really is black  → the bug is upstream
//	B) ReceiveImage never touches the buffer at all                → the bug is HERE
//
// A CANARY separates them. Nothing in the repo could tell these apart before, which is exactly how
// this got filed once as "the read-back oracle does not work on this rig".
//
// OPT-IN (RAVE_SPOUT_RECVDIAG=1) and it deliberately calls into the MISALIGNED vtable window, so it
// can take the process down: with a live sender attached, our IsUpdated() lands on ReceiveImage with
// a garbage pointer argument and access-violates (0xc0000005, observed). That is further proof of the
// diagnosis, and the reason this is not part of the normal sweep. The permanent regression gate is
// TestRecvContentCarriesPixels, which asserts pixels through the SHIPPING path.
//
// Note for triage: the shipping code never made that particular call in this order, which is why the
// field symptom was a black picture rather than a crash.
//
//	RAVE_SPOUT_RECVDIAG=1 go test -tags spout ./internal/videoshare -run TestRecvDiag -v

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRecvDiag(t *testing.T) {
	if os.Getenv("RAVE_PROBE_ROLE") == "send" {
		t.Skip("publisher role")
	}
	if os.Getenv("RAVE_SPOUT_RECVDIAG") == "" {
		t.Skip("set RAVE_SPOUT_RECVDIAG=1 - this probe calls the misaligned vtable window and can AV")
	}
	startSoakPublisher(t)
	const w, h = 3840, 2160
	waitForSender(t, soakSender, w, h)

	samples, err := RecvDiag(soakSender, w, h, 12, 0xA5, func() { time.Sleep(60 * time.Millisecond) })
	if err != nil {
		t.Skipf("probe unavailable: %v", err)
	}
	size := w * h * 4
	var sawContent, sawZeroed bool
	for i, s := range samples {
		t.Logf("attempt %2d: recv=%v updated=%v frameNew=%v connected=%v sender=%dx%d fmt=%d cpu=%v gldx=%v frame=%d handle=%#x | canary=%d zeros=%d other=%d",
			i, s.RecvOK, s.Updated, s.FrameNew, s.Connected, s.SenderW, s.SenderH, s.SenderFmt,
			s.SenderCPU, s.SenderGLDX, s.SenderFrame, s.SenderHandle, s.Canary, s.Zeros, s.Other)
		if s.Other > size/100 {
			sawContent = true
		}
		if s.Canary == 0 && s.Zeros == size {
			sawZeroed = true
		}
	}
	switch {
	case sawContent:
		t.Log("VERDICT: content arrives - the CPU readback carries pixels")
	case sawZeroed:
		t.Log("VERDICT (B-): the buffer was OVERWRITTEN WITH ZEROS - the copy ran but read black")
	default:
		t.Log("VERDICT (B): the canary SURVIVED - ReceiveImage never wrote to the buffer")
	}
}

// startSoakPublisher runs the 4K gradient publisher in a second process (same-process send+receive
// deadlocks the driver's keyed mutex).
func startSoakPublisher(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=TestSpoutSoakPublisher", "-test.v")
	cmd.Env = append(os.Environ(), "RAVE_PROBE_ROLE=send")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn publisher: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
}

// waitForSender blocks until the named sender advertises a usable shared texture at w×h.
func waitForSender(t *testing.T, name string, w, h int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, _, sw, sh, ok := senderShare(name); ok && sw == w && sh == h {
			return
		}
		if time.Now().After(deadline) {
			t.Skipf("sender %q never registered a shared texture", name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
