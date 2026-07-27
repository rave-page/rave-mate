//go:build windows && cgo

package mfenc

// Software MF encode tier gates. This is the LAST RUNG of the native engine and it has to work on
// a box with no usable hardware MFT (or with a poisoned adapter) - once shared-texture capture is
// the default path, the encoder child is load-bearing for ALL capture, so "no hardware encoder"
// must never mean "no video". Forcing the tier (RAVE_MATE_MFENC_SW=1) makes it verifiable on a
// machine that HAS working silicon, which is the only way it stays tested.

import (
	"testing"
	"time"
)

// encodeReal feeds `frames` frames of moving content and returns AU count + total bytes.
func encodeReal(t *testing.T, s *ProcSession, w, h, frames int) (aus, bytes, keys int) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			aus++
			bytes += len(au.Data)
			if au.Keyframe {
				keys++
			}
		}
		close(done)
	}()
	buf := make([]byte, w*h*4)
	for f := 0; f < frames; f++ {
		for p := 0; p < len(buf); p += 4 {
			v := byte((p/4 + f*23) % 241)
			buf[p], buf[p+1], buf[p+2], buf[p+3] = v, byte(255-int(v)), byte(f*7), 255
		}
		if err := s.Encode(buf, int64(f)*33_333_333); err != nil {
			t.Fatalf("Encode frame %d on the software tier: %v", f, err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}
	return aus, bytes, keys
}

// TestProcSoftwareTierEncodesRealContent forces the software MF H.264 encoder and requires a real
// bitstream out of it. Bytes-per-frame is asserted, not just AU count: black output would satisfy
// every counter, which is exactly how the field problem stayed invisible for 12 minutes.
func TestProcSoftwareTierEncodesRealContent(t *testing.T) {
	requireEncExe(t)
	t.Setenv("RAVE_MATE_MFENC_SW", "1") // force: hardware silicon on this box must be bypassed
	resetChildren(t)
	const w, h = 640, 360
	s, err := OpenProcSession(0, w, h, w, h, 30, 2000, 60)
	if err != nil {
		t.Skipf("software MF H.264 encoder unavailable on this Windows build: %v", err)
	}
	name, sw, drive := s.Name(), s.IsSoftware(), s.Drive()
	t.Logf("software tier: encoder=%q software=%v drive=%s degrade=%q", name, sw, drive, s.DegradeReason())
	// The child's open-stage trace names the drive mode it resolved and warns when a SYNC MFT also
	// exposes IMFMediaEventGenerator - i.e. when the old QI-based discriminator would have hung.
	_, tail, _ := childAliveAfter(0)
	t.Logf("child open trace:\n%s", tail)
	if !sw {
		s.Close()
		t.Fatalf("RAVE_MATE_MFENC_SW=1 bound %q but did not report the software tier - the gate is not testing what it claims", name)
	}
	aus, bytes, keys := encodeReal(t, s, w, h, 40)
	t.Logf("software tier: aus=%d bytes=%d keyframes=%d (%d B/frame)", aus, bytes, keys, bytes/max(aus, 1))
	if aus < 20 {
		t.Fatalf("software tier produced aus=%d, want >=20", aus)
	}
	if keys == 0 {
		t.Fatal("software tier produced no keyframe")
	}
	if perFrame := bytes / aus; perFrame < 300 {
		t.Fatalf("software tier produced %d B/frame - black or frozen, not a real picture", perFrame)
	}
}

// TestProcSoftwareTierTwoSessions: the last rung has to survive session multiplexing too,
// otherwise degrading a poisoned adapter onto it just moves the failure.
func TestProcSoftwareTierTwoSessions(t *testing.T) {
	requireEncExe(t)
	t.Setenv("RAVE_MATE_MFENC_SW", "1")
	resetChildren(t)
	if s, err := OpenProcSession(0, 320, 240, 320, 240, 30, 500, 30); err != nil {
		t.Skipf("software MF H.264 encoder unavailable: %v", err)
	} else {
		s.Close()
	}
	resetChildren(t)
	runs := runConcurrent(t, [][2]int{{640, 360}, {320, 240}}, 30)
	alive, tail, _ := childAliveAfter(0)
	for i, r := range runs {
		t.Logf("sw session %d: encoder=%q sw=%v drive=%s aus=%d bytes=%d err=%v", i+1, r.name, r.software, r.drive, r.aus, r.bytes, r.encErr)
		if r.encErr != nil {
			t.Errorf("software tier session %d of 2: %v", i+1, r.encErr)
		}
		if !r.software {
			t.Errorf("software tier session %d reported hardware (%q)", i+1, r.name)
		}
		if r.aus < 10 {
			t.Errorf("software tier session %d: aus=%d, want >=10", i+1, r.aus)
		}
	}
	if !alive {
		t.Errorf("child died with two software-tier sessions; stderr tail: %s", tail)
	}
}

// TestProcHardwareOnlyPolicyStillWorks pins the third policy value: RAVE_MATE_MFENC_SW=0 keeps the
// pre-tier behaviour, so a rig can prove a HARDWARE regression without the software rung masking it.
func TestProcHardwareOnlyPolicyStillWorks(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	t.Setenv("RAVE_MATE_MFENC_SW", "0")
	resetChildren(t)
	s, err := OpenProcSession(0, 640, 360, 640, 360, 30, 2000, 60)
	if err != nil {
		t.Fatalf("hardware-only policy failed to open on a box with hardware: %v", err)
	}
	if s.IsSoftware() {
		s.Close()
		t.Fatal("RAVE_MATE_MFENC_SW=0 still landed on the software tier")
	}
	t.Logf("hardware-only: encoder=%q drive=%s", s.Name(), s.Drive())
	aus, bytes, _ := encodeReal(t, s, 640, 360, 30)
	if aus < 15 {
		t.Fatalf("hardware-only: aus=%d bytes=%d", aus, bytes)
	}
}
