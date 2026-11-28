//go:build windows && cgo

package mfenc

// framegate_windows_test.go - new-frame gating on the zero-copy path (encoder child spoutLoop).
//
// Instrument: a shared texture WE own (newTestTexture, no Spout, no SpoutLibrary.dll) plus a fake
// PRODUCER: the named semaphore "<sender>_Count_Semaphore" driven exactly as SpoutFrameCount.cpp's
// sender does it (created with count 1, Wait(0) + Release(2) per frame). The child reads only that
// count, so the fake is the real signal path end to end.
//
//	go test ./internal/mfenc -run TestZeroCopyFrameGate -v

import (
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	k32gate     = windows.NewLazySystemDLL("kernel32.dll")
	procCreateS = k32gate.NewProc("CreateSemaphoreW")
	procRelS    = k32gate.NewProc("ReleaseSemaphore")
)

// fakeProducer owns a Spout-shaped frame counter for sender name.
type fakeProducer struct{ h windows.Handle }

func newFakeProducer(t *testing.T, name string) *fakeProducer {
	t.Helper()
	p, _ := windows.UTF16PtrFromString(name + "_Count_Semaphore")
	h, _, err := procCreateS.Call(0, 1, 0x7fffffff, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		t.Fatalf("CreateSemaphoreW: %v", err)
	}
	fp := &fakeProducer{h: windows.Handle(h)}
	t.Cleanup(func() { _ = windows.CloseHandle(fp.h) })
	return fp
}

func (f *fakeProducer) frame() {
	_, _ = windows.WaitForSingleObject(f.h, 0)
	_, _, _ = procRelS.Call(uintptr(f.h), 2, 0)
}

// run drives frames at fps until stop closes (fps 0 = static: never advances).
func (f *fakeProducer) run(fps int, stop <-chan struct{}) {
	if fps <= 0 {
		return
	}
	tk := time.NewTicker(time.Second / time.Duration(fps))
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			f.frame()
		}
	}
}

// gateArm opens a 60 fps zero-copy session over tex for sender name, runs the producer at fps for
// span, and returns the final stats + AU count.
func gateArm(t *testing.T, tex *testTexture, name string, producer *fakeProducer, fps int, span time.Duration) (ProcStats, int) {
	t.Helper()
	s, err := riskSession(t, name, func() (uint64, uint32, int, int, bool) {
		return tex.Share, dxgiB8G8R8A8UNorm, riskW, riskH, true
	}, riskW, riskH)
	if err != nil {
		t.Fatalf("zero-copy open refused: %v", err)
	}
	if !s.IsZeroCopy() {
		s.Close()
		t.Fatal("session did not come up zero-copy")
	}
	sink := collectAUs(s)
	stop := make(chan struct{})
	if producer != nil {
		go producer.run(fps, stop)
	}
	time.Sleep(300 * time.Millisecond) // open + first keyframe settle
	base := sink.len()
	_ = s.Stats()
	time.Sleep(span)
	st := s.Stats()
	n := sink.len() - base
	close(stop)
	s.Close()
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}
	return st, n
}

