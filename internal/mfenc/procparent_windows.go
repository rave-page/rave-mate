//go:build windows && cgo

package mfenc

// procparent: supervisor + shared-memory client for the Zig per-adapter encoder child
// (native/zigenc, rave-mate-enc.exe). The 4K60 field crash proved vendor MFTs can fault
// on THEIR OWN worker threads - unguardable in-process - so the media child NEVER runs
// the MF pipeline in-process: sessions live in the child; a driver AV kills only it. The
// parent observes the exit, reports which sessions died, restarts with bounded backoff
// (RestartPolicy plug - Phase 2 re-places sessions on another adapter), and only after
// N consecutive fast-fails poisons the (adapter, geometry) tuple.
//
// Wire contract (must mirror native/zigenc/src/main.zig):
//   control stdin JSON: open/close/bitrate/idr/quit; stdout events hello/opened/closed.
//   data per session: named shm "Local\rvmfenc-<pid>-<sid>" - 256 B header
//   (8 frameSeq | 16 framePTS ns | 24 consSeq | 32 auWrite | 40 auRead | 48 auDropped |
//   56 encBusyNs), RGBA frame slot, AU byte ring (u32 len | u32 flags | i64 pts | data,
//   8-aligned; len 0xFFFFFFFF = wrap; tail<16 = implicit wrap). Events <shm>-f/-c/-a.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"rave.page/mate/internal/sysexec"
)

// Warnf receives async supervisor warnings (child crashes, poisons). Package-level seam:
// mediapipe points it at the logbus.
var Warnf = func(format string, args ...any) {}

const (
	shmHdrSize     = 256
	wrapMarker     = 0xFFFFFFFF
	openWait       = 8 * time.Second
	encodeWait     = 2 * time.Second
	crashWindow    = 30 * time.Second
	maxConsecFails = 3
	latWindow      = 512
)

// SessionInfo names a session for crash reports + the restart policy.
type SessionInfo struct {
	SID                  uint32
	LUID                 int64
	InW, InH, OutW, OutH int
	FPS                  float64
}

// RestartDecision is the policy verdict after an encoder-child crash.
type RestartDecision struct {
	Retry bool
	LUID  int64 // adapter for the respawn (Phase 2: re-place on another adapter)
}

// RestartPolicy decides where a crashed child's sessions go. Default: same adapter,
// bounded consecutive attempts. The Phase-2 load governor replaces this plug.
var RestartPolicy = func(luid int64, consecFails int, died []SessionInfo) RestartDecision {
	if consecFails >= maxConsecFails {
		return RestartDecision{}
	}
	return RestartDecision{Retry: true, LUID: luid}
}

// ── poison cache (per-process): (adapter, geometry) tuples that crashed the child
// maxConsecFails times in a row. Routes on them go to the ffmpeg engine.
var (
	poisonMu sync.Mutex
	poisoned = map[geomKey]string{}
)

type geomKey struct {
	luid                 int64
	inW, inH, outW, outH int
	fpsN, fpsD           int
}

func poisonKey(s *ProcSession) geomKey {
	fpsN, fpsD := fpsRational(s.fps)
	return geomKey{s.child.luid, s.inW, s.inH, s.outW, s.outH, fpsN, fpsD}
}

// ── shm region ──

type shmRegion struct {
	name    string
	mapping windows.Handle
	base    unsafe.Pointer // OS file-mapping view, NOT Go heap (see createShm)
	baseRaw uintptr        // for UnmapViewOfFile
	size    int
	evFrame windows.Handle
	evCons  windows.Handle
	evAU    windows.Handle
}

func (r *shmRegion) u64(off uintptr) *uint64 { return (*uint64)(unsafe.Add(r.base, off)) }
func (r *shmRegion) i64(off uintptr) *int64  { return (*int64)(unsafe.Add(r.base, off)) }

func (r *shmRegion) close() {
	if r.baseRaw != 0 {
		_ = windows.UnmapViewOfFile(r.baseRaw)
	}
	for _, h := range []windows.Handle{r.mapping, r.evFrame, r.evCons, r.evAU} {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}
}

