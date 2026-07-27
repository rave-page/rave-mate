//go:build windows && cgo

package mfenc

// zerocopy_risks_windows_test.go - the zigmedia risks that had never EXECUTED, only been reasoned
// about, and that block promoting zero-copy to the default:
//
//	R3  IDXGIKeyedMutex synchronisation (capFlags bit 1). Every Spout sender on every rig tested
//	    exposes Spout's NAMED access mutex, so the keyed branch - the one carrying the
//	    AcquireSync-hangs-the-session-thread hazard - had never run.
//	R4  TYPELESS / exotic source formats. No sender here produces one, so the allowlist and the
//	    CheckVideoProcessorFormat probe were unit-tested against a table, never against a driver.
//	R1  A restarted sender's CHANGED share handle. The oracle was gated by unit tests (a fabricated
//	    probe struct) and the recycle wiring, never by actually swapping a live texture underneath a
//	    running session - which is the "route looks healthy, ships a still frame" failure.
//	R7  A texture created on another adapter (only meaningful on a multi-adapter box).
//
// The instrument is newTestTexture: no Spout, no registry, no SpoutLibrary.dll - the child resolves
// its handle from a Go callback, so a texture with exactly the properties under test can be handed
// straight to it. Every arm asserts CONTENT (decoded pixels or bytes-per-frame), never "no error":
// three components in one night reported success while producing nothing.
//
//	go test ./internal/mfenc -run TestZeroCopyRisk -v

import (
	"bytes"
	"errors"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/mediatools"
)

const (
	riskW = 1280
	riskH = 720
)

// bgraBands builds the probe image in B8G8R8A8 byte order (what a real Spout sender publishes):
// top half `top`, bottom half `bot`, each as an RGB triple. A vertical flip and an R/B swizzle are
// both visible in one decoded frame.
func bgraBands(w, h int, top, bot [3]byte) []byte {
	px := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		c := top
		if y >= h/2 {
			c = bot
		}
		for x := 0; x < w; x++ {
			p := px[(y*w+x)*4:]
			p[0], p[1], p[2], p[3] = c[2], c[1], c[0], 255 // B,G,R,A
		}
	}
	return px
}

// assertProbeBands decodes the bitstream and asserts the top/bottom bands survived. This is the
// content gate: rows, channels and "is there a picture at all" in one measurement. Skipped (with a
// loud log) when ffmpeg is absent - a silent skip on a content gate would be the exact failure
// shape these tests exist to catch.
func assertProbeBands(t *testing.T, aus []AU, w, h int, wantTop, wantBot [3]byte) {
	t.Helper()
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Log("ffmpeg not found - orientation/colour NOT verified this run")
		return
	}
	var in bytes.Buffer
	for _, au := range aus {
		in.Write(au.Data)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
		"-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil || len(out) < w*h*3 {
		t.Fatalf("decode failed (%v), got %d bytes want %d", err, len(out), w*h*3)
	}
	at := func(x, y int) (byte, byte, byte) {
		i := (y*w + x) * 3
		return out[i], out[i+1], out[i+2]
	}
	// Sample well inside each band: chroma subsampling and a keyframe's quantisation both smear
	// the boundary.
	tr, tg, tb := at(w/2, h/8)
	br, bg, bb := at(w/2, h*7/8)
	near := func(got, want byte) bool {
		d := int(got) - int(want)
		return d > -72 && d < 72
	}
	if !near(tr, wantTop[0]) || !near(tg, wantTop[1]) || !near(tb, wantTop[2]) {
		t.Fatalf("top band decoded rgb(%d,%d,%d), want ~rgb(%d,%d,%d) - rows or channels are swapped",
			tr, tg, tb, wantTop[0], wantTop[1], wantTop[2])
	}
	if !near(br, wantBot[0]) || !near(bg, wantBot[1]) || !near(bb, wantBot[2]) {
		t.Fatalf("bottom band decoded rgb(%d,%d,%d), want ~rgb(%d,%d,%d) - rows or channels are swapped",
			br, bg, bb, wantBot[0], wantBot[1], wantBot[2])
	}
	t.Logf("content verified: top rgb(%d,%d,%d) bottom rgb(%d,%d,%d)", tr, tg, tb, br, bg, bb)
}

