//go:build windows && cgo

package mfenc

// livefps_windows_test.go - the receiver's fps hint is LIVE on the native path (proto v7 "fps" op):
// a running session changes its tick period in place - no reopen, no keyframe storm - bounded to
// [1 fps, negotiated rate]. Zero-copy arm: the producer changes a texel faster than any tick, so
// the AU rate IS the tick rate. Readback arm: the parent pushes 60 fps; a 30 fps cap halves it.
//
//	go test ./internal/mfenc -run TestLiveFPSCap -v

import (
	"testing"
	"time"
)

// window counts AUs and keyframes delivered over span.
func window(sink *auSink, span time.Duration) (aus, keys int, rate float64) {
	n0 := sink.len()
	t0 := time.Now()
	time.Sleep(span)
	got := sink.from(n0)
	for _, au := range got {
		if au.Keyframe {
			keys++
		}
	}
	return len(got), keys, float64(len(got)) / time.Since(t0).Seconds()
}

func TestLiveFPSCapZeroCopy(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	tex, err := newTestTexture(0, riskW, riskH, dxgiB8G8R8A8UNorm, false, bgraBands(riskW, riskH, [3]byte{255, 0, 0}, [3]byte{0, 0, 255}))
	if err != nil {
		t.Skipf("no shared texture on this box: %v", err)
	}
	defer tex.Close()
	s, err := riskSession(t, "rave-mate livefps zc", func() (uint64, uint32, int, int, bool) {
		return tex.Share, dxgiB8G8R8A8UNorm, riskW, riskH, true
	}, riskW, riskH)
	if err != nil {
		t.Fatalf("zero-copy open refused: %v", err)
	}
	defer s.Close()
	sink := collectAUs(s)
	stop := make(chan struct{})
	done := make(chan int, 1)
	go pokeProducer(tex, riskW, riskH, 120, stop, done) // a change every tick at any cap
	defer func() { close(stop); <-done }()
	time.Sleep(600 * time.Millisecond)
	const span = 2 * time.Second
	// GOP 60 frames (riskSession): at most one periodic IDR per 1 s at 60, per 3 s at 20.
	steps := []struct {
		name     string
		cap      int
		lo, hi   float64
		maxKeys  int
		wantTick float64
	}{
		{"uncapped", 0, 55, 62, 3, 60},
		{"cap20", 20, 17, 22, 2, 20},
		{"cap500-clamps-to-60", 500, 55, 62, 3, 60},
		{"cap1", 1, 0.5, 2, 2, 1},
		{"clear", 0, 55, 62, 3, 60},
	}
	for _, st := range steps {
		if !s.SetMaxFPS(st.cap) {
			t.Fatalf("%s: SetMaxFPS(%d) not sent (child protocol too old?)", st.name, st.cap)
		}
		time.Sleep(300 * time.Millisecond) // one old-period tick may still land
		aus, keys, rate := window(sink, span)
		ps := s.Stats()
		t.Logf("%s: cap=%d aus=%d (%.1f/s) keyframes=%d tickFPS=%.1f detect=%s", st.name, st.cap, aus, rate, keys, ps.TickFPS, ps.Detector)
		if rate < st.lo || rate > st.hi {
			t.Fatalf("%s: %.1f AU/s, want %.0f..%.0f", st.name, rate, st.lo, st.hi)
		}
		if keys > st.maxKeys {
			t.Fatalf("%s: %d keyframes in %v - a cap change must not force IDRs", st.name, keys, span)
		}
		if ps.TickFPS != st.wantTick {
			t.Fatalf("%s: TickFPS=%.1f, want %.1f", st.name, ps.TickFPS, st.wantTick)
		}
	}
	if s.Stats().Restarts != 0 {
		t.Fatal("the session restarted: the cap must be applied in place")
	}
}

func TestLiveFPSCapReadback(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	const w, h = 640, 360
	s, err := OpenProcSessionOpts(ProcOpts{LUID: 0, InW: w, InH: h, OutW: w, OutH: h, FPS: 60, Kbps: 4000, Gop: 60})
	if err != nil {
		t.Skipf("readback session: %v", err)
	}
	defer s.Close()
	sink := collectAUs(s)
	stop := make(chan struct{})
	go func() {
		frame := make([]byte, w*h*4)
		tk := time.NewTicker(time.Second / 60)
		defer tk.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tk.C:
				frame[(i*4)%len(frame)] = byte(i)
				_ = s.Encode(frame, time.Now().UnixNano())
			}
		}
	}()
	defer close(stop)
	time.Sleep(600 * time.Millisecond)
	_, _, base := window(sink, 2*time.Second)
	if !s.SetMaxFPS(30) {
		t.Fatal("SetMaxFPS(30) not sent")
	}
	time.Sleep(300 * time.Millisecond)
	_, keys, capped := window(sink, 2*time.Second)
	t.Logf("readback: pushed ~60/s: uncapped %.1f AU/s, cap 30 -> %.1f AU/s (keyframes %d)", base, capped, keys)
	if capped > base*0.65 || capped < base*0.35 {
		t.Fatalf("cap 30 on a %.1f AU/s readback session gave %.1f AU/s, want about half", base, capped)
	}
	if keys > 2 {
		t.Fatalf("%d keyframes after a cap change", keys)
	}
}
