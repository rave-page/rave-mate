//go:build windows && cgo

package mfenc

// framecompare_windows_test.go - counter-less new-frame detection (encoder child cap.FrameCmp): the
// TestZeroCopyFrameGate pattern with NO "<sender>_Count_Semaphore" anywhere, i.e. a Spout sender with
// frame counting off (the default). The producer is a texture WE own, changed ONE texel per frame via
// mf_testtex_poke - the smallest change the detector must never miss.
//
//	go test ./internal/mfenc -run TestZeroCopyFrameCompare -v

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pokeProducer changes one texel of tex per frame at fps until stop closes (a different texel and
// value each frame, never repeated across arms, so every frame differs from every older one). Paced
// by spinning on the clock, not a Ticker: a 120 Hz Ticker delivered ~52 Hz here, which is not "a
// change every tick". Returns the number of frames produced on done.
func pokeProducer(tex *testTexture, w, h, fps int, stop <-chan struct{}, done chan<- int) {
	gap := time.Second / time.Duration(fps)
	next := time.Now()
	n := 0
	for {
		select {
		case <-stop:
			done <- n
			return
		default:
		}
		if now := time.Now(); now.Before(next) {
			runtime.Gosched()
			continue
		}
		next = next.Add(gap)
		n++
		// Sequence is process-wide: a per-producer counter re-wrote the texels+values the previous
		// arm had left in place - genuinely identical frames, which the detector (correctly) skipped.
		i := int(pokeSeq.Add(1))
		tex.Poke((i*37)%w, (i*101)%h, 0xFF000000|uint32(i*2654435761)&0xFFFFFF)
	}
}

var pokeSeq atomic.Int64

// blindPacing restarts the encoder children with the GPU frame compare off, for gates that need every
// tick of a STATIC test texture captured (capture mechanics under continuous load, not detection).
func blindPacing(t *testing.T) {
	t.Helper()
	t.Setenv("RAVE_MATE_MFENC_FRAMECMP", "0")
	resetChildren(t)
}

// cmpArm opens a 60 fps zero-copy session over tex (no counter), runs a one-texel producer at fps
// (0 = static) for span, and returns the final stats + AU count in the window.
func cmpArm(t *testing.T, tex *testTexture, name string, w, h, fps int, span time.Duration) (ProcStats, int) {
	t.Helper()
	s, err := riskSession(t, name, func() (uint64, uint32, int, int, bool) {
		return tex.Share, dxgiB8G8R8A8UNorm, w, h, true
	}, w, h)
	if err != nil {
		t.Fatalf("zero-copy open refused: %v", err)
	}
	if !s.IsZeroCopy() {
		s.Close()
		t.Fatal("session did not come up zero-copy")
	}
	sink := collectAUs(s)
	stop := make(chan struct{})
	done := make(chan int, 1)
	produced := -1
	halt := sync.OnceFunc(func() {
		close(stop)
		if fps > 0 {
			produced = <-done
		}
	})
	defer halt() // a Fatal below must not leave the producer poking a texture the caller then frees
	if fps > 0 {
		go pokeProducer(tex, w, h, fps, stop, done)
	}
	time.Sleep(500 * time.Millisecond) // open + first keyframe + compare self-test settle
	base := sink.len()
	_ = s.Stats()
	t0 := time.Now()
	time.Sleep(span)
	st := s.Stats()
	n := sink.len() - base
	halt()
	if fps > 0 {
		t.Logf("producer: %.1f frames/s over the run (one texel each)", float64(produced)/(time.Since(t0)+500*time.Millisecond).Seconds())
	}
	s.Close()
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}
	return st, n
}