func createShm(name string, size int) (*shmRegion, error) {
	r := &shmRegion{name: name, size: size}
	n16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	r.mapping, err = windows.CreateFileMapping(windows.InvalidHandle, nil, windows.PAGE_READWRITE,
		uint32(uint64(size)>>32), uint32(uint64(size)&0xFFFFFFFF), n16)
	if err != nil {
		return nil, fmt.Errorf("CreateFileMapping: %w", err)
	}
	r.baseRaw, err = windows.MapViewOfFile(r.mapping, windows.FILE_MAP_WRITE|windows.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		r.close()
		return nil, fmt.Errorf("MapViewOfFile: %w", err)
	}
	// uintptr→Pointer is safe here: the address is an OS file-mapping view (never moves,
	// not GC-managed), converted ONCE at the syscall boundary; all field access derives
	// from base via unsafe.Add.
	r.base = *(*unsafe.Pointer)(unsafe.Pointer(&r.baseRaw)) //nolint:govet // OS mapping, not a Go pointer
	for i, suffix := range []string{"-f", "-c", "-a"} {
		e16, err := windows.UTF16PtrFromString(name + suffix)
		if err != nil {
			r.close()
			return nil, err
		}
		h, err := windows.CreateEvent(nil, 0 /*auto-reset*/, 0, e16)
		if err != nil {
			r.close()
			return nil, fmt.Errorf("CreateEvent: %w", err)
		}
		switch i {
		case 0:
			r.evFrame = h
		case 1:
			r.evCons = h
		default:
			r.evAU = h
		}
	}
	return r, nil
}

// ── encoder child ──

type openedEv struct {
	Ev   string `json:"ev"`
	SID  uint32 `json:"sid"`
	OK   bool   `json:"ok"`
	Err  string `json:"err"`
	Name string `json:"name"`
	Bgra bool   `json:"bgra"`
}

type procChild struct {
	luid int64

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	proc       windows.Handle // PROCESS_QUERY_LIMITED_INFORMATION for CPU sampling
	sessions   map[uint32]*ProcSession
	openWait   map[uint32]chan openedEv
	closeWait  map[uint32]chan struct{}
	dead       bool
	stateCh    chan struct{} // closed+replaced on every liveness transition (spawn/death/loop)
	spawnCount int
	consecFail int
	lastSpawn  time.Time
	restarts   int
	stderrTail []byte // bounded crash-forensics tail (stage traces name the faulting call)
	// CPU sampling state
	lastCPU  time.Duration
	lastWall time.Time
	cpuPct   float64
}

var (
	childMu  sync.Mutex
	children = map[int64]*procChild{}
	sidSeq   atomic.Uint32
)

// encExePath locates rave-mate-enc.exe: RAVE_MATE_ENC_EXE override, else beside our exe,
// else the in-repo zig-out (dev/test runs).
func encExePath() (string, error) {
	if p := os.Getenv("RAVE_MATE_ENC_EXE"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "rave-mate-enc.exe")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			p := filepath.Join(d, "native", "zigenc", "zig-out", "bin", "rave-mate-enc.exe")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return "", errors.New("rave-mate-enc.exe not found (build native/zigenc or set RAVE_MATE_ENC_EXE)")
}

func getChild(luid int64) (*procChild, error) {
	childMu.Lock()
	defer childMu.Unlock()
	if c, ok := children[luid]; ok {
		if c.isDeadLocked() {
			return nil, fmt.Errorf("mfenc: encoder child for adapter %#x is crash-looping (%d consecutive fails) - using ffmpeg", uint64(luid), maxConsecFails)
		}
		return c, nil
	}
	c := &procChild{luid: luid, sessions: map[uint32]*ProcSession{}, openWait: map[uint32]chan openedEv{},
		closeWait: map[uint32]chan struct{}{}, stateCh: make(chan struct{})}
	if err := c.spawn(); err != nil {
		return nil, err
	}
	children[luid] = c
	return c, nil
}

func (c *procChild) isDeadLocked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead && c.consecFail >= maxConsecFails
}

// signalState wakes waitUsable waiters after a liveness transition. Caller holds c.mu.
func (c *procChild) signalState() {
	close(c.stateCh)
	c.stateCh = make(chan struct{})
}

