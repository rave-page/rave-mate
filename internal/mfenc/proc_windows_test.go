//go:build windows && cgo

package mfenc

// End-to-end tests for the Zig encoder child (native/zigenc) via the supervised
// shared-memory session client. Hardware-gated tests skip without an MF encoder;
// crash-containment tests run anywhere the exe builds.

import (
	"strings"
	"testing"
	"time"
)

func requireEncExe(t *testing.T) {
	t.Helper()
	if _, err := encExePath(); err != nil {
		t.Skip("rave-mate-enc.exe not built: ", err)
	}
}

// drainSession collects AUs until the channel closes.
func drainSession(s *ProcSession) (aus *int, done chan struct{}) {
	n := new(int)
	d := make(chan struct{})
	go func() {
		for range s.Output() {
			*n++
		}
		close(d)
	}()
	return n, d
}

// TestProcSession1080p60 runs a real session through the Zig child: open, 60 frames,
// live bitrate retarget + forced IDR, drain, close.
func TestProcSession1080p60(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	s, err := OpenProcSession(0, 1920, 1080, 1920, 1080, 60, 8000, 120)
	if err != nil {
		t.Fatalf("OpenProcSession: %v", err)
	}
	t.Logf("child encoder=%q bgra=%v", s.Name(), s.InputIsBGRA())
	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			aus = append(aus, au)
		}
		close(done)
	}()
	// two pre-built frames alternate (motion) so the loop times ENCODE, not frame gen
	frames := [2][]byte{make([]byte, 1920*1080*4), make([]byte, 1920*1080*4)}
	for k, f := range frames {
		for j := 0; j < len(f); j += 4 {
			f[j] = byte(j + k*97)
			f[j+3] = 255
		}
	}
	t0 := time.Now()
	for i := 0; i < 60; i++ {
		if i == 20 {
			s.SetBitrate(2000)
		}
		if i == 40 {
			s.ForceKeyframe()
		}
		if err := s.Encode(frames[i%2], int64(i)*16_666_667); err != nil {
			t.Fatalf("Encode %d: %v", i, err)
		}
	}
	encWall := time.Since(t0)
	t.Logf("60 frames submitted in %v (%.1f fps sustained)", encWall, 60/encWall.Seconds())
	time.Sleep(200 * time.Millisecond) // tail
	st := s.Stats()
	t.Logf("pre-close stats: %+v submitted=%d received=%d", st, s.submitted, s.received)
	s.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Output never closed")
	}
	if len(aus) < 30 {
		s.child.mu.Lock()
		tail := string(s.child.stderrTail)
		s.child.mu.Unlock()
		t.Fatalf("aus=%d want >=30; child stderr: %s", len(aus), tail)
	}
	if !aus[0].Keyframe {
		t.Fatal("first AU not keyframe")
	}
	var later int
	for i, au := range aus {
		if au.Keyframe && i >= 30 {
			later++
		}
	}
	if later == 0 {
		t.Fatal("ForceKeyframe produced no later keyframe")
	}
	if st.LatP50Ms <= 0 || st.LatP99Ms < st.LatP50Ms {
		t.Fatalf("latency stats implausible: %+v", st)
	}
	t.Logf("aus=%d p50=%.2fms p99=%.2fms depth=%d cpu=%.1f%%", len(aus), st.LatP50Ms, st.LatP99Ms, st.QueueDepth, st.ChildCPUPct)
}

