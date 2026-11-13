//go:build windows && cgo

package mfenc

// REAL-GPU verification of the native receive path (unit C part 2, step 5): MF encode session (4K,
// synthetic frame) -> AUs piped into rave-mate-txtest (zigmedia native sender) -> UDP loopback ->
// child network source -> PTS-locked playout -> D3D11 decode -> publish into an UNCHANGED nominal 4K
// destination texture. Phase A: publish-interval acceptance on a clean run (p99 <= 18 ms, zero
// > 25 ms after warm-up). Phase B: 10 toggles of the coded-resolution cap with the destination
// unchanged, zero recycle/pin. Opt-in ONLY (RAVE_MATE_NETRX_GPU=1); VRAM gate >= 4 GB free before,
// aborts below 3 GB, total run <= ~15 s.
//
//	RAVE_MATE_NETRX_GPU=1 go test ./internal/mfenc -run TestNetRXDecodeLive4K -v -count=1

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"rave.page/mate/internal/gpumem"
)

func TestNetRXDecodeLive4K(t *testing.T) {
	if os.Getenv("RAVE_MATE_NETRX_GPU") != "1" {
		t.Skip("real-GPU run: set RAVE_MATE_NETRX_GPU=1 (needs >= 4 GB free VRAM)")
	}
	if !Available() {
		t.Skip("no hardware MF encode/decode pipeline")
	}
	_, txExe := netExes(t)
	sampler := gpumem.NewSampler()
	requireFreeVRAM(t, sampler, 4096)
	fps := 60
	if v, err := strconv.Atoi(os.Getenv("RAVE_MATE_NETRX_GPU_FPS")); err == nil && v > 0 {
		fps = v
	}
	period := time.Second / time.Duration(fps)

	var logMu sync.Mutex
	var logLines []string
	capture := func(f string, a ...any) {
		logMu.Lock()
		logLines = append(logLines, fmt.Sprintf(f, a...))
		logMu.Unlock()
	}
	oldWarn, oldInfo := Warnf, Infof
	Warnf, Infof = capture, capture
	defer func() { Warnf, Infof = oldWarn, oldInfo }()

	dst, err := newTestTexture(0, capNomW, capNomH, dxgiB8G8R8A8UNorm, false, nil)
	if err != nil {
		t.Skipf("no destination shared texture on this host: %v", err)
	}
	defer dst.Close()
	dstHandle := dst.Share

	enc, err := OpenProcSession(0, capNomW, capNomH, capNomW, capNomH, float64(fps), 50000, fps)
	if err != nil {
		t.Skipf("no encode session: %v", err)
	}
	defer enc.Close()
	dec, err := OpenProcDecSession(ProcDecOpts{LUID: 0, InW: capNomW, InH: capNomH, OutW: capNomW, OutH: capNomH,
		FPS: float64(fps), KbpsHint: 50000,
		Dest: &DecodeDest{Name: "rave-mate netrx dst", Resolve: func() (uint64, uint32, int, int, bool) {
			return dstHandle, dxgiB8G8R8A8UNorm, capNomW, capNomH, dstHandle != 0
		}}})
	if err != nil {
		t.Fatalf("native decode open: %v", err)
	}
	defer dec.Close()
	if !dec.NetCapable() {
		t.Fatal("decode session has no network source support (child < v5)")
	}
	k := newNetKeys(t)
	h, port := parkUDP(t)
	if err := dec.AttachNet(NetRXParams{Socket: h, Encrypt: true, Token: k.token, OpenKey: k.k1, OpenPrefix: k.p1,
		SendKey: k.k2, SendPrefix: k.p2, FPS: float64(fps), Preset: "balanced", Stream: 1, InitialSeq: 1000}); err != nil {
		_ = windows.Closesocket(windows.Handle(h))
		t.Fatalf("attach: %v", err)
	}
	tx := startTx(t, txExe, port, k, map[string]any{"au": "pipe", "codec": 4})
	t.Logf("encoder=%q decoder=%q hw=%v dst=%#x %dx%d @%d fps", enc.Name(), dec.Name(), dec.IsHardware(), dstHandle, capNomW, capNomH, fps)

	// Encoder AUs -> txtest stdin ([u32 len][u32 flags][i64 pts][data], little-endian).
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		var hdr [16]byte
		for au := range enc.Output() {
			binary.LittleEndian.PutUint32(hdr[0:], uint32(len(au.Data)))
			fl := uint32(0)
			if au.Keyframe {
				fl = 1
			}
			binary.LittleEndian.PutUint32(hdr[4:], fl)
			binary.LittleEndian.PutUint64(hdr[8:], uint64(au.PTSNs))
			if _, err := tx.stdin.Write(hdr[:]); err != nil {
				return
			}
			if _, err := tx.stdin.Write(au.Data); err != nil {
				return
			}
		}
	}()
	frame := makeCapFrame(capNomW, capNomH)
	t0 := time.Now()
	pts := t0.UnixNano()
	i := 0
	feedFor := func(d time.Duration) {
		end := time.Now().Add(d)
		lastVRAM := time.Now()
		for time.Now().Before(end) {
			if err := enc.Encode(frame, pts); err != nil {
				t.Fatalf("encode: %v", err)
			}
			i++
			pts += int64(period)
			time.Sleep(time.Until(t0.Add(time.Duration(i) * period)))
			if time.Since(lastVRAM) > time.Second {
				lastVRAM = time.Now()
				requireFreeVRAM(t, sampler, 3072) // abort below 3 GB
			}
		}
	}

	// Phase A: clean run. Warm-up 2 s, then 4 s measured.
	feedFor(2 * time.Second)
	warm, _ := dec.NetStats()
	feedFor(4 * time.Second)
	a, _ := dec.NetStats()
	t.Logf("phase A: published=%d publish p50/p99/max=%.1f/%.1f/%.1f ms over25 %d->%d release p99/max=%.1f/%.1f playout=%.1fms misses=%d decErrors=%d rxErrors=%d",
		a.Published, a.PubP50Ms, a.PubP99Ms, a.PubMaxMs, warm.PubOver25, a.PubOver25, a.RelP99Ms, a.RelMaxMs, a.PlayoutMs, a.Misses, a.DecErrors, a.RxErrors)
	if a.Published < uint64(5*fps) || a.DecErrors != 0 {
		t.Fatalf("phase A: published=%d decErrors=%d", a.Published, a.DecErrors)
	}
	if a.PubP99Ms > 18 || a.PubOver25 != warm.PubOver25 {
		t.Errorf("phase A acceptance: publish p99 %.1f ms (<= 18), %d intervals > 25 ms after warm-up (0)", a.PubP99Ms, a.PubOver25-warm.PubOver25)
	}

	// Phase B: 10 cap toggles, destination unchanged.
	before := dec.Stats()
	for n := 0; n < capToggles; n++ {
		w, hh := capNomW, capNomH
		if n%2 == 0 {
			w, hh = capLowW, capLowH
		}
		NoteDecodeResizeGrace()
		if !enc.SetCapHeight(w, hh) {
			t.Fatalf("toggle %d: SetCapHeight(%d,%d) false", n, w, hh)
		}
		feedFor(450 * time.Millisecond)
	}
	feedFor(300 * time.Millisecond)
	st := dec.Stats()
	b, _ := dec.NetStats()
	t.Logf("phase B: decResizes %d->%d decFrames %d->%d restarts=%d coded=%dx%d published=%d keyWaits=%d plis=%d",
		before.DecResizes, st.DecResizes, before.DecFrames, st.DecFrames, st.Restarts, st.DecCodedW, st.DecCodedH, b.Published, b.KeyWaits, b.PLIsSent)
	if st.DecResizes-before.DecResizes < capToggles-1 || st.DecFrames <= before.DecFrames || st.Restarts != 0 || dstHandle != dst.Share {
		t.Fatalf("phase B: resizes=%d frames %d->%d restarts=%d dst %#x->%#x", st.DecResizes-before.DecResizes, before.DecFrames, st.DecFrames, st.Restarts, dstHandle, dst.Share)
	}
	logMu.Lock()
	joined := strings.ToLower(strings.Join(logLines, "\n"))
	logMu.Unlock()
	for _, bad := range []string{"reopen failed", "pinning this sender", "pinned to the", "ffmpeg decode path", "recycling ("} {
		if strings.Contains(joined, bad) {
			t.Fatalf("supervisor log contains %q:\n%s", bad, joined)
		}
	}
	enc.Close()
	<-pumpDone
	tx.quit()
}