// TestZeroCopyFrameGate: a sender whose counter never advances produces nothing beyond the 2 fps
// keep-alive; a 30 fps producer on a 60 fps route yields ~30 encodes/s (not 60); a sender with no
// counter keeps the blind 60 fps pacing (fallback).
func TestZeroCopyFrameGate(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	tex, err := newTestTexture(0, riskW, riskH, dxgiB8G8R8A8UNorm, false, bgraBands(riskW, riskH, [3]byte{255, 0, 0}, [3]byte{0, 0, 255}))
	if err != nil {
		t.Skipf("no shared texture on this box: %v", err)
	}
	defer tex.Close()
	const span = 3 * time.Second
	secs := span.Seconds()

	t.Run("static", func(t *testing.T) {
		const name = "rave-mate framegate static"
		p := newFakeProducer(t, name)
		st, aus := gateArm(t, tex, name, p, 0, span)
		t.Logf("static: aus=%d (%.1f/s) capFPS=%.1f srcFPS=%.1f dupSkips=%d capFlags=%#x", aus, float64(aus)/secs,
			st.CapFPS, st.SrcFPS, st.DupSkips, st.CapFlags)
		if !st.FrameCounter {
			t.Fatalf("capFlags=%#x: frame counter not detected - gating never engaged", st.CapFlags)
		}
		// keep-alive 500 ms: <= 2/s, +2 for window edges.
		if max := int(secs*2) + 2; aus > max || aus < 2 {
			t.Fatalf("static sender produced %d AUs in %.0f s, want keep-alive only (2..%d)", aus, secs, max)
		}
		if st.DupSkips < uint64(secs*40) {
			t.Fatalf("dupSkips=%d, want ~%d tick slots skipped", st.DupSkips, int(secs*58))
		}
	})

	// A newly joined receiver asks for an IDR: on a static source it must not wait for the keep-alive.
	t.Run("idr-on-static", func(t *testing.T) {
		const name = "rave-mate framegate idr"
		newFakeProducer(t, name)
		s, err := riskSession(t, name, func() (uint64, uint32, int, int, bool) {
			return tex.Share, dxgiB8G8R8A8UNorm, riskW, riskH, true
		}, riskW, riskH)
		if err != nil {
			t.Fatalf("zero-copy open refused: %v", err)
		}
		defer s.Close()
		sink := collectAUs(s)
		time.Sleep(700 * time.Millisecond)
		for i := 0; i < 3; i++ {
			// Align just after a keep-alive AU, so the next keep-alive is ~500 ms away.
			n0 := sink.len()
			deadline := time.Now().Add(2 * time.Second)
			for sink.len() == n0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			n1 := sink.len()
			t0 := time.Now()
			s.ForceKeyframe()
			var got time.Duration
			for time.Since(t0) < 2*time.Second && got == 0 {
				for _, au := range sink.from(n1) {
					if au.Keyframe {
						got = time.Since(t0)
						break
					}
				}
				time.Sleep(time.Millisecond)
			}
			t.Logf("idr %d: keyframe after %v", i, got)
			if got == 0 || got > 200*time.Millisecond {
				t.Fatalf("IDR on a static source took %v, want well under the 500 ms keep-alive", got)
			}
		}
	})

	t.Run("30fps", func(t *testing.T) {
		const name = "rave-mate framegate 30fps"
		p := newFakeProducer(t, name)
		st, aus := gateArm(t, tex, name, p, 30, span)
		rate := float64(aus) / secs
		t.Logf("30fps producer on a 60 fps route: aus=%d (%.1f/s) capFPS=%.1f srcFPS=%.1f dupSkips=%d", aus, rate,
			st.CapFPS, st.SrcFPS, st.DupSkips)
		if rate < 25 || rate > 35 {
			t.Fatalf("%.1f encodes/s for a 30 fps producer, want ~30 (blind pacing would give ~60)", rate)
		}
		if st.SrcFPS < 25 || st.SrcFPS > 35 {
			t.Fatalf("srcFPS=%.1f, want the producer's ~30", st.SrcFPS)
		}
		if st.DupSkips == 0 {
			t.Fatal("dupSkips=0 at 30-on-60: duplicate ticks were not skipped")
		}
	})

	t.Run("no-counter-fallback", func(t *testing.T) {
		st, aus := gateArm(t, tex, "rave-mate framegate blind", nil, 0, span)
		rate := float64(aus) / secs
		t.Logf("no counter: aus=%d (%.1f/s) capFPS=%.1f dupSkips=%d capFlags=%#x", aus, rate, st.CapFPS, st.DupSkips, st.CapFlags)
		if st.FrameCounter || st.DupSkips != 0 {
			t.Fatalf("gating engaged without a counter (flags=%#x dupSkips=%d)", st.CapFlags, st.DupSkips)
		}
		if rate < 45 {
			t.Fatalf("%.1f encodes/s without a counter, want blind ~60 (fallback must keep the old pacing)", rate)
		}
	})
}
