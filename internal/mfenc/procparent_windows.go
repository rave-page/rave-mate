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
//   control stdin JSON: open/close/bitrate/idr/quit; stdout events
//   hello/opened/srcgone/closed.
//   data per session: named shm "Local\rvmfenc-<pid>-<sid>" - 256 B header
//   (0 magic 'RMF2' | 4 ver | 8 frameSeq | 16 framePTS ns | 24 consSeq | 32 auWrite |
//   40 auRead | 48 auDropped | 56 encBusyNs | 64 capFrames | 72 capSkips | 80 mtxTimeouts |
//   88 srcErrors | 96 lastCapNs | 104 capFmt | 108 capFlags), RGBA frame slot, AU byte ring
//   (u32 len | u32 flags | i64 pts | data, 8-aligned; len 0xFFFFFFFF = wrap; tail<16 =
//   implicit wrap). Events <shm>-f/-c/-a.
//
// Zero-copy sessions (src:"spout", zigmedia inc 1) carry NO frame slot: the child opens the
// sender's GPU shared texture itself, so the shm is header + AU ring only (66.4 → 4.0 MB per
// 4K session) and Go moves scalars, never pixels. Requires child hello.ver >= 2.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	shmHdrSize = 256
	wrapMarker = 0xFFFFFFFF
	openWait   = 8 * time.Second
	// encodeWait MUST stay comfortably above the child's per-frame budget (SUBMIT_WAIT_MS = 250 ms
	// in native/zigenc, twice per frame worst case). Both ends used to sit at 2 s, so the parent's
	// deadline expired at the same instant the child's did and a merely SATURATED encoder ended the
	// route with "mfenc: encode timeout" instead of dropping a frame. The child now always answers
	// first; reaching this deadline means the child is genuinely wedged or gone.
	encodeWait = 4 * time.Second
	// maxConsecFails is the crash streak that poisons an (adapter, encoder) pair. The streak is
	// measured CRASH TO CRASH in the ledger (procfail_windows.go); the old "within 30 s of the
	// last spawn" window reset it on every route, so it never reached this number.
	maxConsecFails = 3
	latWindow      = 512

	// Header v2 (additive; v1 used 0-63 only). Parent stamps magic+ver; the child refuses a
	// zero-copy session on anything below ver 2, because that is the one layout where it would
	// otherwise size the mapping from in_w*in_h*4 and map past the parent's smaller mapping.
	shmMagic      = 0x32464D52 // 'RMF2'
	shmVer        = 2
	protoVerZeroC = 2 // minimum child hello.ver that may be asked for src:"spout"

	// Zero-copy capture counters, child-written (design §3.2).
	offCapFrames  = 64
	offCapSkips   = 72
	offMtxTimeout = 80
	offSrcErrors  = 88
	offLastCapNs  = 96
	offCapFmt     = 104
	offCapFlags   = 108

	// Crash attribution (child-written, read AFTER the child dies). A vendor AV happens inside a
	// driver, often on its own worker thread: there is no usable stderr tail and no Go stack, which
	// is why the field AMD 0xc0000005 was unattributed. The child latches the stage it is about to
	// enter into shared memory, so the corpse leaves a breadcrumb behind. Mirrors native/zigenc
	// off_stage/off_feed_rc/off_feed_fails.
	offStage     = 112
	offFeedRC    = 116
	offFeedFails = 120
	offBusyDrops = 124 // frames the encoder had no credit for in time (saturation, not breakage)

	// AU ring for a zero-copy session: half a second of bitstream, floor 4 MiB, ceiling
	// 16 MiB - GEOMETRY-INDEPENDENT, so a sender resize costs zero SHM realloc.
	ringKBMin = 4 * 1024
	ringKBMax = 16 * 1024

	// R1 (stale share handle = silently frozen picture): a sender restart can leave
	// OpenSharedResource succeeding on a DEAD texture whose content never changes, so the route
	// looks healthy and ships a still frame. Oracle = the child's lastCapNs going stale plus a
	// share-handle comparison on the registry rescan.
	spoutWatchEvery  = 2 * time.Second
	spoutStaleAfter  = 3 * time.Second
	spoutMaxRecycles = 3 // then the sender is pinned to the readback path (§7.3)
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

// Crash-loop accounting lives in the (adapter, encoder) failure LEDGER - procfail_windows.go.
// It replaced a per-(adapter, geometry) poison map keyed on the wrong thing and driven by a
// counter that reset on every route (see that file's header for the three defects).

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
	// Stamp the header identity BEFORE the child can open it: a zero-copy session refuses to map
	// anything until it reads magic + ver >= 2 (never guess a layout).
	*(*uint32)(r.base) = shmMagic
	*(*uint32)(unsafe.Add(r.base, 4)) = shmVer
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
	// Zero-copy verdict + srcgone reason ride the same decoder (one line = one event).
	Src    string `json:"src"`
	Cap    string `json:"cap"`
	ErrSrc string `json:"err_src"`
	Reason string `json:"reason"`
	Ver    uint32 `json:"ver"`
	// dir:"dec" (zigmedia inc 2): which direction the session got + why a destination was refused.
	Dir    string `json:"dir"`
	ErrDst string `json:"err_dst"`
	// Vendor-portability verdict: the drive mode the MFT's own MF_TRANSFORM_ASYNC selected, the
	// software-tier flag, and the adapter the child actually resolved.
	Drive   string `json:"drive"`
	SW      bool   `json:"sw"`
	LUIDRes int64  `json:"luid_res"`
	Adapter string `json:"adapter"`
	// encfail: an ATTRIBUTED mid-route encode failure (rc + the stage it died in).
	RC    int32  `json:"rc"`
	Stage uint32 `json:"stage"`
	Fails uint32 `json:"fails"`
}

// swEncoderKey is the ledger row for "the software tier on this adapter". The concrete MFT name
// varies by Windows build, so the TIER is the unit of poisoning here, not the name.
const swEncoderKey = "\x00software-mf-tier"

