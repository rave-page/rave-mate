//go:build windows && cgo

package mfenc

// Concurrent-session gates for the encoder child. THE field defect these exist for: on AMD a
// single encode session is clean (real content, zero drops, no fault), but opening a SECOND
// session in the same child wedged it 3/3 - no METransformNeedInput, the parent timed out at
// 2.2 s, the route ended and the child later took an access violation. Every gate in this
// package was single-session, so the whole class was invisible on the dev box.
//
// Judging by counters alone is what hid the original problem (a 4K route carried BLACK for 12
// minutes while every counter read healthy), so these gates assert BYTES OF REAL BITSTREAM per
// session, not just survival: two sessions fed distinct moving content must each produce AUs
// whose size is consistent with actual picture data, and the child must still be alive at the end.

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// sessionRun is one concurrent session's outcome.
type sessionRun struct {
	name     string
	aus      int
	bytes    int
	keyed    int
	encErr   error
	drive    string
	software bool
	degrade  string
}

// runConcurrent opens n sessions on the SAME child, feeds each `frames` frames of distinct moving
// content, and reports each one's bitstream. Sessions run in parallel goroutines, which is the
// point: the field failure needs two live sessions at once, not two in sequence.
func runConcurrent(t *testing.T, geoms [][2]int, frames int) []sessionRun {
	t.Helper()
	out := make([]sessionRun, len(geoms))
	sessions := make([]*ProcSession, len(geoms))
	for i, g := range geoms {
		w, h := g[0], g[1]
		// luid 0 for every session = the SAME child (children are keyed by adapter LUID), which
		// is what puts both sessions in one process.
		s, err := OpenProcSession(0, w, h, w, h, 30, 8000, 60)
		if err != nil {
			for _, prev := range sessions[:i] {
				if prev != nil {
					prev.Close()
				}
			}
			t.Fatalf("OpenProcSession(%dx%d) [session %d of %d]: %v", w, h, i+1, len(geoms), err)
		}
		sessions[i] = s
		out[i] = sessionRun{name: s.Name(), drive: s.Drive(), software: s.IsSoftware()}
		t.Logf("session %d: %dx%d encoder=%q drive=%s software=%v degrade=%q",
			i+1, w, h, s.Name(), s.Drive(), s.IsSoftware(), s.DegradeReason())
	}

	collected := make([]chan sessionRun, len(sessions))
	for i, s := range sessions {
		collected[i] = make(chan sessionRun, 1)
		go func(idx int, s *ProcSession, ch chan sessionRun) {
			r := sessionRun{}
			for au := range s.Output() {
				r.aus++
				r.bytes += len(au.Data)
				if au.Keyframe {
					r.keyed++
				}
			}
			ch <- r
		}(i, s, collected[i])
	}

	// Feed all sessions CONCURRENTLY - serialising them would not reproduce the failure.
	fed := make([]chan error, len(sessions))
	for i, s := range sessions {
		fed[i] = make(chan error, 1)
		go func(idx int, s *ProcSession, ch chan error) {
			w, h := geoms[idx][0], geoms[idx][1]
			// Distinct MOVING content per session: a flat frame encodes to almost nothing, so a
			// broken pipeline could pass a bytes check on black. Each frame shifts a gradient.
			buf := make([]byte, w*h*4)
			for f := 0; f < frames; f++ {
				for p := 0; p < len(buf); p += 4 {
					v := byte((p/4 + f*17 + idx*61) % 251)
					buf[p], buf[p+1], buf[p+2], buf[p+3] = v, byte(255-int(v)), byte(idx*80+f), 255
				}
				if err := s.Encode(buf, int64(f)*33_333_333); err != nil {
					ch <- fmt.Errorf("frame %d: %w", f, err)
					return
				}
			}
			ch <- nil
		}(i, s, fed[i])
	}
	for i := range sessions {
		out[i].encErr = <-fed[i]
	}
	time.Sleep(300 * time.Millisecond) // encoder tail
	for i, s := range sessions {
		out[i].degrade = s.DegradeReason()
		if out[i].drive == "" {
			out[i].drive = s.Drive()
		}
		s.Close()
		r := <-collected[i]
		out[i].aus, out[i].bytes, out[i].keyed = r.aus, r.bytes, r.keyed
	}
	return out
}