// riskSession opens a zero-copy session over tex (a texture WE own) at the given geometry.
func riskSession(t *testing.T, name string, resolve func() (uint64, uint32, int, int, bool), w, h int) (*ProcSession, error) {
	t.Helper()
	return OpenProcSessionOpts(ProcOpts{
		LUID: 0, InW: w, InH: h, OutW: w, OutH: h, FPS: 60, Kbps: 8000, Gop: 60,
		Spout: &SpoutSource{Name: name, Resolve: resolve},
	})
}

// collectAUs drains a session's output into a slice until the channel closes.
func collectAUs(s *ProcSession) (*[]AU, chan struct{}) {
	aus := &[]AU{}
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			*aus = append(*aus, au)
		}
		close(done)
	}()
	return aus, done
}

// TestZeroCopyRiskKeyedMutex executes the IDXGIKeyedMutex branch (R3) for the first time on real
// hardware: a shared texture created with MISC_SHARED_KEYEDMUTEX, so the child's QI succeeds and it
// takes the keyed path instead of Spout's named mutex.
//
// What must hold, and why each matters:
//   - capFlags bit 1 SET and bit 2 CLEAR: the keyed path really ran (otherwise this test proves
//     nothing new - it would just be the named-mutex path again).
//   - frames captured and AUs produced with the probe's PIXELS in them: R3's hazard is that
//     AcquireSync hangs or starves the session thread, which would show as zero/stalled capture.
//   - mtxTimeouts must not run away: a bounded acquire that ALWAYS times out is a live-lock, and
//     the counter is the only thing that can tell it from healthy pacing.
func TestZeroCopyRiskKeyedMutex(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	red, blue := [3]byte{255, 0, 0}, [3]byte{0, 0, 255}
	tex, err := newTestTexture(0, riskW, riskH, dxgiB8G8R8A8UNorm, true, bgraBands(riskW, riskH, red, blue))
	if err != nil {
		t.Skipf("no keyed-mutex shared texture on this box: %v", err)
	}
	defer tex.Close()
	t.Logf("keyed-mutex texture: handle=%#x fmt=%d %dx%d", tex.Share, dxgiB8G8R8A8UNorm, riskW, riskH)

	s, err := riskSession(t, "rave-mate keyedmutex gate", func() (uint64, uint32, int, int, bool) {
		return tex.Share, dxgiB8G8R8A8UNorm, riskW, riskH, true
	}, riskW, riskH)
	if err != nil {
		t.Fatalf("keyed-mutex zero-copy open refused: %v", err)
	}
	if !s.IsZeroCopy() {
		t.Fatal("session did not come up zero-copy")
	}
	aus, done := collectAUs(s)
	time.Sleep(500 * time.Millisecond)
	st := statsAfterRateWindow(s)
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}

	t.Logf("R3 keyed mutex: aus=%d capFrames=%d capFPS=%.1f skips=%d mtxTimeouts=%d srcErrors=%d "+
		"encBusy=%.2fms capFmt=%d capFlags=%#x", len(*aus), st.CapFrames, st.CapFPS, st.CapSkips,
		st.MtxTimeouts, st.SrcErrors, st.EncBusyMs, st.CapFmt, st.CapFlags)

	const flagKeyed, flagNamed = 1 << 1, 1 << 2
	if st.CapFlags&flagKeyed == 0 {
		t.Fatalf("capFlags=%#x: the KEYED-mutex branch did not run, so this test proved nothing new", st.CapFlags)
	}
	if st.CapFlags&flagNamed != 0 {
		t.Fatalf("capFlags=%#x: both keyed and named mutex bits set", st.CapFlags)
	}
	if st.CapFrames < 30 {
		t.Fatalf("capFrames=%d over 2 s at 60 fps - AcquireSync is starving the session thread (R3)", st.CapFrames)
	}
	if st.CapFPS < 30 {
		t.Fatalf("capFPS=%.1f: the keyed acquire is not sustaining the paced rate", st.CapFPS)
	}
	if st.EncBusyMs <= 0 || st.EncBusyMs > 25 {
		t.Fatalf("encBusyMs=%.2f, want a plausible per-frame capture+encode cost", st.EncBusyMs)
	}
	if st.SrcErrors != 0 {
		t.Fatalf("srcErrors=%d on a healthy keyed-mutex texture", st.SrcErrors)
	}
	if st.MtxTimeouts > st.CapFrames/4 {
		t.Fatalf("mtxTimeouts=%d against capFrames=%d - the keyed acquire is live-locking", st.MtxTimeouts, st.CapFrames)
	}
	if len(*aus) < 20 {
		s.child.mu.Lock()
		tail := string(s.child.stderrTail)
		s.child.mu.Unlock()
		t.Fatalf("aus=%d want >= 20; child stderr: %s", len(*aus), tail)
	}
	// CONTENT, not "no error": a keyed-mutex path that acquired but blitted nothing would satisfy
	// every counter above.
	assertProbeBands(t, *aus, riskW, riskH, red, blue)
}