// ledgerEncoder maps an opened event onto its ledger row: software sessions all share one row.
func ledgerEncoder(ev openedEv) string {
	if ev.SW {
		return swEncoderKey
	}
	return ev.Name
}

var warnLUIDDriftOnce sync.Once

// ErrZeroCopyRefused is the open-side downgrade rung: the child could not consume the sender's
// shared texture (foreign adapter, exotic/TYPELESS format, geometry moved under us). The caller
// reopens the SAME session on the readback path - never a dead route.
var ErrZeroCopyRefused = errors.New("mfenc: zero-copy source refused")

type procChild struct {
	luid int64

	mu         sync.Mutex
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	proc       windows.Handle // PROCESS_QUERY_LIMITED_INFORMATION for CPU sampling
	sessions   map[uint32]*ProcSession
	decs       map[uint32]*ProcDecSession // dir:"dec" sessions, same child, same supervisor
	openWait   map[uint32]chan openedEv
	closeWait  map[uint32]chan struct{}
	dead       bool
	protoVer   uint32        // child hello.ver (0 = not seen yet); >= 2 may be asked for zero-copy
	helloCh    chan struct{} // closed when this incarnation's hello lands (re-armed per spawn)
	stateCh    chan struct{} // closed+replaced on every liveness transition (spawn/death/loop)
	spawnCount int
	consecFail int // mirror of the ledger streak (waitUsable/isDeadLocked read it)
	lastSpawn  time.Time
	lastEnc    string // last MFT bound in this child: names the ledger row when a crash lands
	// with no session left registered (teardown-time AV)
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

// stagedChildExe extracts the EMBEDDED encoder child to the per-role cache dir
// (%LocalAppData%/rave-mate/proc, the sysexec proc-link convention). The filename is
// version-stamped with the embed's content hash, so a self-updated main exe always runs
// ITS OWN child version (stale on-disk copies are ignored + best-effort pruned). Atomic
// write+rename; re-extract only on hash mismatch. "" when this build has no embed.
var (
	stagedOnce sync.Once
	stagedPath string
	stagedErr  error
)

// HasEmbeddedChild reports whether this build carries the embedded encoder child.
func HasEmbeddedChild() bool { return len(embeddedEnc) > 0 }

func stagedChildExe() (string, error) {
	if len(embeddedEnc) == 0 {
		return "", nil
	}
	stagedOnce.Do(func() {
		sum := sha256.Sum256(embeddedEnc)
		stamp := hex.EncodeToString(sum[:6])
		base, err := os.UserCacheDir()
		if err != nil {
			stagedErr = err
			return
		}
		dir := filepath.Join(base, "rave-mate", "proc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			stagedErr = err
			return
		}
		dst := filepath.Join(dir, "rave-mate-enc-"+stamp+".exe")
		if fi, err := os.Stat(dst); err == nil && fi.Size() == int64(len(embeddedEnc)) {
			stagedPath = dst // hash-stamped name + size match = current version already staged
			return
		}
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, embeddedEnc, 0o755); err != nil {
			stagedErr = err
			return
		}
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			// lost a race with a sibling extract: reuse if it now matches
			if fi, serr := os.Stat(dst); serr == nil && fi.Size() == int64(len(embeddedEnc)) {
				stagedPath = dst
				return
			}
			stagedErr = err
			return
		}
		stagedPath = dst
		if old, err := filepath.Glob(filepath.Join(dir, "rave-mate-enc-*.exe")); err == nil {
			for _, f := range old { // prune superseded versions (in-use ones refuse removal: fine)
				if f != dst {
					_ = os.Remove(f)
				}
			}
		}
	})
	return stagedPath, stagedErr
}

