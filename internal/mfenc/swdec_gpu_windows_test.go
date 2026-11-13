//go:build windows && cgo && spout

package mfenc

// Real-GPU gates for the adaptive decode mode (.devnotes/VRAM_FULL_CARD_POLICY.md 1e). Env-gated: they
// allocate on the card. Each run is short (<= ~15 s of GPU work), checks free VRAM first (>= 4 GB) and
// aborts below 3 GB mid-run.
//
//	RAVE_MATE_SWDEC_GPU=1 RAVE_MATE_ENC_EXE=...\native\zigenc\zig-out\bin\rave-mate-enc.exe \
//	  go test -tags spout ./internal/mfenc -run 'TestSWDecode' -v

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/decmode"
	"rave.page/mate/internal/gpumem"
)

const nomW, nomH = 3840, 2160 // the destination is ALWAYS nominal (owner invariant)

func requireSWDecGPU(t *testing.T) gpumem.Sampler {
	t.Helper()
	if os.Getenv("RAVE_MATE_SWDEC_GPU") != "1" {
		t.Skip("real-GPU run: set RAVE_MATE_SWDEC_GPU=1 (needs >= 4 GB free VRAM)")
	}
	if !Available() {
		t.Skip("no hardware MF encode/decode pipeline")
	}
	requireEncExe(t)
	s := gpumem.NewSampler()
	requireFreeVRAM(t, s, 4096)
	return s
}

// bandPattern: top half RED, bottom half BLUE at w x h (RGBA) - the child's band probe checks it in the
// nominal destination after the VP scale.
func bandPattern(w, h int) []byte {
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		r, b := byte(255), byte(0)
		if y >= h/2 {
			r, b = 0, 255
		}
		for x := 0; x < w; x++ {
			p := pix[(y*w+x)*4:]
			p[0], p[1], p[2], p[3] = r, 0, b, 255
		}
	}
	return pix
}

// encodeAUs encodes n frames of the band pattern at w x h (H.264, GOP gop) and returns the AUs.
func encodeAUs(t *testing.T, w, h, n, gop int) []AU {
	t.Helper()
	enc, err := OpenProcSession(0, w, h, w, h, 60, 12000, gop)
	if err != nil {
		t.Skipf("no encode session: %v", err)
	}
	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range enc.Output() {
			aus = append(aus, AU{Data: append([]byte(nil), au.Data...), PTSNs: au.PTSNs, Keyframe: au.Keyframe})
		}
		close(done)
	}()
	img := bandPattern(w, h)
	base := time.Now().UnixNano()
	for i := 0; i < n; i++ {
		if err := enc.Encode(img, base+int64(i)*16_666_667); err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
	}
	enc.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("encoder output never closed")
	}
	if len(aus) == 0 || !aus[0].Keyframe {
		t.Fatalf("bad bitstream: %d AUs", len(aus))
	}
	return aus
}

// withAdaptive installs the adaptive seams for the test (threads, short hysteresis) and restores them.
func withAdaptive(t *testing.T, threads int) {
	old := DecCPUFallback
	DecCPUFallback = func() (bool, int, decmode.Config) {
		return true, threads, decmode.Config{Dwell: time.Hour} // policy never switches on its own here
	}
	t.Cleanup(func() { DecCPUFallback = old })
}

func childPIDOf(d *ProcDecSession) uint32 {
	d.child.mu.Lock()
	defer d.child.mu.Unlock()
	return uint32(d.child.cmd.Process.Pid)
}