// TestZeroCopyRiskFormatRefusal executes the format allowlist + CheckVideoProcessorFormat probe
// (R4) against a real driver. A TYPELESS source must be REFUSED cleanly - not guessed at with a
// typed view (undefined behaviour), not crashed on, and not accepted into a black route.
func TestZeroCopyRiskFormatRefusal(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	cases := []struct {
		name string
		fmt  uint32
	}{
		{"B8G8R8A8_TYPELESS", dxgiB8G8R8A8Typeless},
		{"B8G8R8X8_TYPELESS", 92},
		{"R8G8B8A8_SNORM", 31},
		{"R16G16B16A16_UNORM", 11},
	}
	refused := 0
	for _, c := range cases {
		tex, err := newTestTexture(0, riskW, riskH, c.fmt, false, nil)
		if err != nil {
			t.Logf("%s: driver will not create this shared texture (%v) - arm skipped", c.name, err)
			continue
		}
		s, err := riskSession(t, "rave-mate fmt gate "+c.name, func() (uint64, uint32, int, int, bool) {
			return tex.Share, c.fmt, riskW, riskH, true
		}, riskW, riskH)
		if err == nil {
			st := s.Stats()
			zc := s.IsZeroCopy()
			s.Close()
			tex.Close()
			if zc {
				t.Errorf("%s (fmt %d) was ACCEPTED as a zero-copy source (capFlags=%#x) - R4's allowlist did not bite",
					c.name, c.fmt, st.CapFlags)
			}
			continue
		}
		tex.Close()
		refused++
		if !errors.Is(err, ErrZeroCopyRefused) {
			t.Errorf("%s: refused with %v, want an ErrZeroCopyRefused the caller can downgrade on", c.name, err)
			continue
		}
		t.Logf("%s (fmt %d) refused: %v", c.name, c.fmt, err)
	}
	if refused == 0 {
		t.Skip("no exotic format could be created on this box - R4 stays unexecuted here")
	}
}

