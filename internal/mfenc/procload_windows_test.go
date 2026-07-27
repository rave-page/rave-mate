//go:build windows && cgo

package mfenc

// Software-tier-under-contention gate. THE field failure this exists for: during a merge sweep with
// two live routes on the box, hardware MFT enumeration failed, the ladder correctly degraded to the
// software tier - and the software tier then produced ZERO AUs with err=nil and the child alive.
// A silent zero-output last rung is worse than a hard failure: the degrade ladder has nowhere left
// to go, and every counter says healthy.
//
// Reproduced deterministically by EXHAUSTING the hardware encoder (NVENC/AMF have a concurrent
// session cap), which is what a busy rig does to us, then taking the automatic ladder - NOT the
// RAVE_MATE_MFENC_SW=1 shortcut, because that skips rung 1 entirely and is therefore a different
// code path from the one that failed.

import (
	"testing"
	"time"
)

// TestProcSoftwareLadderUnderHardwareExhaustion opens hardware sessions until the silicon refuses,
// then opens one MORE session through the automatic ladder and requires REAL BITSTREAM from
// whatever tier it lands on.
func TestProcSoftwareLadderUnderHardwareExhaustion(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	resetChildren(t)

	// Saturate the hardware encoder. Consumer silicon caps concurrent sessions; keep them LIVE.
	var hold []*ProcSession
	defer func() {
		for _, s := range hold {
			s.Close()
		}
	}()
	for i := 0; i < 12; i++ {
		s, err := OpenProcSession(0, 1920, 1080, 1920, 1080, 60, 12000, 120)
		if err != nil {
			t.Logf("hardware exhausted after %d sessions: %v", len(hold), err)
			break
		}
		if s.IsSoftware() {
			t.Logf("session %d already landed on the software tier - hardware is exhausted", i+1)
			hold = append(hold, s)
			break
		}
		hold = append(hold, s)
		go func(s *ProcSession) { // keep the AU stream drained so nothing wedges on a full ring
			for range s.Output() {
			}
		}(s)
	}
	t.Logf("holding %d hardware sessions", len(hold))

	// The session under test takes the AUTOMATIC ladder (sw_policy=auto) - the exact path that
	// degraded in the field.
	const w, h = 640, 360
	s, err := OpenProcSession(0, w, h, w, h, 30, 2000, 60)
	if err != nil {
		t.Fatalf("open under hardware exhaustion failed outright (the ladder should have degraded): %v", err)
	}
	t.Logf("session under test: encoder=%q tier=%s drive=%s degrade=%q",
		s.Name(), tierName(s.IsSoftware()), s.Drive(), s.DegradeReason())

	aus, bytes, keys := 0, 0, 0
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
	var encErr error
	for f := 0; f < 40; f++ {
		for p := 0; p < len(buf); p += 4 {
			v := byte((p/4 + f*29) % 239)
			buf[p], buf[p+1], buf[p+3] = v, byte(255-int(v)), 255
		}
		if encErr = s.Encode(buf, int64(f)*33_333_333); encErr != nil {
			break
		}
	}
	time.Sleep(500 * time.Millisecond) // generous tail: a loaded box is slow, not broken
	st := s.Stats()
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}
	alive, tail, _ := childAliveAfter(0)
	t.Logf("under test: tier=%s aus=%d bytes=%d keyframes=%d busyDrops=%d encFails=%d err=%v alive=%v",
		tierName(st.Software), aus, bytes, keys, st.BusyDrops, st.EncFails, encErr, alive)
	if encErr != nil {
		t.Errorf("Encode failed under hardware exhaustion: %v (child stderr tail: %s)", encErr, tail)
	}
	// THE assertion: whatever tier we degraded to must produce output. An output-volume check is
	// the only thing that catches a rung that accepts frames and emits nothing while erroring not
	// at all - the failure mode that hit three components in one night.
	if aus == 0 {
		t.Fatalf("the degrade ladder landed on the %s tier and produced ZERO AUs with err=%v busyDrops=%d encFails=%d - "+
			"a silent zero-output last rung leaves the ladder nowhere to go; child stderr tail: %s",
			tierName(st.Software), encErr, st.BusyDrops, st.EncFails, tail)
	}
	if keys == 0 {
		t.Errorf("no keyframe from the %s tier (aus=%d)", tierName(st.Software), aus)
	}
	if perFrame := bytes / aus; perFrame < 200 {
		t.Errorf("%s tier produced %d B/frame - black or frozen, not a picture", tierName(st.Software), perFrame)
	}
}