// childAliveAfter reports whether the child for this adapter is still running (no AV).
func childAliveAfter(luid int64) (alive bool, tail string, fails int) {
	childMu.Lock()
	c := children[luid]
	childMu.Unlock()
	if c == nil {
		return false, "no child", 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.dead, string(c.stderrTail), c.consecFail
}

// TestProcTwoSessionsOneChild is the AMD field repro, and the gate that must not regress on
// NVIDIA. Two live sessions in ONE child, fed in parallel, both must produce real bitstream and
// the child must survive.
func TestProcTwoSessionsOneChild(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	// 720p + 480p: different geometry per session, so each session really needs its own video
	// processor + NV12 pool while sharing the child's device.
	runs := runConcurrent(t, [][2]int{{1280, 720}, {854, 480}}, 45)

	alive, tail, fails := childAliveAfter(0)
	for i, r := range runs {
		t.Logf("session %d result: encoder=%q drive=%s sw=%v aus=%d bytes=%d keyframes=%d err=%v degrade=%q",
			i+1, r.name, r.drive, r.software, r.aus, r.bytes, r.keyed, r.encErr, r.degrade)
	}
	t.Logf("child alive=%v fails=%d", alive, fails)
	for i, r := range runs {
		if r.encErr != nil {
			t.Errorf("session %d: Encode failed with a sibling session live: %v (child stderr tail: %s)", i+1, r.encErr, tail)
		}
		if r.aus < 20 {
			t.Errorf("session %d: aus=%d, want >=20 - a session starved while a sibling was live", i+1, r.aus)
		}
		if r.keyed == 0 {
			t.Errorf("session %d: no keyframe at all", i+1)
		}
		// REAL CONTENT, not just "some bytes": moving gradients at 720p/480p cannot compress to a
		// few hundred bytes per frame. Black/frozen output is the failure shape that reported
		// healthy for 12 minutes in the field, so it must fail a gate here.
		if r.aus > 0 {
			perFrame := r.bytes / r.aus
			if perFrame < 400 {
				t.Errorf("session %d: %d bytes over %d AUs = %d B/frame - that is black or frozen content, not a real picture",
					i+1, r.bytes, r.aus, perFrame)
			}
		}
	}
	if !alive {
		t.Errorf("encoder child died with two concurrent sessions (the AMD field failure); stderr tail: %s", tail)
	}
}

// TestProcTwoSessionsPerSessionDevice runs the SAME two-session load with the old per-session
// device policy. It is the A/B half of the AMD diagnosis: if the concurrent-session failure is
// caused by two D3D11 devices + two device managers on one adapter, this configuration fails on
// AMD while the default (one device per child) passes, and both pass on NVIDIA.
//
// Not skipped on NVIDIA: it also guards the fallback knob itself from rotting.
func TestProcTwoSessionsPerSessionDevice(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	t.Setenv("RAVE_MATE_MFENC_DEVICE", "session")
	// A fresh child is required for the env var to apply (policy is read at child startup).
	resetChildren(t)
	runs := runConcurrent(t, [][2]int{{1280, 720}, {854, 480}}, 45)
	alive, tail, _ := childAliveAfter(0)
	for i, r := range runs {
		t.Logf("per-session-device session %d: encoder=%q drive=%s aus=%d bytes=%d err=%v",
			i+1, r.name, r.drive, r.aus, r.bytes, r.encErr)
	}
	if !alive {
		t.Errorf("per-session-device policy killed the child with two sessions; stderr tail: %s", tail)
	}
	for i, r := range runs {
		if r.encErr != nil {
			t.Errorf("per-session-device session %d: %v", i+1, r.encErr)
		}
	}
}

// TestProcFieldTupleTwoSessions is the EXACT field repro: a 4K60 50 Mbps session live while a
// 720p30 session opens in the same child. On the AMD rig that combination wedged the second
// session 3/3 (no METransformNeedInput, parent timeout at 2.2 s, child AV ~50 s later) while
// either session ALONE was clean.
//
// It also separates the two candidate causes, which the 720p+480p gate above cannot:
//   - if this fails on AMD but 720p+480p passes, the trigger is encoder CAPACITY (a 4K60 route
//     plus anything else saturates one iGPU's VCN), and the fix that matters is the saturation
//     path: short per-frame budget + counted drops instead of a route-ending timeout;
//   - if BOTH fail on AMD, the trigger is session multiplexing itself, and the shared-device
//     policy (RAVE_MATE_MFENC_DEVICE, default `child`) is the fix - A/B it against `session`.
func TestProcFieldTupleTwoSessions(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	resetChildren(t)
	s4k, err := OpenProcSession(0, 3840, 2160, 3840, 2160, 60, 50000, 120)
	if err != nil {
		t.Skipf("4K60 session unavailable here: %v", err)
	}
	t.Logf("4K60 session: encoder=%q drive=%s sw=%v", s4k.Name(), s4k.Drive(), s4k.IsSoftware())
	aus4k, done4k := drainSession(s4k)
	// Keep the 4K session LIVE while the second one opens - that is the whole point.
	big := make([]byte, 3840*2160*4)
	for p := 0; p < len(big); p += 4 {
		big[p], big[p+3] = byte(p/4), 255
	}
	feed4kDone := make(chan struct{})
	stop4k := make(chan struct{})
	go func() {
		defer close(feed4kDone)
		for i := 0; ; i++ {
			select {
			case <-stop4k:
				return
			default:
			}
			if err := s4k.Encode(big, int64(i)*16_666_667); err != nil {
				t.Logf("4K session Encode stopped: %v", err)
				return
			}
		}
	}()
	time.Sleep(500 * time.Millisecond) // let the 4K route reach steady state, as in the field

	s720, err := OpenProcSession(0, 1280, 720, 1280, 720, 30, 8000, 60)
	if err != nil {
		close(stop4k)
		<-feed4kDone
		s4k.Close()
		<-done4k
		t.Fatalf("second (720p) session would not open while a 4K60 session was live: %v", err)
	}
	t.Logf("720p session: encoder=%q drive=%s degrade=%q", s720.Name(), s720.Drive(), s720.DegradeReason())
	aus720, done720 := drainSession(s720)
	small := make([]byte, 1280*720*4)
	var encErr error
	for i := 0; i < 60; i++ {
		for p := 0; p < len(small); p += 4 {
			small[p], small[p+1], small[p+3] = byte(p/4+i*13), byte(i*5), 255
		}
		if encErr = s720.Encode(small, int64(i)*33_333_333); encErr != nil {
			break
		}
	}
	time.Sleep(300 * time.Millisecond)
	st720, st4k := s720.Stats(), s4k.Stats()
	close(stop4k)
	<-feed4kDone
	s720.Close()
	s4k.Close()
	<-done720
	<-done4k
	alive, tail, fails := childAliveAfter(0)
	t.Logf("720p: aus=%d busyDrops=%d degrade=%q | 4K: aus=%d busyDrops=%d | child alive=%v fails=%d",
		*aus720, st720.BusyDrops, st720.DegradeReason, *aus4k, st4k.BusyDrops, alive, fails)
	if encErr != nil {
		t.Errorf("720p Encode failed with a 4K60 session live (the field failure): %v; child stderr tail: %s", encErr, tail)
	}
	if *aus720 < 10 {
		t.Errorf("720p session produced aus=%d while a 4K60 session was live, want >=10", *aus720)
	}
	if !alive {
		t.Errorf("encoder child died with 4K60 + 720p concurrent (the field AV); stderr tail: %s", tail)
	}
}

// TestProcFourSessionsOneChild pushes past two: the child multiplexes N sessions by design, and
// "two routes at once" is a normal thing to want, so four must not be special either.
func TestProcFourSessionsOneChild(t *testing.T) {
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	if os.Getenv("RAVE_MATE_MFENC_SOAK") == "" {
		t.Skip("set RAVE_MATE_MFENC_SOAK=1 (four concurrent encode sessions saturate a laptop GPU)")
	}
	requireEncExe(t)
	resetChildren(t)
	runs := runConcurrent(t, [][2]int{{1280, 720}, {854, 480}, {640, 360}, {320, 240}}, 30)
	alive, tail, _ := childAliveAfter(0)
	for i, r := range runs {
		t.Logf("session %d: aus=%d bytes=%d err=%v", i+1, r.aus, r.bytes, r.encErr)
		if r.encErr != nil {
			t.Errorf("session %d of 4: %v", i+1, r.encErr)
		}
	}
	if !alive {
		t.Errorf("child died with four concurrent sessions; stderr tail: %s", tail)
	}
}

// resetChildren tears down every cached child so the next open spawns a fresh one (env-policy
// changes are read at child startup) and restores the pool afterwards.
func resetChildren(t *testing.T) {
	t.Helper()
	drop := func() {
		childMu.Lock()
		old := children
		children = map[int64]*procChild{}
		childMu.Unlock()
		for _, c := range old {
			_ = c.send(map[string]any{"op": "quit"})
			c.mu.Lock()
			cmd := c.cmd
			c.cmd = nil // orphan this incarnation: its wait() must not respawn or poison
			c.dead = true
			c.mu.Unlock()
			if cmd != nil && cmd.Process != nil {
				go func() { _, _ = cmd.Process.Wait() }()
			}
		}
	}
	drop()
	t.Cleanup(func() {
		drop()
		ResetPoison()
	})
}