// encExePath locates rave-mate-enc.exe: RAVE_MATE_ENC_EXE override, else the extracted
// EMBED (version-exact, survives self-updates), else beside our exe (NSIS install), else
// the in-repo zig-out (dev/test runs).
func encExePath() (string, error) {
	if p := os.Getenv("RAVE_MATE_ENC_EXE"); p != "" {
		return p, nil
	}
	if p, err := stagedChildExe(); err != nil {
		Warnf("mfenc: embedded child extraction failed: %v", err)
	} else if p != "" {
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

// ChildAvailable reports whether the native engine is ACTUALLY usable end to end:
// hardware caps (cgo probe) AND the encoder child exe resolvable + spawnable + answering
// hello. Advertisement MUST use this, never Available() alone - a self-updated install
// without the child would otherwise negotiate an engine it cannot run (field #166: the
// self-updater swaps only rave-mate.exe, sidecars never arrive).
var (
	childAvailOnce sync.Once
	childAvailOK   bool
)

func ChildAvailable() bool {
	childAvailOnce.Do(func() { childAvailOK = childAvailCheck() })
	return childAvailOK
}

// RefreshChildAvailable re-evaluates the gate (child staged/updated at runtime; tests).
func RefreshChildAvailable() bool {
	childAvailOK = childAvailCheck()
	childAvailOnce = sync.Once{}
	childAvailOnce.Do(func() {})
	return childAvailOK
}

// childAvailCheck is ChildAvailable's uncached core (testable).
func childAvailCheck() bool {
	if !Available() {
		return false
	}
	exe, err := encExePath()
	if err != nil {
		Warnf("mfenc: native engine NOT advertised - encoder child unavailable: %v", err)
		return false
	}
	if err := probeChildHello(exe); err != nil {
		Warnf("mfenc: native engine NOT advertised - encoder child probe failed (%s): %v", exe, err)
		return false
	}
	return true
}

// probeChildHello spawns the child once and requires its hello line within 3s.
func probeChildHello(exe string) error {
	cmd := exec.Command(exe, "0")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	sysexec.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	sysexec.AssignToJob(cmd.Process, true)
	defer func() {
		_, _ = stdin.Write([]byte("{\"op\":\"quit\"}\n"))
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			sysexec.KillTree(cmd.Process)
		}
	}()
	lineCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		if sc.Scan() {
			lineCh <- sc.Text()
		}
		close(lineCh)
	}()
	select {
	case ln, ok := <-lineCh:
		if !ok || !strings.Contains(ln, "\"ev\":\"hello\"") {
			return fmt.Errorf("unexpected first line %q", ln)
		}
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("no hello within 3s")
	}
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
	c := &procChild{luid: luid, sessions: map[uint32]*ProcSession{}, decs: map[uint32]*ProcDecSession{},
		openWait:  map[uint32]chan openedEv{},
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

// waitProtoVer returns the child's protocol version, waiting up to d for its hello line. 0 = no
// hello yet: the caller must NOT request zero-copy (the version gate is the only thing standing
// between a v1 child and a mapping it would size wrong).
func (c *procChild) waitProtoVer(d time.Duration) uint32 {
	c.mu.Lock()
	v, ch := c.protoVer, c.helloCh
	c.mu.Unlock()
	if v != 0 || ch == nil {
		return v
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ch:
	case <-tm.C:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protoVer
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
	c.protoVer = 0
	c.helloCh = make(chan struct{})
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
		case "hello":
			c.mu.Lock()
			c.protoVer = ev.Ver
			ch := c.helloCh
			c.helloCh = nil // one close per incarnation (spawn re-arms it)
			c.mu.Unlock()
			if ch != nil {
				close(ch)
			}
		case "opened":
			c.mu.Lock()
			ch := c.openWait[ev.SID]
			delete(c.openWait, ev.SID)
			c.mu.Unlock()
			if ch != nil {
				ch <- ev
			}
		case "srcgone":
			c.mu.Lock()
			s := c.sessions[ev.SID]
			c.mu.Unlock()
			if s != nil {
				s.onSrcGone(ev.Reason)
			}
		case "encfail":
			// The ENCODER wedged, not the source. Before the child distinguished the two, this
			// arrived as srcgone and burned the zero-copy recycle budget on a healthy sender.
			c.mu.Lock()
			s := c.sessions[ev.SID]
			c.mu.Unlock()
			if s != nil {
				s.onEncFail(ev.RC, ev.Stage, ev.Fails)
			}
		case "dstgone":
			c.mu.Lock()
			d := c.decs[ev.SID]
			c.mu.Unlock()
			if d != nil {
				d.onDstGone(ev.Reason)
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
	died := make([]SessionInfo, 0, len(c.sessions)+len(c.decs))
	// The child's last latched stage NAMES the faulting call. Read it while we still hold c.mu:
	// Close() removes a session from this map BEFORE it unmaps the shm, so anything visible here
	// is guaranteed still mapped (reading after the unlock would race that unmap).
	var stage, encName string
	for _, s := range c.sessions {
		if st := stageName(atomic.LoadUint32((*uint32)(unsafe.Add(s.shm.base, offStage)))); st != "" && stage == "" {
			stage = st
			if rc := atomic.LoadInt32((*int32)(unsafe.Add(s.shm.base, offFeedRC))); rc != 0 {
				stage += fmt.Sprintf(" (last feed rc %d)", rc)
			}
		}
		if encName == "" {
			encName = s.name
		}
		s.recovering.Store(true) // drop frames until re-placed - the route must not stall
		live = append(live, s)
		died = append(died, SessionInfo{SID: s.sid, LUID: c.luid, InW: s.inW, InH: s.inH, OutW: s.outW, OutH: s.outH, FPS: s.fps})
	}
	if encName == "" {
		encName = c.lastEnc // crash during/after teardown: no live session to ask
	}
	// dir:"dec" sessions ride the same supervisor: without this a child crash recovers every send
	// route and silently kills the receive ones.
	liveDec := make([]*ProcDecSession, 0, len(c.decs))
	for _, d := range c.decs {
		d.recovering.Store(true) // drop AUs until re-placed
		liveDec = append(liveDec, d)
		died = append(died, SessionInfo{SID: d.sid, LUID: c.luid, InW: d.inW, InH: d.inH, OutW: d.outW, OutH: d.outH, FPS: d.fps})
	}
	tail := string(c.stderrTail)
	c.mu.Unlock()
	if len(live) == 0 && len(liveDec) == 0 && err == nil {
		return // clean exit with no sessions (quit)
	}
	// Count the crash against (adapter, encoder) in the LEDGER, not against a per-spawn counter,
	// and do it whether or not a session happened to still be registered: an AV during teardown
	// is the same broken driver. This is the line that makes the safety net able to engage at all.
	fails, poisonedNow := NoteCrash(c.luid, encName, stage)
	c.mu.Lock()
	c.consecFail = fails // mirrored for waitUsable/isDeadLocked
	c.mu.Unlock()
	Warnf("mfenc: encoder child (adapter %#x, %s) exited: %v - %d session(s) affected, fails %d/%d in this streak%s; stderr tail: %s",
		uint64(c.luid), encoderLabel(encName), err, len(live)+len(liveDec), fails, failLimit, stageSuffix(stage), tail)

	verdict := RestartPolicy(c.luid, fails, died)
	if !verdict.Retry || poisonedNow {
		reason, _ := PoisonedTuple(c.luid, encName)
		if reason == "" {
			reason = fmt.Sprintf("encoder child crash limit reached on adapter %#x", uint64(c.luid))
		}
		for _, d := range liveDec {
			d.fail("mfenc: " + reason)
		}
		for _, s := range live {
			s.degradeTo(reason)
			s.fail("mfenc: " + reason)
		}
		Warnf("mfenc: adapter %#x + %s poisoned after %d crashes - later routes take the software encode tier (or ffmpeg if that is poisoned too)%s",
			uint64(c.luid), encoderLabel(encName), fails, stageSuffix(stage))
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
		for _, d := range liveDec {
			d.fail("mfenc: encoder child respawn failed: " + err.Error())
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
	// Decode sessions: same re-place, plus a ring reset - whatever bitstream survived the crash is
	// unusable to a fresh decoder without a keyframe, and the route's own PLI machinery asks the
	// peer for one.
	for _, d := range liveDec {
		if d.closed.Load() {
			continue
		}
		atomic.StoreUint64(d.shm.u64(offInRead), 0)
		atomic.StoreUint64(d.shm.u64(offInWrite), 0)
		if ev, err := c.openDecSession(d); err != nil || !ev.OK {
			d.fail("mfenc: decode session reopen after crash failed")
			continue
		}
		d.recovering.Store(false)
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
	// Zero-copy source (omitted entirely on a readback session: absent = v1 behaviour).
	Src    string `json:"src,omitempty"`
	Sh     uint64 `json:"sh,omitempty"`
	SFmt   uint32 `json:"sfmt,omitempty"`
	SName  string `json:"sname,omitempty"`
	CapN   int    `json:"cap_n,omitempty"`
	CapD   int    `json:"cap_d,omitempty"`
	RingKB int    `json:"ring_kb,omitempty"`
	PTS0   int64  `json:"pts0,omitempty"`
	// dir:"dec" receive-side fields (omitted entirely on an encode session: absent = "enc").
	Dir      string `json:"dir,omitempty"`
	Codec    string `json:"codec,omitempty"`
	DSh      uint64 `json:"dsh,omitempty"`
	DFmt     uint32 `json:"dfmt,omitempty"`
	DName    string `json:"dname,omitempty"`
	InRingKB int    `json:"in_ring_kb,omitempty"`
	// SW: take the SOFTWARE encoder tier for this session (our ledger poisoned the hardware MFT
	// on this adapter). Per-session, so one bad adapter never forces software on the others.
	SW bool `json:"sw,omitempty"`
}

func (c *procChild) openSession(s *ProcSession) (openedEv, error) {
	ch := make(chan openedEv, 1)
	c.mu.Lock()
	c.openWait[s.sid] = ch
	c.mu.Unlock()
	fpsN, fpsD := fpsRational(s.fps)
	cmd := openCmd{Op: "open", SID: s.sid, Shm: s.shm.name, InW: s.inW, InH: s.inH,
		OutW: s.outW, OutH: s.outH, FpsN: fpsN, FpsD: fpsD, Kbps: s.kbps, Gop: s.gop, SW: s.swTier}
	if s.zeroCopy {
		// Re-read the handle on every (re)open, including the post-crash re-place: a sender that
		// restarted while the child was down must not be re-issued its dead texture (R1).
		s.refreshHandle()
		h, sfmt, name := s.spoutHandle()
		cmd.Src, cmd.Sh, cmd.SFmt, cmd.SName = "spout", h, sfmt, name
		cmd.CapN, cmd.CapD, cmd.RingKB = fpsN, fpsD, s.ringKB
		cmd.PTS0 = time.Now().UnixNano()
	}
	err := c.send(cmd)
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

// openDecSession sends a dir:"dec" open and waits for its verdict. The destination handle is
// RE-READ on every (re)open, including the post-crash re-place: a sender re-created while the child
// was down must never be handed its dead texture (the receive side's R1).
func (c *procChild) openDecSession(d *ProcDecSession) (openedEv, error) {
	ch := make(chan openedEv, 1)
	c.mu.Lock()
	c.openWait[d.sid] = ch
	c.mu.Unlock()
	fpsN, fpsD := fpsRational(d.fps)
	d.refreshDest()
	h, dfmt, name := d.destHandle()
	codec := "h264"
	if d.hevc {
		codec = "hevc"
	}
	cmd := openCmd{Op: "open", SID: d.sid, Shm: d.shm.name, InW: d.inW, InH: d.inH,
		OutW: d.outW, OutH: d.outH, FpsN: fpsN, FpsD: fpsD,
		Dir: "dec", Codec: codec, DSh: h, DFmt: dfmt, DName: name, InRingKB: d.ringKB}
	if err := c.send(cmd); err != nil {
		return openedEv{}, err
	}
	select {
	case ev := <-ch:
		return ev, nil
	case <-time.After(openWait):
		c.mu.Lock()
		if ch2, ok := c.openWait[d.sid]; ok && ch2 == ch {
			delete(c.openWait, d.sid) // prune our waiter (readEvents may have raced the delete)
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

	// movedFrom is the adapter this session was ASKED for before affinity resolution re-placed it
	// (0 = never moved). Diagnostic only.
	movedFrom int64

	// Zero-copy capture (src:"spout"): Go moves SCALARS only - handle + format + name - and
	// never a pixel. resolve re-reads them from the (cached) sender registry.
	zeroCopy  bool
	ringKB    int
	resolve   func() (handle uint64, dxgiFormat uint32, w, h int, ok bool)
	watchDone chan struct{}

	zcMu       sync.Mutex
	zcHandle   uint64
	zcFmt      uint32
	zcName     string
	zcRecycles int   // reopen attempts spent on a stale/dead source (cap spoutMaxRecycles)
	zcLastCap  int64 // last observed child lastCapNs (staleness oracle)
	zcLastMove time.Time
	// recycle is the srcgone/staleness ACTION seam (tests assert the oracle→action wiring
	// without a live child).
	recycle    func(reason string)
	downgrades atomic.Int32
	capRate    counterRate
	busyRate   busyMean

	name  string
	bgra  bool
	drive string // "async"/"sync" as resolved from the MFT's MF_TRANSFORM_ASYNC attribute
	// swTier: we ASKED for the software tier (poisoned hardware); swBound: we GOT it.
	swTier  bool
	swBound bool
	// degrade names, in one human sentence, why this session is not on its best path. It reaches
	// the route panel so a silently-degraded rig is visible instead of just slow.
	degradeMu sync.Mutex
	degrade   string
	encFails  atomic.Uint32
	healthy   atomic.Bool // an AU really came out (ledger proof-of-health)

	out        chan AU
	done       chan struct{}
	pumpDone   chan struct{}
	closed     atomic.Bool
	recovering atomic.Bool   // child died; frames dropped until the session is re-placed
	droppedAUs atomic.Uint64 // mirror of the shm counter (safe to read after Close)
	busyDrops  atomic.Uint32 // ditto: saturation drops, mirrored by pump while the shm is live
	// shmMu guards the MAPPING's lifetime against readers. Stats() is called from the route's
	// telemetry goroutine and can land after Close() unmapped the view - reading the counters
	// straight out of the mapping then faults on freed VA (0xc0000005 in Stats, seen in the
	// mediapipe gate). Counters that must survive Close are mirrored into atomics above;
	// everything still read from the mapping takes this lock.
	shmMu   sync.RWMutex
	shmGone bool // set under shmMu.Lock() just before the view is unmapped

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

// SpoutSource is a zero-copy capture source: the sender's GPU shared texture. Resolve re-reads
// the handle + format from the sender registry (cached, no GL, no pixels) - it is called on every
// (re)open so a restarted sender never gets its DEAD handle re-issued (R1).
type SpoutSource struct {
	Name    string
	Resolve func() (handle uint64, dxgiFormat uint32, w, h int, ok bool)
}

// ProcOpts parametrizes one native encode session. Spout == nil = today's SHM frame ring,
// byte-identical to the pre-zero-copy behaviour.
type ProcOpts struct {
	LUID                 int64
	InW, InH, OutW, OutH int
	FPS                  float64
	Kbps, Gop            int
	Spout                *SpoutSource
	// ZeroCopyAdapters lists adapter LUIDs this session MAY be re-placed on when LUID cannot open
	// the sender's shared texture (cross-adapter sender, risk R7 - reproduced on the dev rig).
	// EMPTY = never move, which is the default and what an explicitly pinned encode device gets:
	// "never silently move adapters" is a hard rule, so a move requires the caller to opt in AND it
	// always logs. Only consulted on a zero-copy session.
	ZeroCopyAdapters []int64
}

// OpenProcSession opens one native encode session on the (supervised, per-adapter) Zig
// encoder child. Errors are CLEAN - the caller falls back to the ffmpeg engine.
func OpenProcSession(luid int64, inW, inH, outW, outH int, fps float64, kbps, gop int) (*ProcSession, error) {
	return OpenProcSessionOpts(ProcOpts{LUID: luid, InW: inW, InH: inH, OutW: outW, OutH: outH,
		FPS: fps, Kbps: kbps, Gop: gop})
}

// OpenProcSessionOpts is OpenProcSession with a zero-copy capture source. When o.Spout is set the
// child reads the sender's shared texture itself: NO frame slot is allocated (SHM = header + AU
// ring only) and the caller must never call Encode. A child that refuses the source returns
// ErrZeroCopyRefused so the caller can reopen on the readback path (§7.3, never a dead route).
func OpenProcSessionOpts(o ProcOpts) (*ProcSession, error) {
	s, err := openProcSessionOn(o)
	if err == nil || o.Spout == nil || len(o.ZeroCopyAdapters) == 0 || !errors.Is(err, ErrZeroCopyRefused) {
		return s, err
	}
	// The ENCODER opened fine on this adapter but the SOURCE did not: the sender's texture lives on
	// another GPU. Probe the candidates once (bounded, cached per sender) instead of dropping the
	// whole route to the readback path.
	alt, _, aerr := replaceOnAffineAdapter(o, affinityCandidates(o.Spout.Name, o.LUID, o.ZeroCopyAdapters), openProcSessionOn)
	if aerr == nil {
		return alt, nil
	}
	return nil, err // report the ORIGINAL refusal: it names the rung the caller downgrades from
}

// openProcSessionOn opens the session on exactly o.LUID (no affinity resolution).
func openProcSessionOn(o ProcOpts) (*ProcSession, error) {
	luid, inW, inH, outW, outH := o.LUID, o.InW, o.InH, o.OutW, o.OutH
	kbps, gop := o.Kbps, o.Gop
	if os.Getenv("RAVE_MATE_MFENC_OPEN_FAIL") != "" {
		return nil, errors.New("mfenc: native open disabled (RAVE_MATE_MFENC_OPEN_FAIL)")
	}
	fps := o.FPS
	if fps <= 0 {
		fps = 30
	}
	// A poisoned (adapter, encoder) pair does NOT mean "no native video": it means "not THAT
	// encoder". The session asks the child for the SOFTWARE tier instead, which keeps real pixels
	// on the wire. Only when the software tier on this adapter is poisoned too do we refuse and
	// let mediapipe substitute an ffmpeg encoder.
	swTier := false
	degrade := ""
	if reason, bad := PoisonedOn(luid); bad {
		if swReason, swBad := PoisonedTuple(luid, swEncoderKey); swBad {
			return nil, errors.New("mfenc: no usable encoder on this adapter - " + reason + " / software tier: " + swReason + " - using ffmpeg")
		}
		swTier = true
		degrade = "hardware encoder poisoned (" + reason + ") - encoding on the software MF tier"
		Warnf("mfenc: %s", degrade)
	}

	zc := o.Spout != nil
	frameBytes := inW * inH * 4
	ringSize := 8 << 20
	if frameBytes > ringSize {
		ringSize = frameBytes
	}
	ringKB := 0
	if zc {
		frameBytes = 0 // no frame slot: 66.4 MB → 4.0 MB of shared VA per 4K session
		ringKB = ringKBFor(kbps)
		ringSize = ringKB * 1024
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
		if zc {
			// Version gate: a v1 child sizes its mapping from in_w*in_h*4 and would map past the
			// end of our (much smaller) zero-copy mapping. Refuse cleanly instead.
			if v := child.waitProtoVer(2 * time.Second); v < protoVerZeroC {
				return nil, fmt.Errorf("%w: encoder child protocol v%d (need v%d)", ErrZeroCopyRefused, v, protoVerZeroC)
			}
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
			zeroCopy: zc, ringKB: ringKB,
			swTier: swTier, degrade: degrade,
		}
		if zc {
			s.zcName, s.resolve = o.Spout.Name, o.Spout.Resolve
			s.recycle = s.recycleSpout
		}
		ev, err := child.openSession(s)
		if err != nil {
			shm.close()
			lastErr = err
			continue // child likely died (crash path counts it); retry once on the respawn
		}
		if !ev.OK {
			shm.close()
			if zc && ev.Cap == "downgraded" {
				// The encoder is fine, the SOURCE is not: hand the caller a typed refusal so it
				// reopens on the readback path instead of dropping to ffmpeg.
				return nil, fmt.Errorf("%w (%s)", ErrZeroCopyRefused, ev.ErrSrc)
			}
			return nil, errors.New("mfenc: native open failed: " + ev.Err) // clean refusal: no poison, no retry
		}
		s.name = ev.Name
		s.bgra = ev.Bgra
		s.drive = ev.Drive
		s.swBound = ev.SW
		if ev.SW && degrade == "" {
			s.degrade = "no usable hardware H.264 MFT on this adapter - encoding on the software MF tier"
			Warnf("mfenc: %s (%s)", s.degrade, ev.Name)
		}
		NoteEncoder(luid, ledgerEncoder(ev))
		// A CONFIGURED adapter LUID that no longer resolves silently ran the pipeline on a
		// different GPU while every log line named the requested one (LUIDs are not stable across
		// reboots, driver resets, or a virtual display appearing). Say so, once per process.
		if luid != 0 && ev.LUIDRes != 0 && ev.LUIDRes != luid {
			warnLUIDDriftOnce.Do(func() {
				Warnf("mfenc: requested encode adapter %#x is not present - the pipeline is on %#x (%s). Re-pick the encoder device in settings; a stored LUID does not survive a driver reset.",
					uint64(luid), uint64(ev.LUIDRes), ev.Adapter)
			})
		}
		child.mu.Lock()
		child.sessions[sid] = s
		child.lastEnc = ledgerEncoder(ev)
		child.mu.Unlock()
		go s.pump()
		if zc {
			s.watchDone = make(chan struct{})
			go s.watchSpout()
		}
		return s, nil
	}
	return nil, fmt.Errorf("mfenc: session open failed: %w", lastErr)
}

// ringKBFor sizes a zero-copy session's AU ring: half a second of bitstream, floor 4 MiB,
// ceiling 16 MiB. Bitrate-derived, so it does NOT change when the sender resizes.
func ringKBFor(kbps int) int {
	kb := kbps / 16 // kbps/8 KB/s, halved = 0.5 s of bitstream
	if kb < ringKBMin {
		return ringKBMin
	}
	if kb > ringKBMax {
		return ringKBMax
	}
	return kb
}

// ── zero-copy source health (R1: a stale share handle is a SILENTLY FROZEN picture) ──

// spoutVerdict is what a zero-copy session should do about its capture source.
type spoutVerdict int

const (
	spoutHealthy spoutVerdict = iota
	spoutRecycleNow
	spoutUnresolvable // sender gone from the registry: wait, do not churn reopens
)

// spoutProbe is one health sample: the registry answer plus the child's capture counters.
type spoutProbe struct {
	curHandle  uint64
	newHandle  uint64
	resolved   bool
	capFrames  uint64
	prevFrames uint64
	lastCapNs  int64
	nowNs      int64
	staleNs    int64
}

// spoutCheck is the frozen-picture oracle, pure so it can be asserted without hardware.
//
// Two independent detectors, because the worst failure mode looks healthy from either side
// alone: after a sender restart OpenSharedResource can still SUCCEED on a dead texture (frames
// keep "arriving", content never changes), and a sender can also vanish while the handle value
// happens to be reused. So: a CHANGED handle is always a recycle, and a capture clock that has
// not moved for staleNs while no new frames were counted is also a recycle.
func spoutCheck(p spoutProbe) (spoutVerdict, string) {
	if !p.resolved {
		return spoutUnresolvable, "sender not in the registry"
	}
	if p.newHandle != 0 && p.curHandle != 0 && p.newHandle != p.curHandle {
		return spoutRecycleNow, "share handle changed (sender restarted)"
	}
	if p.staleNs > 0 && p.lastCapNs > 0 && p.capFrames == p.prevFrames && p.nowNs-p.lastCapNs > p.staleNs {
		return spoutRecycleNow, "no capture progress (frozen source)"
	}
	return spoutHealthy, ""
}

// watchSpout runs the R1 oracle on the same 2 s cadence as the registry scan. Bounded work: one
// cached registry lookup + four header reads per tick, no allocation.
func (s *ProcSession) watchSpout() {
	defer close(s.watchDone)
	t := time.NewTicker(spoutWatchEvery)
	defer t.Stop()
	var prevFrames uint64
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if s.closed.Load() || s.recovering.Load() {
			continue // a child restart is already re-placing us with a fresh handle
		}
		var newH uint64
		ok := false
		if s.resolve != nil {
			newH, _, _, _, ok = s.resolve()
		}
		s.zcMu.Lock()
		cur := s.zcHandle
		s.zcMu.Unlock()
		frames := atomic.LoadUint64(s.shm.u64(offCapFrames))
		v, why := spoutCheck(spoutProbe{curHandle: cur, newHandle: newH, resolved: ok,
			capFrames: frames, prevFrames: prevFrames,
			lastCapNs: atomic.LoadInt64(s.shm.i64(offLastCapNs)), nowNs: childNowNs(),
			staleNs: int64(spoutStaleAfter)})
		prevFrames = frames
		if v == spoutRecycleNow && s.recycle != nil {
			s.recycle(why)
		}
	}
}

// QPC, same counter the child stamps lastCapNs with (system-wide, so the cross-process
// comparison is apples to apples). x/sys/windows does not export these two.
var (
	modkernel32   = windows.NewLazySystemDLL("kernel32.dll")
	procQPCounter = modkernel32.NewProc("QueryPerformanceCounter")
	procQPCFreq   = modkernel32.NewProc("QueryPerformanceFrequency")
	qpcFreqOnce   sync.Once
	qpcFreqPerSec int64
)

// childNowNs is "now" on the child's capture clock (0 = unavailable → staleness check is skipped).
func childNowNs() int64 {
	qpcFreqOnce.Do(func() {
		var f int64
		if r, _, _ := procQPCFreq.Call(uintptr(unsafe.Pointer(&f))); r != 0 {
			qpcFreqPerSec = f
		}
	})
	if qpcFreqPerSec <= 0 {
		return 0
	}
	var ctr int64
	if r, _, _ := procQPCounter.Call(uintptr(unsafe.Pointer(&ctr))); r == 0 {
		return 0
	}
	return int64((float64(ctr) / float64(qpcFreqPerSec)) * 1e9)
}

// onSrcGone handles the child's srcgone event: the capture source is unusable but the encoder is
// intact, so the parent - not the child - decides. Same action as the staleness oracle.
func (s *ProcSession) onSrcGone(reason string) {
	if s.recycle != nil {
		s.recycle("child reported srcgone: " + reason)
	}
}

// recycleSpout closes + reopens the session with a FRESH handle. After spoutMaxRecycles failures
// the sender is pinned to the readback path: the session fails cleanly (the route re-establishes
// there) rather than shipping a frozen picture forever.
func (s *ProcSession) recycleSpout(why string) {
	if s.closed.Load() {
		return
	}
	s.zcMu.Lock()
	if time.Since(s.zcLastMove) < spoutWatchEvery {
		s.zcMu.Unlock()
		return // one recycle per tick, no matter how many detectors fired
	}
	s.zcLastMove = time.Now()
	s.zcRecycles++
	n := s.zcRecycles
	s.zcMu.Unlock()
	s.downgrades.Add(1)
	if n > spoutMaxRecycles {
		pinReadback(s.zcName)
		Warnf("mfenc: zero-copy source %q unusable after %d reopen attempts (%s) - pinning this sender to the readback path",
			s.zcName, spoutMaxRecycles, why)
		s.fail("mfenc: zero-copy capture source gone - " + why)
		// End the AU stream so the route does NOT sit silently on a frozen source: the caller
		// sees EOF, the route re-establishes, and the pin above sends it down the readback path.
		// Own goroutine: Close joins this watchdog, and we may BE it.
		go s.Close()
		return
	}
	Warnf("mfenc: zero-copy source %q recycling (%s), attempt %d/%d", s.zcName, why, n, spoutMaxRecycles)
	// Drop frames while the source is swapped, exactly like a child restart: the route must not
	// stall and must not see errors for the gap.
	s.recovering.Store(true)
	defer s.recovering.Store(false)
	closedCh := make(chan struct{})
	s.child.mu.Lock()
	s.child.closeWait[s.sid] = closedCh
	s.child.mu.Unlock()
	if s.child.send(map[string]any{"op": "close", "sid": s.sid}) == nil {
		select {
		case <-closedCh:
		case <-time.After(3 * time.Second):
		}
	}
	// openSession re-reads the handle (refreshHandle) before it sends, so the reopen is on the
	// NEW texture by construction.
	ev, err := s.child.openSession(s)
	if err != nil || !ev.OK {
		msg := "reopen refused"
		if err != nil {
			msg = err.Error()
		} else if ev.ErrSrc != "" {
			msg = ev.ErrSrc
		}
		Warnf("mfenc: zero-copy source %q reopen failed: %s", s.zcName, msg)
		return // the next watch tick tries again (bounded by spoutMaxRecycles)
	}
	s.ForceKeyframe() // the receiver needs a fresh IDR after the gap
}

// ── readback pinning: senders whose zero-copy path proved unusable (§7.3 last rung) ──
var (
	pinMu    sync.Mutex
	pinnedZC = map[string]bool{}
)

func pinReadback(name string) {
	if name == "" {
		return
	}
	pinMu.Lock()
	pinnedZC[name] = true
	pinMu.Unlock()
}

// ZeroCopyPinnedToReadback reports whether this sender is pinned to the readback path (a
// zero-copy session on it failed repeatedly). Callers skip the zero-copy request entirely.
func ZeroCopyPinnedToReadback(name string) bool {
	pinMu.Lock()
	defer pinMu.Unlock()
	return pinnedZC[name]
}

// refreshHandle re-reads the sender's handle/format from the registry before an (re)open.
func (s *ProcSession) refreshHandle() {
	if s.resolve == nil {
		return
	}
	h, f, _, _, ok := s.resolve()
	if !ok {
		return // keep the last known handle: the child refuses a dead one and we retry
	}
	s.zcMu.Lock()
	s.zcHandle, s.zcFmt = h, f
	s.zcMu.Unlock()
}

func (s *ProcSession) spoutHandle() (uint64, uint32, string) {
	s.zcMu.Lock()
	defer s.zcMu.Unlock()
	return s.zcHandle, s.zcFmt, s.zcName
}

// IsZeroCopy reports whether this session consumes a GPU shared texture (no host frames).
func (s *ProcSession) IsZeroCopy() bool { return s.zeroCopy }

// AdapterLUID is the adapter this session actually runs on (after any affinity re-place).
func (s *ProcSession) AdapterLUID() int64 { return s.child.luid }

// AdapterMoved reports whether affinity resolution re-placed this session onto another adapter.
func (s *ProcSession) AdapterMoved() bool { return s.movedFrom != 0 }

// degradeTo records why this session is off its best path (first reason wins - the root cause).
func (s *ProcSession) degradeTo(reason string) {
	s.degradeMu.Lock()
	if s.degrade == "" {
		s.degrade = reason
	}
	s.degradeMu.Unlock()
}

// DegradeReason is the one-sentence "why is this route not on its best path" for the panel.
// "" = nothing to report.
func (s *ProcSession) DegradeReason() string {
	s.degradeMu.Lock()
	defer s.degradeMu.Unlock()
	return s.degrade
}

// Drive reports the resolved MFT drive mode ("async"/"sync"; "" = pre-fix child).
func (s *ProcSession) Drive() string { return s.drive }

// ledgerKey is this session's row in the (adapter, encoder) failure ledger.
func (s *ProcSession) ledgerKey() string {
	if s.swBound {
		return swEncoderKey
	}
	return s.name
}

// IsSoftware reports whether the software MF encoder tier is serving this session.
func (s *ProcSession) IsSoftware() bool { return s.swBound }

// onEncFail records an ATTRIBUTED mid-route encode failure from the child.
func (s *ProcSession) onEncFail(rc int32, stage uint32, fails uint32) {
	s.encFails.Store(fails)
	name := stageName(stage)
	reason := fmt.Sprintf("encoder rejected frames (rc %d", rc)
	if name != "" {
		reason += ", stage: " + name
	}
	reason += ")"
	s.degradeTo(reason)
	Warnf("mfenc: session %d encode failure #%d - %s (encoder %s, drive %s)", s.sid, fails, reason, encoderLabel(s.name), s.drive)
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

// Failed reports a hard session failure (child crash limit, respawn failure). nil = fine.
// Exported so the mediapipe bridge can tell "the AU stream ended because the route is closing"
// apart from "the AU stream ended because the engine died" - the two used to be indistinguishable,
// and the second one silently ended the route with a frozen picture.
func (s *ProcSession) Failed() error { return s.failed() }

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

// counterRate turns a monotone child-side counter into a per-second rate (anchor refreshed on
// read, >= 500 ms apart - same shape as mediapipe's rate).
type counterRate struct {
	mu     sync.Mutex
	anchor uint64
	at     time.Time
	fps    float64
}

func (r *counterRate) sample(n uint64, now time.Time) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.at.IsZero() {
		r.at, r.anchor = now, n
		return 0
	}
	if d := now.Sub(r.at); d >= 500*time.Millisecond {
		r.fps = float64(n-r.anchor) / d.Seconds()
		r.at, r.anchor = now, n
	}
	return r.fps
}

// busyMean turns the child's cumulative encBusyNs + capFrames into a per-frame mean over the
// interval between reads (a cumulative ratio would flatten a live saturation spike).
type busyMean struct {
	mu     sync.Mutex
	ns     uint64
	frames uint64
	ms     float64
}

func (b *busyMean) mean(ns, frames uint64) float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if frames > b.frames && ns >= b.ns {
		b.ms = float64(ns-b.ns) / float64(frames-b.frames) / 1e6
	}
	b.ns, b.frames = ns, frames
	return b.ms
}

// Encode submits one RGBA frame. During a child restart frames are DROPPED (nil error)
// so the route survives the crash; a poisoned/dead session returns an error.
func (s *ProcSession) Encode(rgba []byte, ptsNs int64) error {
	if s.zeroCopy {
		// There is no frame slot on this session; a host frame here means the caller mixed the
		// two paths, which would write past the header into the AU ring.
		return errors.New("mfenc: Encode on a zero-copy session (the child owns the source)")
	}
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
		// Mirror the saturation counter while the mapping is guaranteed live (pump exits before
		// Close unmaps). Stats() must never read the mapping for a value it may need afterwards.
		s.busyDrops.Store(atomic.LoadUint32((*uint32)(unsafe.Add(s.shm.base, offBusyDrops))))
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
			// Proof of health for the ledger: this (adapter, encoder) really produced output, so
			// a stale poison on it may eventually be forgiven. Time alone never forgives it.
			if s.healthy.CompareAndSwap(false, true) {
				NoteHealthy(s.child.luid, s.ledgerKey())
			}
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
	st := ProcStats{Name: s.name, QueueDepth: depth, DroppedAUs: s.droppedAUs.Load(),
		Drive: s.drive, Software: s.swBound, DegradeReason: s.DegradeReason(), EncFails: s.encFails.Load(),
		BusyDrops: s.busyDrops.Load()}
	if n > 0 {
		sort.Float64s(tmp)
		st.LatP50Ms = tmp[n/2]
		st.LatP99Ms = tmp[(n*99)/100]
	}
	// Everything below reads the MAPPING: hold shmMu so Close() cannot unmap underneath us.
	s.shmMu.RLock()
	defer s.shmMu.RUnlock()
	if s.zeroCopy && !s.shmGone {
		st.ZeroCopy = true
		st.CapFrames = atomic.LoadUint64(s.shm.u64(offCapFrames))
		st.CapSkips = atomic.LoadUint64(s.shm.u64(offCapSkips))
		st.MtxTimeouts = atomic.LoadUint64(s.shm.u64(offMtxTimeout))
		st.SrcErrors = atomic.LoadUint64(s.shm.u64(offSrcErrors))
		st.CapFmt = atomic.LoadUint32((*uint32)(unsafe.Add(s.shm.base, offCapFmt)))
		st.CapFlags = atomic.LoadUint32((*uint32)(unsafe.Add(s.shm.base, offCapFlags)))
		st.Downgrades = int(s.downgrades.Load())
		st.AdapterMoved = s.movedFrom != 0
		if last := atomic.LoadInt64(s.shm.i64(offLastCapNs)); last > 0 {
			if now := childNowNs(); now > last {
				st.CapStaleMs = float64(now-last) / 1e6
			}
		}
		st.CapFPS = s.capRate.sample(st.CapFrames, time.Now())
		st.EncBusyMs = s.busyRate.mean(atomic.LoadUint64(s.shm.u64(56)), st.CapFrames)
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
	if s.watchDone != nil {
		<-s.watchDone // the source watchdog reads the shm header: it must be gone before unmap
	}
	_ = windows.SetEvent(s.shm.evAU) // wake the pump for its final drain
	// UNCONDITIONAL: the shm must never be unmapped while pump can still touch it. pump's
	// exit is bounded by construction (waits <=200ms, ring drain finite, teardown delivery
	// never blocks), so this converges promptly; a timeout fallback here would be a UAF.
	<-s.pumpDone
	// Mark the mapping gone and unmap under the write lock: a Stats() call already inside the
	// read lock finishes first, and any later one sees shmGone and touches nothing.
	s.shmMu.Lock()
	s.shmGone = true
	s.shm.close()
	s.shmMu.Unlock()
}