// TestZeroCopyRiskChangedHandleRecycles is R1 on hardware: a session running over texture A has
// texture B swapped in underneath it (a different share handle, the same geometry - exactly what a
// sender restart looks like). The 2 s health oracle must notice the CHANGED handle and recycle the
// session onto the new texture.
//
// The oracle was previously gated by a fabricated probe struct and the recycle wiring; what makes
// this the real gate is that the assertion is the PICTURE: texture B carries different colours, so
// a session that "recovered" onto the dead texture A, or never recycled at all, decodes A's bands
// and fails. That is the frozen-picture failure mode R1 names, and no counter can see it.
func TestZeroCopyRiskChangedHandleRecycles(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	red, blue := [3]byte{255, 0, 0}, [3]byte{0, 0, 255}
	green, white := [3]byte{0, 255, 0}, [3]byte{255, 255, 255}
	texA, err := newTestTexture(0, riskW, riskH, dxgiB8G8R8A8UNorm, false, bgraBands(riskW, riskH, red, blue))
	if err != nil {
		t.Skipf("no shared texture on this box: %v", err)
	}
	defer texA.Close()
	texB, err := newTestTexture(0, riskW, riskH, dxgiB8G8R8A8UNorm, false, bgraBands(riskW, riskH, green, white))
	if err != nil {
		t.Skipf("second shared texture failed: %v", err)
	}
	defer texB.Close()
	if texA.Share == texB.Share {
		t.Fatal("two textures share one handle - the oracle has nothing to detect")
	}

	live := texA
	s, err := riskSession(t, "rave-mate r1 gate", func() (uint64, uint32, int, int, bool) {
		return live.Share, dxgiB8G8R8A8UNorm, riskW, riskH, true
	}, riskW, riskH)
	if err != nil {
		t.Fatalf("zero-copy open: %v", err)
	}
	if !s.IsZeroCopy() {
		t.Fatal("session did not come up zero-copy")
	}
	aus, done := collectAUs(s)
	time.Sleep(1200 * time.Millisecond)
	before := s.Stats()
	if before.CapFrames < 20 {
		s.Close()
		<-done
		t.Fatalf("capFrames=%d before the swap - the session was not capturing to begin with", before.CapFrames)
	}
	// The swap. From here the resolver answers with B's handle, which is what a re-created sender
	// looks like to the 2 s registry tick.
	swapAt := len(*aus)
	live = texB
	// Two watch ticks plus a reopen: the recycle closes and reopens the session inside the child.
	time.Sleep(5500 * time.Millisecond)
	after := s.Stats()
	tailAUs := append([]AU(nil), (*aus)[min(swapAt+2, len(*aus)):]...)
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}

	t.Logf("R1 handle swap: capFrames %d→%d downgrades %d→%d aus=%d (swap at %d) srcErrors=%d",
		before.CapFrames, after.CapFrames, before.Downgrades, after.Downgrades, len(*aus), swapAt, after.SrcErrors)

	if after.Downgrades <= before.Downgrades {
		t.Fatalf("downgrades %d→%d: the changed handle was never detected (R1 - the route would ship a frozen picture)",
			before.Downgrades, after.Downgrades)
	}
	if after.CapFrames == 0 {
		t.Fatal("capFrames reset to 0 and never recovered: the recycle killed the capture")
	}
	if len(tailAUs) < 20 {
		t.Fatalf("only %d AUs after the swap - the route did not resume", len(tailAUs))
	}
	// THE assertion: the picture followed the new texture. Decoding the tail must show B's bands.
	assertProbeBands(t, tailAUs, riskW, riskH, green, white)
}

// TestZeroCopyRiskGeometryMismatch: a sender that resized under us must be REFUSED at open, not
// encoded at the wrong size (which would ship a torn or letterboxed picture with clean counters).
func TestZeroCopyRiskGeometryMismatch(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	tex, err := newTestTexture(0, 640, 360, dxgiB8G8R8A8UNorm, false, nil)
	if err != nil {
		t.Skipf("no shared texture on this box: %v", err)
	}
	defer tex.Close()
	// The session negotiated 1280x720; the texture is 640x360.
	s, err := riskSession(t, "rave-mate dim gate", func() (uint64, uint32, int, int, bool) {
		return tex.Share, dxgiB8G8R8A8UNorm, riskW, riskH, true
	}, riskW, riskH)
	if err == nil {
		zc := s.IsZeroCopy()
		s.Close()
		if zc {
			t.Fatal("a 640x360 texture was accepted for a 1280x720 session")
		}
		return
	}
	if !errors.Is(err, ErrZeroCopyRefused) {
		t.Fatalf("geometry mismatch refused with %v, want ErrZeroCopyRefused", err)
	}
	t.Logf("geometry mismatch refused: %v", err)
}