// TestZeroCopyFrameCompare: with no frame counter, a static texture yields only the 2 fps keep-alive;
// one texel changing at 30 fps on a 60 fps route yields ~30 encodes/s and srcFPS ~30; a texel changing
// faster than the tick yields ~60/s; an IDR on a static source is served promptly.
func TestZeroCopyFrameCompare(t *testing.T) {
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
		st, aus := cmpArm(t, tex, "rave-mate framecmp static", riskW, riskH, 0, span)
		t.Logf("static: aus=%d (%.1f/s) capFPS=%.1f srcFPS=%.1f dupSkips=%d detect=%s cmpLat=%.2fms cmpTimeouts=%d capFlags=%#x",
			aus, float64(aus)/secs, st.CapFPS, st.SrcFPS, st.DupSkips, st.Detector, st.CmpLatMs, st.CmpTimeouts, st.CapFlags)
		if st.Detector != DetectGPUCompare || st.FrameCounter {
			t.Fatalf("detect=%s counter=%v (capFlags=%#x): want gpu-compare with no counter", st.Detector, st.FrameCounter, st.CapFlags)
		}
		if max := int(secs*2) + 2; aus > max || aus < 2 {
			t.Fatalf("static sender produced %d AUs in %.0f s, want keep-alive only (2..%d)", aus, secs, max)
		}
		if st.DupSkips < uint64(secs*40) {
			t.Fatalf("dupSkips=%d, want ~%d duplicate ticks", st.DupSkips, int(secs*58))
		}
	})

	t.Run("one-texel-30fps", func(t *testing.T) {
		st, aus := cmpArm(t, tex, "rave-mate framecmp 30fps", riskW, riskH, 30, span)
		rate := float64(aus) / secs
		t.Logf("1 texel @30 fps on a 60 fps route: aus=%d (%.1f/s) capFPS=%.1f srcFPS=%.1f dupSkips=%d cmpLat=%.2fms cmpTimeouts=%d",
			aus, rate, st.CapFPS, st.SrcFPS, st.DupSkips, st.CmpLatMs, st.CmpTimeouts)
		if rate < 25 || rate > 35 {
			t.Fatalf("%.1f encodes/s for a one-texel 30 fps producer, want ~30 (a missed change reads ~2, blind ~60)", rate)
		}
		if st.SrcFPS < 25 || st.SrcFPS > 35 {
			t.Fatalf("srcFPS=%.1f, want the producer's ~30", st.SrcFPS)
		}
		if st.DupSkips == 0 {
			t.Fatal("dupSkips=0 at 30-on-60: duplicate ticks were not skipped")
		}
	})

	// Same detector behind the texture's own IDXGIKeyedMutex (the copy is released by ReleaseSync,
	// whose implicit flush replaces the named-mutex path's explicit one).
	t.Run("keyed-one-texel-30fps", func(t *testing.T) {
		ktex, err := newTestTexture(0, riskW, riskH, dxgiB8G8R8A8UNorm, true, bgraBands(riskW, riskH, [3]byte{0, 255, 0}, [3]byte{0, 0, 255}))
		if err != nil {
			t.Fatalf("keyed texture: %v", err)
		}
		defer ktex.Close()
		st, aus := cmpArm(t, ktex, "rave-mate framecmp keyed", riskW, riskH, 30, span)
		rate := float64(aus) / secs
		t.Logf("keyed, 1 texel @30 fps: aus=%d (%.1f/s) srcFPS=%.1f dupSkips=%d detect=%s capFlags=%#x mtxTimeouts=%d",
			aus, rate, st.SrcFPS, st.DupSkips, st.Detector, st.CapFlags, st.MtxTimeouts)
		if st.CapFlags&0x2 == 0 || st.Detector != DetectGPUCompare {
			t.Fatalf("capFlags=%#x detect=%s: want the keyed branch with gpu-compare", st.CapFlags, st.Detector)
		}
		if rate < 25 || rate > 35 || st.SrcFPS < 25 || st.SrcFPS > 35 {
			t.Fatalf("%.1f encodes/s, srcFPS %.1f for a keyed one-texel 30 fps producer, want ~30", rate, st.SrcFPS)
		}
	})

	t.Run("every-tick", func(t *testing.T) {
		st, aus := cmpArm(t, tex, "rave-mate framecmp 120", riskW, riskH, 120, span)
		rate := float64(aus) / secs
		t.Logf("1 texel @120 fps on a 60 fps route: aus=%d (%.1f/s) capFPS=%.1f srcFPS=%.1f dupSkips=%d cmpLat=%.2fms capSkips=%d busyDrops=%d mtxTimeouts=%d cmpTimeouts=%d capFrames=%d",
			aus, rate, st.CapFPS, st.SrcFPS, st.DupSkips, st.CmpLatMs, st.CapSkips, st.BusyDrops, st.MtxTimeouts, st.CmpTimeouts, st.CapFrames)
		if rate < 54 {
			t.Fatalf("%.1f encodes/s with a change every tick, want ~60", rate)
		}
	})

	t.Run("idr-on-static", func(t *testing.T) {
		s, err := riskSession(t, "rave-mate framecmp idr", func() (uint64, uint32, int, int, bool) {
			return tex.Share, dxgiB8G8R8A8UNorm, riskW, riskH, true
		}, riskW, riskH)
		if err != nil {
			t.Fatalf("zero-copy open refused: %v", err)
		}
		defer s.Close()
		sink := collectAUs(s)
		time.Sleep(700 * time.Millisecond)
		for i := 0; i < 3; i++ {
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
			t.Logf("idr %d: keyframe after %v (detect=%s)", i, got, s.Stats().Detector)
			if got == 0 || got > 200*time.Millisecond {
				t.Fatalf("IDR on a static source took %v, want well under the 500 ms keep-alive", got)
			}
		}
	})
}