// waitUsable blocks until the child is alive (nil), crash-looped (error), or the deadline
// passes - so an open landing during a restart backoff WAITS for the respawn instead of
// burning its attempts in microseconds (route survival beats a spurious ffmpeg fallback).
func (c *procChild) waitUsable(d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		c.mu.Lock()
		if !c.dead {
			c.mu.Unlock()
			return nil
		}
		if c.consecFail >= maxConsecFails {
			c.mu.Unlock()
			return fmt.Errorf("mfenc: encoder child (adapter %#x) is crash-looping - using ffmpeg", uint64(c.luid))
		}
		ch := c.stateCh
		c.mu.Unlock()
		rem := time.Until(deadline)
		if rem <= 0 {
			return errors.New("mfenc: encoder child not back within the respawn window")
		}
		tm := time.NewTimer(rem)
		select {
		case <-ch:
			tm.Stop()
		case <-tm.C:
			return errors.New("mfenc: encoder child not back within the respawn window")
		}
	}
}

// spawn launches the child (caller need not hold c.mu on first spawn; restarts hold it).
func (c *procChild) spawn() error {
	exe, err := encExePath()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, strconv.FormatInt(c.luid, 10))
	cmd.Env = append(os.Environ(), "RAVE_MATE_MFENC_TRACE=1") // stage breadcrumbs → crash forensics
	if v := os.Getenv("RAVE_MATE_MFENC_TEST_FAULT_FIRST"); v != "" && c.spawnCount == 0 {
		// Test knob: FIRST spawn crashes after v encoded frames (field failure mode = driver
		// AV mid-route); the respawn runs clean, proving route continuity by execution.
		cmd.Env = append(cmd.Env, "RAVE_MATE_MFENC_FAULT_AFTER_FRAMES="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	sysexec.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobRealtime) // media-child death reaps encoders
	c.cmd = cmd
	c.stdin = stdin
	c.dead = false
	c.signalState() // spawn/restart callers hold c.mu (first spawn: fresh struct, no waiters yet)
	c.spawnCount++
	c.lastSpawn = time.Now()
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid)); err == nil {
		c.proc = h
	}
	c.stderrTail = nil
	go c.readEvents(stdout)
	go c.readStderr(stderr)
	go c.wait(cmd)
	return nil
}

func (c *procChild) readEvents(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024)
	for sc.Scan() {
		var ev openedEv
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Ev {
		case "opened":
			c.mu.Lock()
			ch := c.openWait[ev.SID]
			delete(c.openWait, ev.SID)
			c.mu.Unlock()
			if ch != nil {
				ch <- ev
			}
		case "closed":
			c.mu.Lock()
			ch := c.closeWait[ev.SID]
			delete(c.closeWait, ev.SID)
			c.mu.Unlock()
			if ch != nil {
				close(ch)
			}
		}
	}
}