// TestSWDecodeCodedSizes: software decode at 4K / 1080p / 720p coded into a NOMINAL 4K destination,
// vs the HW pipeline at the same size: frames, picture (child band read-back), cadence, CPU, capacity
// fps (frames per second of child decode+publish busy time) and child VRAM.
func TestSWDecodeCodedSizes(t *testing.T) {
	s := requireSWDecGPU(t)
	t.Setenv("RAVE_MATE_MFDEC_PROBE_BANDS", "1") // before the per-adapter child spawns
	withAdaptive(t, 4)
	logSupervisor(t)
	dst, err := newTestTexture(0, nomW, nomH, dxgiB8G8R8A8UNorm, false, nil)
	if err != nil {
		t.Skipf("no destination texture: %v", err)
	}
	defer dst.Close()
	sizes := []struct{ w, h int }{{3840, 2160}, {1920, 1080}, {1280, 720}}
	for _, sz := range sizes {
		aus := encodeAUs(t, sz.w, sz.h, 120, 60) // 2 s of 60 fps
		for _, sw := range []bool{false, true} {
			requireFreeVRAM(t, s, 3072)
			mode := "hw"
			if sw {
				mode = "cpu"
			}
			d, err := OpenProcDecSession(ProcDecOpts{LUID: 0, InW: sz.w, InH: sz.h, OutW: nomW, OutH: nomH, FPS: 60,
				KbpsHint: 12000, Software: sw,
				Dest: &DecodeDest{Name: "rave-mate swdec gate", Resolve: func() (uint64, uint32, int, int, bool) {
					return dst.Share, dxgiB8G8R8A8UNorm, nomW, nomH, true
				}}})
			if err != nil {
				t.Fatalf("%dx%d %s open: %v", sz.w, sz.h, mode, err)
			}
			pid := childPIDOf(d)
			time.Sleep(300 * time.Millisecond)
			vramOpen := childVRAM(s, pid)
			_ = d.Stats()
			start := time.Now()
			for i, au := range aus { // paced at 60 fps like a live route
				if err := d.Decode(au.Data, au.PTSNs, au.Keyframe); err != nil {
					t.Fatalf("decode %d: %v", i, err)
				}
				time.Sleep(time.Until(start.Add(time.Duration(i+1) * 16_666_667)))
			}
			time.Sleep(rateWindowSettle)
			st := d.Stats()
			vramRun := childVRAM(s, pid)
			capFPS := 0.0
			if st.DecBusyMs > 0 {
				capFPS = 1000 / st.DecBusyMs
			}
			t.Logf("%-4s coded %dx%d -> dest %dx%d: decoder=%q frames=%d/%d decFPS=%.1f busy=%.2fms capacity=%.0ffps cpu=%.0f%% errors=%d decDropped=%d childVRAM open=%dMB running=%dMB",
				mode, sz.w, sz.h, nomW, nomH, d.Name(), st.DecFrames, len(aus), st.DecFPS, st.DecBusyMs, capFPS, st.ChildCPUPct,
				st.DecErrors, st.DecDropped, vramOpen, vramRun)
			if st.DecErrors != 0 || st.DecFrames < uint64(len(aus)/2) {
				dumpChildTail(t, d.child)
				t.Fatalf("%s %dx%d: frames=%d errors=%d", mode, sz.w, sz.h, st.DecFrames, st.DecErrors)
			}
			assertChildBands(t, d.child)
			d.Close()
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// TestSWDecodeModeSwitchCycles: one live decode session switched HW -> CPU -> HW five times while frames
// keep flowing. Destination handle + size never change, a keyframe is requested at every switch, the
// fresh decoder never sees a non-key AU first (keygate: decDropped counts the gated AUs), no child
// restart, bounded publish gap, and child VRAM returns to its baseline.
func TestSWDecodeModeSwitchCycles(t *testing.T) {
	s := requireSWDecGPU(t)
	withAdaptive(t, 4)
	const w, h = 1920, 1080 // coded (the CPU-mode cap size), destination nominal 4K
	dst, err := newTestTexture(0, nomW, nomH, dxgiB8G8R8A8UNorm, false, nil)
	if err != nil {
		t.Skipf("no destination texture: %v", err)
	}
	defer dst.Close()
	enc, err := OpenProcSession(0, w, h, w, h, 60, 8000, 600) // long GOP: only REQUESTED IDRs arrive
	if err != nil {
		t.Skipf("no encode session: %v", err)
	}
	defer enc.Close()
	d, err := OpenProcDecSession(ProcDecOpts{LUID: 0, InW: w, InH: h, OutW: nomW, OutH: nomH, FPS: 60, KbpsHint: 8000,
		Dest: &DecodeDest{Name: "rave-mate swdec switch gate", Resolve: func() (uint64, uint32, int, int, bool) {
			return dst.Share, dxgiB8G8R8A8UNorm, nomW, nomH, true
		}}})
	if err != nil {
		t.Fatalf("decode open: %v", err)
	}
	defer d.Close()
	var kfReqs atomic.Int32
	d.SetKeyframeRequester(func() { kfReqs.Add(1); enc.ForceKeyframe() })
	pid := childPIDOf(d)
	restarts0 := d.Stats().Restarts

	// Live source: 60 fps frames encoded and fed until stop.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for au := range enc.Output() {
			_ = d.Decode(append([]byte(nil), au.Data...), au.PTSNs, au.Keyframe)
		}
	}()
	go func() {
		defer wg.Done()
		img := bandPattern(w, h)
		tk := time.NewTicker(16_666_667)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				_ = enc.Encode(img, time.Now().UnixNano())
			}
		}
	}()
	time.Sleep(time.Second)
	baseVRAM := childVRAM(s, pid)
	var maxGap time.Duration
	for cycle := 0; cycle < 5; cycle++ {
		for _, to := range []decmode.Mode{decmode.CPU, decmode.HW} {
			requireFreeVRAM(t, s, 3072)
			kf0 := kfReqs.Load()
			t0 := time.Now()
			ok, why := d.switchMode(to, "gate cycle")
			swDur := time.Since(t0)
			// The child's frame counter restarts with each decoder instance (and the old value lingers in
			// the header until the new one publishes): the oracle is the last-publish timestamp moving.
			lp0 := atomic.LoadInt64(d.shm.i64(offLastPubNs))
			if !ok {
				dumpChildTail(t, d.child)
				t.Fatalf("cycle %d switch to %s failed: %s", cycle, to, why)
			}
			var gap time.Duration
			for time.Since(t0) < 3*time.Second {
				if atomic.LoadInt64(d.shm.i64(offLastPubNs)) != lp0 {
					gap = time.Since(t0)
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if gap == 0 {
				dumpChildTail(t, d.child)
				t.Fatalf("cycle %d -> %s: no frame published within 3 s", cycle, to)
			}
			if gap > maxGap {
				maxGap = gap
			}
			if kfReqs.Load() == kf0 {
				t.Fatalf("cycle %d -> %s: no keyframe requested", cycle, to)
			}
			st := d.Stats()
			t.Logf("cycle %d -> %s: switch call %s, first publish after %s, decFrames=%d decDropped(keygate)=%d errors=%d childVRAM=%dMB software=%v",
				cycle, to, swDur.Round(time.Millisecond), gap.Round(time.Millisecond), st.DecFrames, st.DecDropped, st.DecErrors, childVRAM(s, pid), d.software.Load())
			if st.DecErrors != 0 {
				t.Fatalf("decode errors after the switch: %d", st.DecErrors)
			}
			time.Sleep(400 * time.Millisecond)
		}
	}
	close(stop)
	endVRAM := childVRAM(s, pid)
	st := d.Stats()
	t.Logf("10 switches: max publish gap %s, keyframe requests %d, child VRAM baseline=%dMB end=%dMB, restarts=%d",
		maxGap.Round(time.Millisecond), kfReqs.Load(), baseVRAM, endVRAM, st.Restarts-restarts0)
	if st.Restarts != restarts0 {
		t.Fatalf("child restarted during the switches (%d -> %d)", restarts0, st.Restarts)
	}
	if endVRAM > baseVRAM+32 {
		t.Fatalf("child VRAM grew across 10 switches: %d -> %d MB", baseVRAM, endVRAM)
	}
	if maxGap > 1500*time.Millisecond {
		t.Fatalf("publish gap %s across a switch exceeds 1.5 s", maxGap)
	}
	if h, _, _ := d.destHandle(); h != dst.Share {
		t.Fatalf("destination handle changed %#x -> %#x", dst.Share, h)
	}
	if strings.Contains(strings.ToLower(string(d.child.stderrTail)), "dstgone") {
		t.Fatal("child reported dstgone during the switches")
	}
	enc.Close()
	wg.Wait()
}

// logSupervisor routes the decode supervisor's Warnf/Infof into the test log.
func logSupervisor(t *testing.T) {
	ow, oi := Warnf, Infof
	Warnf = func(f string, a ...any) { t.Logf("supervisor: "+f, a...) }
	Infof = Warnf
	t.Cleanup(func() { Warnf, Infof = ow, oi })
}