// TestProcSession4K60CrashTuple is the exact field-crash tuple through the ISOLATED
// child: 3840x2160@60, 50 Mbps, gop 120, luid 0. Must encode or fail clean - and a
// child crash must never take this process down.
func TestProcSession4K60CrashTuple(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	s, err := OpenProcSession(0, 3840, 2160, 3840, 2160, 60, 50000, 120)
	if err != nil {
		t.Logf("clean failure (degrades to ffmpeg upstream): %v", err)
		return
	}
	n, done := drainSession(s)
	frame := make([]byte, 3840*2160*4)
	for i := 0; i < 10; i++ {
		if err := s.Encode(frame, int64(i)*16_666_667); err != nil {
			t.Fatalf("Encode %d: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	s.Close()
	<-done
	if *n < 5 {
		t.Fatalf("aus=%d want >=5", *n)
	}
	t.Logf("4K60 via child: aus=%d", *n)
}

// TestProcCrashMidRouteContinues proves THE P0 requirement by execution: the encoder
// child takes a deliberate AV mid-route (after 5 frames, first spawn only); the media
// child (this process) must survive, restart it, and the SAME session must keep
// producing AUs - route continuity across a driver crash.
func TestProcCrashMidRouteContinues(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	t.Setenv("RAVE_MATE_MFENC_TEST_FAULT_FIRST", "5")
	// Own luid → own child (registry is per-adapter; other tests already spawned luid 0,
	// and the fault knob fires on a child's FIRST spawn only). An unknown LUID degrades to
	// the default adapter inside the child.
	s, err := OpenProcSession(0x7e58, 640, 360, 640, 360, 30, 1500, 60)
	if err != nil {
		t.Fatalf("OpenProcSession: %v", err)
	}
	n, done := drainSession(s)
	frame := make([]byte, 640*360*4)
	for i := 0; i < 150; i++ {
		if err := s.Encode(frame, int64(i)*33_333_333); err != nil {
			t.Fatalf("Encode %d must never error across the crash: %v", i, err)
		}
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	st := s.Stats()
	s.Close()
	<-done
	if st.Restarts < 1 {
		t.Fatalf("expected >=1 child restart, got %d", st.Restarts)
	}
	if *n < 20 {
		t.Fatalf("aus=%d want >=20 (encoding must resume after the crash)", *n)
	}
	t.Logf("mid-route crash contained: aus=%d restarts=%d", *n, st.Restarts)
}

// TestProcCrashLoopFailsClean: a child that dies at startup EVERY time (worker-thread AV,
// the raw field failure mode) must yield clean open errors - never kill this process -
// and end in the crash-loop refusal that sends routes to ffmpeg.
func TestProcCrashLoopFailsClean(t *testing.T) {
	requireEncExe(t)
	t.Setenv("RAVE_MATE_MFENC_FAULT_INJECT_THREAD", "1")
	// Distinct luid so this test gets its own child object (no interference with the
	// shared luid-0 child from other tests).
	const luid = int64(0x7e57)
	sawLoop := false
	for i := 0; i < 6; i++ {
		_, err := OpenProcSession(luid, 320, 240, 320, 240, 30, 500, 30)
		if err == nil {
			t.Fatal("open must fail while the child crashes at startup")
		}
		if strings.Contains(err.Error(), "crash-looping") {
			sawLoop = true
			break
		}
		time.Sleep(700 * time.Millisecond) // let the supervisor count the exit
	}
	if !sawLoop {
		t.Fatal("crash-loop refusal never surfaced")
	}
}

// TestProcCloseUnreadOutputNoUAF (review CRITICAL 1): a consumer that abandoned Output()
// (mf_bridge emit on ctx cancel with full frames chan) must not turn Close into a UAF -
// pump must discard undeliverable AUs during teardown and exit BEFORE the shm unmaps;
// Close blocks on that ordering unconditionally and still returns promptly.
func TestProcCloseUnreadOutputNoUAF(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	s, err := OpenProcSession(0, 320, 240, 320, 240, 30, 500, 30)
	if err != nil {
		t.Fatalf("OpenProcSession: %v", err)
	}
	frame := make([]byte, 320*240*4)
	for i := 0; i < 16; i++ { // > out chan cap (8): tail AUs pile up with NO reader
		if err := s.Encode(frame, int64(i)*33_333_333); err != nil {
			t.Fatalf("Encode %d: %v", i, err)
		}
	}
	time.Sleep(300 * time.Millisecond) // let AUs land in chan + ring
	t0 := time.Now()
	s.Close()
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("Close stalled %v with an unread Output", d)
	}
	select { // Close returned → pump must already be done (shm was unmapped after it)
	case <-s.pumpDone:
	default:
		t.Fatal("Close returned before pump exited - unmap raced live shm reads")
	}
	s.child.mu.Lock()
	tail := string(s.child.stderrTail)
	s.child.mu.Unlock()
	t.Logf("child stderr tail: %s", tail)
}

// TestProcOpenDuringRespawnBackoff (review IMPORTANT 4): an open landing while the child
// is inside its restart backoff must WAIT for the respawn (waitUsable), not burn its
// attempts in microseconds and fall to ffmpeg.
func TestProcOpenDuringRespawnBackoff(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	t.Setenv("RAVE_MATE_MFENC_TEST_FAULT_FIRST", "1") // first spawn dies on frame 1
	const luid = int64(0x7e59)                        // own child (fault fires on first spawn only)
	a, err := OpenProcSession(luid, 320, 240, 320, 240, 30, 500, 30)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer a.Close()
	nA, doneA := drainSession(a)
	frame := make([]byte, 320*240*4)
	_ = a.Encode(frame, 0) // triggers the child AV
	for i := 0; i < 100 && !a.recovering.Load(); i++ {
		_ = a.Encode(frame, int64(i+1)*33_333_333)
		time.Sleep(10 * time.Millisecond)
	}
	// child is dead/backing off RIGHT NOW: this open must ride the respawn
	b, err := OpenProcSession(luid, 640, 480, 640, 480, 30, 1000, 60)
	if err != nil {
		t.Fatalf("open B during respawn backoff must succeed, got: %v", err)
	}
	nB, doneB := drainSession(b)
	bframe := make([]byte, 640*480*4)
	for i := 0; i < 10; i++ {
		if err := b.Encode(bframe, int64(i)*33_333_333); err != nil {
			t.Fatalf("B Encode %d: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	b.Close()
	<-doneB
	if *nB < 5 {
		t.Fatalf("B aus=%d want >=5", *nB)
	}
	a.Close()
	<-doneA
	_ = nA
}

// TestProcSessionOpenCloseCycles (review IMPORTANT 2+3): repeated open/encode/close on ONE
// long-lived child exercises the detached closer + per-session handle/view cleanup paths.
func TestProcSessionOpenCloseCycles(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	frame := make([]byte, 320*240*4)
	for i := 0; i < 10; i++ {
		s, err := OpenProcSession(0, 320, 240, 320, 240, 30, 500, 30)
		if err != nil {
			t.Fatalf("cycle %d open: %v", i, err)
		}
		n, done := drainSession(s)
		if err := s.Encode(frame, 0); err != nil {
			t.Fatalf("cycle %d encode: %v", i, err)
		}
		time.Sleep(50 * time.Millisecond)
		s.Close()
		<-done
		if *n < 1 {
			t.Fatalf("cycle %d: no AU", i)
		}
	}
}

// TestProcOpenFailKnobClean: the Go-side kill-switch short-circuits before any child spawn.
func TestProcOpenFailKnobClean(t *testing.T) {
	t.Setenv("RAVE_MATE_MFENC_OPEN_FAIL", "1")
	if _, err := OpenProcSession(0, 320, 240, 320, 240, 30, 500, 30); err == nil {
		t.Fatal("OpenProcSession succeeded despite RAVE_MATE_MFENC_OPEN_FAIL")
	}
}