// TestZeroCopyFrameCompare4KCost: what the compare costs and saves at 3840x2160. Arm A = compare on,
// static source (every tick: copy + compare, encode only the keep-alive). Arm B = compare disabled
// (RAVE_MATE_MFENC_FRAMECMP=0), same static source: blind 60 fps copy + CSC + encode. Same child
// binary, same texture. The GPU time of the compare pass itself is gated in Zig (cap.zig test).
func TestZeroCopyFrameCompare4KCost(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	const w, h = 3840, 2160
	tex, err := newTestTexture(0, w, h, dxgiB8G8R8A8UNorm, false, bgraBands(w, h, [3]byte{255, 0, 0}, [3]byte{0, 0, 255}))
	if err != nil {
		t.Skipf("no 4K shared texture on this box: %v", err)
	}
	defer tex.Close()
	const span = 4 * time.Second
	secs := span.Seconds()

	resetChildren(t)
	cmp, cmpAUs := cmpArm(t, tex, "rave-mate framecmp 4k cmp", w, h, 0, span)
	t.Logf("4K compare ON, static: aus=%d (%.1f/s) dupSkips=%d detect=%s cmpLat=%.2fms cmpTimeouts=%d childCPU=%.1f%% encBusy=%.2fms/encode",
		cmpAUs, float64(cmpAUs)/secs, cmp.DupSkips, cmp.Detector, cmp.CmpLatMs, cmp.CmpTimeouts, cmp.ChildCPUPct, cmp.EncBusyMs)

	t.Setenv("RAVE_MATE_MFENC_FRAMECMP", "0")
	resetChildren(t)
	blind, blindAUs := cmpArm(t, tex, "rave-mate framecmp 4k blind", w, h, 0, span)
	t.Logf("4K compare OFF (blind), static: aus=%d (%.1f/s) detect=%s childCPU=%.1f%% encBusy=%.2fms/encode",
		blindAUs, float64(blindAUs)/secs, blind.Detector, blind.ChildCPUPct, blind.EncBusyMs)

	if cmp.Detector != DetectGPUCompare {
		t.Fatalf("arm A detect=%s, want gpu-compare", cmp.Detector)
	}
	if blind.Detector != DetectBlindNoCompare {
		t.Fatalf("arm B detect=%s, want blind-nocmp (kill switch)", blind.Detector)
	}
	// A verdict not ready in its budget ENCODES (never a missed change), so a contended GPU (go test
	// runs GPU packages in parallel) legitimately adds encodes - up to one per timeout.
	if max := int(secs*2) + 2 + int(cmp.CmpTimeouts); cmpAUs > max {
		t.Fatalf("compare arm encoded %d AUs on a static 4K source, want keep-alive + timeouts only (<= %d)", cmpAUs, max)
	}
	if float64(blindAUs)/secs < 45 {
		t.Fatalf("blind arm %.1f encodes/s, want ~60 (the baseline the compare saves)", float64(blindAUs)/secs)
	}
	if cmp.CmpTimeouts > uint32(secs*60/20) {
		t.Fatalf("cmpTimeouts=%d: more than 5%% of 4K verdicts missed their budget", cmp.CmpTimeouts)
	}
}