func (c *procChild) readStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.stderrTail = append(c.stderrTail, buf[:n]...)
			if len(c.stderrTail) > 8192 {
				c.stderrTail = c.stderrTail[len(c.stderrTail)-8192:]
			}
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (c *procChild) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead || c.stdin == nil {
		return errors.New("mfenc: encoder child down")
	}
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// wait supervises one child incarnation: crash → report + policy → respawn + session
// reopen, or poison after maxConsecFails.
func (c *procChild) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	c.mu.Lock()
	if c.cmd != cmd { // superseded incarnation
		c.mu.Unlock()
		return
	}
	c.dead = true
	c.signalState()
	if c.proc != 0 {
		_ = windows.CloseHandle(c.proc)
		c.proc = 0
	}
	// fail any in-flight open waiters
	for sid, ch := range c.openWait {
		delete(c.openWait, sid)
		ch <- openedEv{SID: sid, OK: false, Err: "encoder child died during open"}
	}
	live := make([]*ProcSession, 0, len(c.sessions))
	died := make([]SessionInfo, 0, len(c.sessions))
	for _, s := range c.sessions {
		s.recovering.Store(true) // drop frames until re-placed - the route must not stall
		live = append(live, s)
		died = append(died, SessionInfo{SID: s.sid, LUID: c.luid, InW: s.inW, InH: s.inH, OutW: s.outW, OutH: s.outH, FPS: s.fps})
	}
	if time.Since(c.lastSpawn) < crashWindow {
		c.consecFail++
	} else {
		c.consecFail = 1
	}
	fails := c.consecFail
	tail := string(c.stderrTail)
	c.mu.Unlock()
	if len(live) == 0 && err == nil {
		return // clean exit with no sessions (quit)
	}
	Warnf("mfenc: encoder child (adapter %#x) exited: %v - %d session(s) affected, consecutive fails %d; stderr tail: %s",
		uint64(c.luid), err, len(live), fails, tail)

	dec := RestartPolicy(c.luid, fails, died)
	if !dec.Retry {
		for _, s := range live {
			k := poisonKey(s)
			poisonMu.Lock()
			poisoned[k] = fmt.Sprintf("encoder child crashed %d times (adapter %#x, %dx%d->%dx%d@%g)",
				fails, uint64(c.luid), s.inW, s.inH, s.outW, s.outH, s.fps)
			poisonMu.Unlock()
			s.fail("mfenc: encoder child crash limit reached")
		}
		Warnf("mfenc: adapter %#x poisoned after %d consecutive crashes - affected geometries fall back to ffmpeg", uint64(c.luid), fails)
		c.mu.Lock()
		c.signalState() // wake waitUsable: crash-loop verdict is final for this child
		c.mu.Unlock()
		return
	}
	backoff := time.Duration(500*(1<<uint(fails-1))) * time.Millisecond
	if backoff > 2*time.Second {
		backoff = 2 * time.Second
	}
	time.Sleep(backoff)
	c.mu.Lock()
	if err := c.spawn(); err != nil {
		c.consecFail = maxConsecFails // no binary to respawn: final for this child
		c.signalState()
		c.mu.Unlock()
		for _, s := range live {
			s.fail("mfenc: encoder child respawn failed: " + err.Error())
		}
		return
	}
	c.restarts++
	c.mu.Unlock()
	// re-place every session in the new incarnation (same shm - it survives the child)
	for _, s := range live {
		if s.closed.Load() {
			continue
		}
		if err := c.reopenSession(s); err != nil {
			s.fail("mfenc: session reopen after crash failed: " + err.Error())
			continue
		}
		s.recovering.Store(false)
		_ = c.send(map[string]any{"op": "idr", "sid": s.sid}) // receiver needs a fresh IDR
	}
}

type openCmd struct {
	Op   string `json:"op"`
	SID  uint32 `json:"sid"`
	Shm  string `json:"shm"`
	InW  int    `json:"in_w"`
	InH  int    `json:"in_h"`
	OutW int    `json:"out_w"`
	OutH int    `json:"out_h"`
	FpsN int    `json:"fps_n"`
	FpsD int    `json:"fps_d"`
	Kbps int    `json:"kbps"`
	Gop  int    `json:"gop"`
}

func (c *procChild) openSession(s *ProcSession) (openedEv, error) {
	ch := make(chan openedEv, 1)
	c.mu.Lock()
	c.openWait[s.sid] = ch
	c.mu.Unlock()
	fpsN, fpsD := fpsRational(s.fps)
	err := c.send(openCmd{Op: "open", SID: s.sid, Shm: s.shm.name, InW: s.inW, InH: s.inH,
		OutW: s.outW, OutH: s.outH, FpsN: fpsN, FpsD: fpsD, Kbps: s.kbps, Gop: s.gop})
	if err != nil {
		return openedEv{}, err
	}
	select {
	case ev := <-ch:
		return ev, nil
	case <-time.After(openWait):
		c.mu.Lock()
		if ch2, ok := c.openWait[s.sid]; ok && ch2 == ch {
			delete(c.openWait, s.sid) // prune our waiter (readEvents may have raced the delete)
		}
		c.mu.Unlock()
		select {
		case ev := <-ch: // late event landed between timeout and prune - use it
			return ev, nil
		default:
		}
		return openedEv{}, errors.New("open timeout")
	}
}

func (c *procChild) reopenSession(s *ProcSession) error {
	ev, err := c.openSession(s)
	if err != nil {
		return err
	}
	if !ev.OK {
		return errors.New(ev.Err)
	}
	return nil
}

// ── ProcSession: one encode session (medialink-facing surface) ──

