//go:build windows && cgo && spout

package mfenc

// Real-GPU gate for the child's ONE shared D3D11 device across encode + decode sessions. Env-gated like
// the swdec gates:
//
//	RAVE_MATE_SWDEC_GPU=1 RAVE_MATE_ENC_EXE=...\native\zigenc\zig-out\bin\rave-mate-enc.exe \
//	  go test -tags spout ./internal/mfenc -run TestDecSharedDevice -v

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDecSharedDevice: a live encode session + two decode sessions in one child. Both decoders lease the
// child's shared device, both publish, closing one leaves the other publishing (per-session teardown
// drops only its lease), and the second decoder's open costs no second device.
func TestDecSharedDevice(t *testing.T) {
	s := requireSWDecGPU(t)
	withAdaptive(t, 4)
	logSupervisor(t)
	const w, h = 1280, 720
	const dw, dh = 1920, 1080
	dstA, err := newTestTexture(0, dw, dh, dxgiB8G8R8A8UNorm, false, nil)
	if err != nil {
		t.Skipf("no destination texture: %v", err)
	}
	defer dstA.Close()
	dstB, err := newTestTexture(0, dw, dh, dxgiB8G8R8A8UNorm, false, nil)
	if err != nil {
		t.Skipf("no destination texture: %v", err)
	}
	defer dstB.Close()
	enc, err := OpenProcSession(0, w, h, w, h, 60, 6000, 60)
	if err != nil {
		t.Skipf("no encode session: %v", err)
	}
	defer enc.Close()
	open := func(name string, dst *testTexture) *ProcDecSession {
		d, err := OpenProcDecSession(ProcDecOpts{LUID: 0, InW: w, InH: h, OutW: dw, OutH: dh, FPS: 60, KbpsHint: 6000,
			Dest: &DecodeDest{Name: name, Resolve: func() (uint64, uint32, int, int, bool) {
				return dst.Share, dxgiB8G8R8A8UNorm, dw, dh, true
			}}})
		if err != nil {
			t.Fatalf("%s open: %v", name, err)
		}
		return d
	}
	a := open("rave-mate decshare A", dstA)
	pid := childPIDOf(a)
	time.Sleep(300 * time.Millisecond)
	vramA := childVRAM(s, pid)
	b := open("rave-mate decshare B", dstB)
	defer b.Close()
	if childPIDOf(b) != pid {
		t.Fatalf("decoders landed in different children: %d vs %d", pid, childPIDOf(b))
	}
	time.Sleep(300 * time.Millisecond)
	vramB := childVRAM(s, pid)
	perSession := os.Getenv("RAVE_MATE_MFENC_DEVICE") == "session" // A/B baseline: private devices
	if perSession == (a.DevShared() || b.DevShared()) {
		dumpChildTail(t, a.child)
		t.Fatalf("device lease: A=%v B=%v, want shared=%v", a.DevShared(), b.DevShared(), !perSession)
	}

	var aClosed atomic.Bool
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for au := range enc.Output() {
			if !aClosed.Load() {
				_ = a.Decode(append([]byte(nil), au.Data...), au.PTSNs, au.Keyframe)
			}
			_ = b.Decode(append([]byte(nil), au.Data...), au.PTSNs, au.Keyframe)
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
	time.Sleep(1500 * time.Millisecond)
	vramRun := childVRAM(s, pid)
	sa, sb := a.Stats(), b.Stats()
	t.Logf("enc shared=%v; decA frames=%d errors=%d; decB frames=%d errors=%d; child VRAM: after A open=%dMB after B open=%dMB running=%dMB",
		enc.Stats().DevShared, sa.DecFrames, sa.DecErrors, sb.DecFrames, sb.DecErrors, vramA, vramB, vramRun)
	if sa.DecFrames == 0 || sb.DecFrames == 0 || sa.DecErrors != 0 || sb.DecErrors != 0 {
		dumpChildTail(t, a.child)
		t.Fatalf("both decoders must publish without errors: A=%d/%d B=%d/%d", sa.DecFrames, sa.DecErrors, sb.DecFrames, sb.DecErrors)
	}

	aClosed.Store(true)
	a.Close()
	lp0 := atomic.LoadInt64(b.shm.i64(offLastPubNs))
	time.Sleep(time.Second)
	sb2 := b.Stats()
	if atomic.LoadInt64(b.shm.i64(offLastPubNs)) == lp0 || sb2.DecErrors != 0 {
		dumpChildTail(t, b.child)
		t.Fatalf("B must keep publishing after A closed: errors=%d", sb2.DecErrors)
	}
	t.Logf("after A close: decB frames=%d errors=%d restarts=%d child VRAM=%dMB", sb2.DecFrames, sb2.DecErrors, sb2.Restarts, childVRAM(s, pid))
	close(stop)
	enc.Close()
	wg.Wait()
}