type ProcSession struct {
	child *procChild
	sid   uint32
	shm   *shmRegion

	inW, inH, outW, outH int
	fps                  float64
	kbps, gop            int
	frameBytes           int
	ringSize             uint64
	ringOff              uintptr

	name       string
	bgra       bool
	out        chan AU
	done       chan struct{}
	pumpDone   chan struct{}
	closed     atomic.Bool
	recovering atomic.Bool   // child died; frames dropped until the session is re-placed
	droppedAUs atomic.Uint64 // mirror of the shm counter (safe to read after Close)

	failMu  sync.Mutex
	failErr error

	// perf (per session)
	pmu       sync.Mutex
	submitAt  map[int64]time.Time // pts ns → submit time
	lat       [latWindow]float64  // ms, ring
	latN      int
	latIdx    int
	submitted uint64
	received  uint64
}

// OpenProcSession opens one native encode session on the (supervised, per-adapter) Zig
// encoder child. Errors are CLEAN - the caller falls back to the ffmpeg engine.
func OpenProcSession(luid int64, inW, inH, outW, outH int, fps float64, kbps, gop int) (*ProcSession, error) {
	if os.Getenv("RAVE_MATE_MFENC_OPEN_FAIL") != "" {
		return nil, errors.New("mfenc: native open disabled (RAVE_MATE_MFENC_OPEN_FAIL)")
	}
	if fps <= 0 {
		fps = 30
	}
	fpsN, fpsD := fpsRational(fps)
	k := geomKey{luid, inW, inH, outW, outH, fpsN, fpsD}
	poisonMu.Lock()
	reason, bad := poisoned[k]
	poisonMu.Unlock()
	if bad {
		return nil, errors.New("mfenc: poisoned tuple - " + reason + " - using ffmpeg")
	}

	frameBytes := inW * inH * 4
	ringSize := 8 << 20
	if frameBytes > ringSize {
		ringSize = frameBytes
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ { // one respawned-child retry (waitUsable spans the backoff)
		child, err := getChild(luid)
		if err != nil {
			return nil, err
		}
		// A crash may have JUST happened: wait out the respawn backoff (max 2s) + spawn
		// instead of burning both attempts in microseconds against a dead child.
		if err := child.waitUsable(3 * time.Second); err != nil {
			return nil, err
		}
		sid := sidSeq.Add(1)
		shm, err := createShm(fmt.Sprintf(`Local\rvmfenc-%d-%d`, os.Getpid(), sid), shmHdrSize+frameBytes+ringSize)
		if err != nil {
			return nil, err
		}
		s := &ProcSession{
			child: child, sid: sid, shm: shm,
			inW: inW, inH: inH, outW: outW, outH: outH, fps: fps, kbps: kbps, gop: gop,
			frameBytes: frameBytes, ringSize: uint64(ringSize),
			ringOff:  uintptr(shmHdrSize + frameBytes),
			out:      make(chan AU, 8),
			done:     make(chan struct{}),
			pumpDone: make(chan struct{}),
			submitAt: map[int64]time.Time{},
		}
		ev, err := child.openSession(s)
		if err != nil {
			shm.close()
			lastErr = err
			continue // child likely died (crash path counts it); retry once on the respawn
		}
		if !ev.OK {
			shm.close()
			return nil, errors.New("mfenc: native open failed: " + ev.Err) // clean refusal: no poison, no retry
		}
		s.name = ev.Name
		s.bgra = ev.Bgra
		child.mu.Lock()
		child.sessions[sid] = s
		child.mu.Unlock()
		go s.pump()
		return s, nil
	}
	return nil, fmt.Errorf("mfenc: session open failed: %w", lastErr)
}

func (s *ProcSession) fail(msg string) {
	s.failMu.Lock()
	if s.failErr == nil {
		s.failErr = errors.New(msg)
	}
	s.failMu.Unlock()
}

func (s *ProcSession) failed() error {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.failErr
}

// Name returns the encoder MFT friendly name.
func (s *ProcSession) Name() string { return s.name }

// InputIsBGRA reports the child's negotiated upload order (diagnostic).
func (s *ProcSession) InputIsBGRA() bool { return s.bgra }

// Output yields encoded AUs; closes after Close.
func (s *ProcSession) Output() <-chan AU { return s.out }

// ForceKeyframe requests a live IDR (no reopen).
func (s *ProcSession) ForceKeyframe() {
	_ = s.child.send(map[string]any{"op": "idr", "sid": s.sid})
}

// SetBitrate live-retargets CBR bitrate (Phase-2 degrade ladder).
func (s *ProcSession) SetBitrate(kbps int) {
	if kbps > 0 {
		_ = s.child.send(map[string]any{"op": "bitrate", "sid": s.sid, "kbps": kbps})
	}
}

// Encode submits one RGBA frame. During a child restart frames are DROPPED (nil error)
// so the route survives the crash; a poisoned/dead session returns an error.
func (s *ProcSession) Encode(rgba []byte, ptsNs int64) error {
	if s.closed.Load() {
		return errors.New("mfenc: session closed")
	}
	if err := s.failed(); err != nil {
		return err
	}
	if len(rgba) < s.frameBytes {
		return fmt.Errorf("mfenc: short frame %d < %d", len(rgba), s.frameBytes)
	}
	if s.recovering.Load() {
		return nil // restart in progress: drop the frame, keep the route
	}
	s.child.mu.Lock()
	dead := s.child.dead
	s.child.mu.Unlock()
	if dead {
		return nil // crash observed before recovery marking: same drop
	}
	// frame slot write → pts → seq (release) → signal
	dst := unsafe.Slice((*byte)(unsafe.Add(s.shm.base, shmHdrSize)), s.frameBytes)
	copy(dst, rgba[:s.frameBytes])
	*s.shm.i64(16) = ptsNs
	seq := atomic.AddUint64(s.shm.u64(8), 1)
	s.pmu.Lock()
	s.submitted++
	if len(s.submitAt) < 1024 {
		s.submitAt[ptsNs-ptsNs%100] = time.Now() // MF quantizes pts to 100 ns units
	}
	s.pmu.Unlock()
	_ = windows.SetEvent(s.shm.evFrame)
	deadline := time.Now().Add(encodeWait)
	for {
		_, _ = windows.WaitForSingleObject(s.shm.evCons, 100)
		if atomic.LoadUint64(s.shm.u64(24)) >= seq {
			return nil
		}
		if s.closed.Load() {
			return errors.New("mfenc: session closed")
		}
		if s.recovering.Load() {
			return nil // crashed mid-frame: dropped, supervisor is re-placing us
		}
		s.child.mu.Lock()
		dead = s.child.dead
		s.child.mu.Unlock()
		if dead {
			return nil // crashed mid-frame: dropped, supervisor is restarting
		}
		if time.Now().After(deadline) {
			return errors.New("mfenc: encode timeout")
		}
	}
}

// pump drains the AU ring into the Output channel and samples latency. On Close it does
// one final drain (tail AUs), then signals pumpDone so the shm can be unmapped safely.
func (s *ProcSession) pump() {
	defer close(s.pumpDone)
	defer close(s.out)
	final := false
	for {
		select {
		case <-s.done:
			if final {
				return
			}
			final = true // one last drain below, then out
		default:
		}
		if !final {
			_, _ = windows.WaitForSingleObject(s.shm.evAU, 200)
		}
		for {
			r := atomic.LoadUint64(s.shm.u64(40))
			w := atomic.LoadUint64(s.shm.u64(32))
			if r >= w {
				break
			}
			tail := s.ringSize - (r % s.ringSize)
			if tail < 16 {
				atomic.StoreUint64(s.shm.u64(40), r+tail)
				continue
			}
			pos := uintptr(r % s.ringSize)
			ln := atomic.LoadUint32((*uint32)(unsafe.Add(s.shm.base, s.ringOff+pos)))
			if ln == wrapMarker {
				atomic.StoreUint64(s.shm.u64(40), r+tail)
				continue
			}
			flags := *(*uint32)(unsafe.Add(s.shm.base, s.ringOff+pos+4))
			pts := *(*int64)(unsafe.Add(s.shm.base, s.ringOff+pos+8))
			data := make([]byte, ln)
			copy(data, unsafe.Slice((*byte)(unsafe.Add(s.shm.base, s.ringOff+pos+16)), ln))
			rec := 16 + ((uint64(ln) + 7) &^ 7)
			atomic.StoreUint64(s.shm.u64(40), r+rec)
			s.droppedAUs.Store(atomic.LoadUint64(s.shm.u64(48)))
			s.sampleLatency(pts)
			au := AU{Data: data, PTSNs: pts, Keyframe: flags&1 != 0}
			// Delivery policy: block (bounded) for a live consumer, but NEVER stall once
			// teardown began - Close() waits on pumpDone before unmapping the shm, so any
			// stall here would either delay Close or (worse) race a UAF. During teardown
			// (final drain, session closed, or done fired mid-wait) undeliverable AUs are
			// discarded immediately.
			if final || s.closed.Load() {
				select {
				case s.out <- au:
				default: // no reader during teardown: discard
				}
				continue
			}
			select {
			case s.out <- au:
			default:
				tm := time.NewTimer(2 * time.Second)
				select {
				case s.out <- au:
					tm.Stop()
				case <-s.done: // teardown began while blocked: discard, drain fast
					tm.Stop()
				case <-tm.C: // consumer wedged outside teardown: drop rather than stall shm
				}
			}
		}
		if final {
			return
		}
	}
}

func (s *ProcSession) sampleLatency(pts int64) {
	s.pmu.Lock()
	defer s.pmu.Unlock()
	s.received++
	t, ok := s.submitAt[pts]
	if !ok {
		return
	}
	delete(s.submitAt, pts)
	ms := float64(time.Since(t).Microseconds()) / 1000
	s.lat[s.latIdx] = ms
	s.latIdx = (s.latIdx + 1) % latWindow
	if s.latN < latWindow {
		s.latN++
	}
}

// Stats reports per-session telemetry (p99 rise = early saturation signal for Phase 2).
func (s *ProcSession) Stats() ProcStats {
	s.pmu.Lock()
	n := s.latN
	var tmp []float64
	if n > 0 {
		tmp = make([]float64, n)
		copy(tmp, s.lat[:n])
	}
	depth := int(s.submitted) - int(s.received)
	s.pmu.Unlock()
	st := ProcStats{Name: s.name, QueueDepth: depth, DroppedAUs: s.droppedAUs.Load()}
	if n > 0 {
		sort.Float64s(tmp)
		st.LatP50Ms = tmp[n/2]
		st.LatP99Ms = tmp[(n*99)/100]
	}
	s.child.mu.Lock()
	st.Restarts = s.child.restarts
	s.child.mu.Unlock()
	st.ChildCPUPct = s.child.cpuPercent()
	return st
}

// cpuPercent samples the child's CPU usage since the previous call (all sessions).
func (c *procChild) cpuPercent() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == 0 {
		return 0
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(c.proc, &creation, &exit, &kernel, &user); err != nil {
		return c.cpuPct
	}
	toDur := func(ft windows.Filetime) time.Duration {
		return time.Duration((int64(ft.HighDateTime)<<32 | int64(ft.LowDateTime)) * 100)
	}
	total := toDur(kernel) + toDur(user)
	now := time.Now()
	if !c.lastWall.IsZero() {
		wall := now.Sub(c.lastWall)
		if wall > 0 {
			c.cpuPct = 100 * float64(total-c.lastCPU) / float64(wall)
		}
	}
	c.lastCPU = total
	c.lastWall = now
	return c.cpuPct
}

// Close ends the session (drains the child side) and releases the shm.
func (s *ProcSession) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	closedCh := make(chan struct{})
	s.child.mu.Lock()
	s.child.closeWait[s.sid] = closedCh
	delete(s.child.sessions, s.sid)
	s.child.mu.Unlock()
	if s.child.send(map[string]any{"op": "close", "sid": s.sid}) == nil {
		select { // child drains its tail AUs into the ring, then emits "closed"
		case <-closedCh:
		case <-time.After(3 * time.Second):
		}
	}
	close(s.done)
	_ = windows.SetEvent(s.shm.evAU) // wake the pump for its final drain
	// UNCONDITIONAL: the shm must never be unmapped while pump can still touch it. pump's
	// exit is bounded by construction (waits <=200ms, ring drain finite, teardown delivery
	// never blocks), so this converges promptly; a timeout fallback here would be a UAF.
	<-s.pumpDone
	s.shm.close()
}
