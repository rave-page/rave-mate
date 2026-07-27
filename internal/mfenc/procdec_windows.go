//go:build windows && cgo

package mfenc

// procdec: the RECEIVE-side session on the same supervised per-adapter Zig child (zigmedia inc 2).
// Mirror image of procparent's encode session, opposite direction: this side APPENDS compressed
// access units to an inbound SHM ring and the child decodes them and blits each frame straight into
// the destination video-share sender's shared texture. Go moves AUs (~100 KB) and scalars; a decoded
// frame (33 MB at 4K) never crosses a pipe, never lands on the Go heap and is never uploaded twice.
//
// Wire contract additions (must mirror native/zigenc/src/main.zig):
//   open: "dir":"dec" | "codec":"h264"|"hevc" | "dsh" u64 dest share handle | "dfmt" u32 |
//         "dname" string | "in_ring_kb" u32.   Requires child hello.ver >= 3.
//   shm:  header (256 B) + INBOUND ring only. Header 128-207 is the second ring-counter block
//         plus decode telemetry (offsets below). Parent signals -f on append, child -c on consume.
//   events: {"ev":"dstgone","sid":N,"reason":"…"} - the destination texture became unusable.

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	protoVerDecode = 3 // minimum child hello.ver that may be asked for dir:"dec"

	// Inbound ring counters + decode telemetry (design §10). inWrite/inDropped are OURS, the rest
	// are child-written.
	offInWrite      = 128
	offInRead       = 136
	offInDropped    = 144
	offDecBusy      = 152
	offDecFrames    = 160
	offDecErrors    = 168
	offLastPubNs    = 176
	offDecFlags     = 184
	offDecDropped   = 192
	offDecMtxTimeo  = 200
	decRingRecovery = 3 * time.Second // publish silence that trips the frozen-destination oracle
	decWatchEvery   = 2 * time.Second
	decMaxRecycles  = 3 // then the sender is pinned to the ffmpeg decode path
)

// ErrDecodeRefused is the open-side downgrade rung: the child could not build the decode pipeline
// or could not consume the destination texture (foreign adapter, exotic format, no D3D11-aware
// decoder). The caller falls back to the ffmpeg decode child - never a dead route.
var ErrDecodeRefused = errors.New("mfenc: native decode refused")

// DecodeDest is the destination video-share sender: Go created it and owns its lifetime, the child
// only renders into it. Resolve re-reads the handle so a sender that was re-created while the child
// was down never gets its DEAD texture re-issued (the receive side's R1).
type DecodeDest struct {
	Name    string
	Resolve func() (handle uint64, dxgiFormat uint32, w, h int, ok bool)
}

// ProcDecOpts parametrizes one native decode session.
type ProcDecOpts struct {
	LUID       int64
	InW, InH   int // decoded stream geometry
	OutW, OutH int // destination sender geometry (the VP output size)
	FPS        float64
	HEVC       bool
	KbpsHint   int // sizes the inbound ring (bitrate-derived, geometry-independent)
	Dest       *DecodeDest
}

// ProcDecStats is one decode session's live telemetry.
type ProcDecStats struct {
	Name        string  // decoder MFT friendly name
	DecFrames   uint64  // frames published into the destination texture
	DecFPS      float64 // derived from DecFrames
	DecBusyMs   float64 // mean child decode+publish time per AU
	InDropped   uint64  // AUs the inbound ring could not take (ring full)
	DecDropped  uint64  // AUs the child could not submit (oversized alloc failed)
	DecErrors   uint64  // decode/publish hard failures
	MtxTimeouts uint64  // destination-texture acquire timeouts
	DecStaleMs  float64 // age of the last publish (frozen-destination oracle)
	DecFlags    uint32  // bit0 live, bit1 keyed mutex, bit2 named mutex, bit3 unsync, bit4 hw MFT
	QueueDepth  int     // AUs appended minus AUs consumed by the child
	Restarts    int
	ChildCPUPct float64
	Downgrades  int
}

// ProcDecSession is one decode+publish session (medialink-facing surface).
type ProcDecSession struct {
	child *procChild
	sid   uint32
	shm   *shmRegion

	inW, inH, outW, outH int
	fps                  float64
	hevc                 bool
	ringKB               int
	ringSize             uint64
	ringOff              uintptr

	name string

	// destination identity - SCALARS only, never a pixel
	dstMu       sync.Mutex
	dstHandle   uint64
	dstFmt      uint32
	dstName     string
	resolve     func() (uint64, uint32, int, int, bool)
	dstRecycles int
	dstLastMove time.Time
	recycle     func(reason string)

	done       chan struct{} // closed by Close: stops the destination watchdog
	watchDone  chan struct{}
	closed     atomic.Bool
	recovering atomic.Bool
	appended   atomic.Uint64
	downgrades atomic.Int32
	decRate    counterRate
	busyRate   busyMean

	failMu  sync.Mutex
	failErr error
}

// OpenProcDecSession opens a native decode session. Errors are CLEAN: ErrDecodeRefused (or any
// other error) means the caller runs the ffmpeg decode child instead.
func OpenProcDecSession(o ProcDecOpts) (*ProcDecSession, error) {
	if o.Dest == nil || o.Dest.Resolve == nil {
		return nil, fmt.Errorf("%w: no destination texture", ErrDecodeRefused)
	}
	if os.Getenv("RAVE_MATE_MFDEC_OPEN_FAIL") != "" {
		return nil, errors.New("mfenc: native decode disabled (RAVE_MATE_MFDEC_OPEN_FAIL)")
	}
	fps := o.FPS
	if fps <= 0 {
		fps = 30
	}
	// Same rule as the outbound ring: half a second of bitstream, 4-16 MiB, derived from BITRATE so
	// a stream resize costs zero SHM realloc.
	ringKB := ringKBFor(o.KbpsHint)
	ringSize := ringKB * 1024

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		child, err := getChild(o.LUID)
		if err != nil {
			return nil, err
		}
		if err := child.waitUsable(3 * time.Second); err != nil {
			return nil, err
		}
		// Version gate: a v2 child ignores "dir" and would open an ENCODE session sized from
		// in_w*in_h*4 - past the end of this much smaller mapping.
		if v := child.waitProtoVer(2 * time.Second); v < protoVerDecode {
			return nil, fmt.Errorf("%w: encoder child protocol v%d (need v%d)", ErrDecodeRefused, v, protoVerDecode)
		}
		sid := sidSeq.Add(1)
		shm, err := createShm(fmt.Sprintf(`Local\rvmfenc-%d-%d`, os.Getpid(), sid), shmHdrSize+ringSize)
		if err != nil {
			return nil, err
		}
		d := &ProcDecSession{
			child: child, sid: sid, shm: shm,
			inW: o.InW, inH: o.InH, outW: o.OutW, outH: o.OutH, fps: fps, hevc: o.HEVC,
			ringKB: ringKB, ringSize: uint64(ringSize), ringOff: shmHdrSize,
			dstName: o.Dest.Name, resolve: o.Dest.Resolve, done: make(chan struct{}),
		}
		d.recycle = d.recycleDest
		ev, err := child.openDecSession(d)
		if err != nil {
			shm.close()
			lastErr = err
			continue // child likely died; retry once on the respawn
		}
		if !ev.OK {
			shm.close()
			if ev.Cap == "downgraded" {
				return nil, fmt.Errorf("%w (%s)", ErrDecodeRefused, ev.ErrDst)
			}
			return nil, errors.New("mfenc: native decode open failed: " + ev.Err)
		}
		d.name = ev.Name
		child.mu.Lock()
		child.decs[sid] = d
		child.mu.Unlock()
		d.watchDone = make(chan struct{})
		go d.watchDest()
		return d, nil
	}
	return nil, fmt.Errorf("mfenc: decode session open failed: %w", lastErr)
}

// refreshDest re-reads the destination handle/format before an (re)open.
func (d *ProcDecSession) refreshDest() {
	if d.resolve == nil {
		return
	}
	h, f, _, _, ok := d.resolve()
	if !ok {
		return // keep the last handle: the child refuses a dead one and we retry
	}
	d.dstMu.Lock()
	d.dstHandle, d.dstFmt = h, f
	d.dstMu.Unlock()
}

func (d *ProcDecSession) destHandle() (uint64, uint32, string) {
	d.dstMu.Lock()
	defer d.dstMu.Unlock()
	return d.dstHandle, d.dstFmt, d.dstName
}

// Name returns the decoder MFT friendly name.
func (d *ProcDecSession) Name() string { return d.name }

// Decode appends one access unit to the inbound ring and wakes the child.
//
// Drop policy (the CLAUDE.md bound): the ring is 4-16 MiB and holds compressed AUs only. A full
// ring drops the NEWEST AU and counts it (inDropped) - blocking here would stall the route's jitter
// drain, and a receive route that cannot keep up must degrade by losing frames, not by growing.
// During a child restart AUs are dropped silently (nil error) so the route survives the crash.
func (d *ProcDecSession) Decode(au []byte, ptsNs int64, keyframe bool) error {
	if d.closed.Load() {
		return errors.New("mfenc: decode session closed")
	}
	if err := d.failed(); err != nil {
		return err
	}
	if len(au) == 0 {
		return nil
	}
	if d.recovering.Load() {
		return nil
	}
	d.child.mu.Lock()
	dead := d.child.dead
	d.child.mu.Unlock()
	if dead {
		return nil
	}
	rec := uint64(16 + ((len(au) + 7) &^ 7))
	if rec > d.ringSize {
		atomic.AddUint64(d.shm.u64(offInDropped), 1) // an AU larger than the whole ring: hopeless
		return nil
	}
	w := atomic.LoadUint64(d.shm.u64(offInWrite))
	r := atomic.LoadUint64(d.shm.u64(offInRead))
	need := rec
	tail := d.ringSize - (w % d.ringSize)
	wraps := tail < rec
	if wraps {
		need = rec + tail
	}
	if w < r || need > d.ringSize-(w-r) {
		atomic.AddUint64(d.shm.u64(offInDropped), 1)
		return nil
	}
	if wraps {
		if tail >= 4 { // leave a wrap marker so the child skips the unusable tail
			*(*uint32)(unsafe.Add(d.shm.base, d.ringOff+uintptr(w%d.ringSize))) = wrapMarker
		}
		w += tail
	}
	pos := uintptr(w % d.ringSize)
	*(*uint32)(unsafe.Add(d.shm.base, d.ringOff+pos)) = uint32(len(au))
	flags := uint32(0)
	if keyframe {
		flags = 1
	}
	*(*uint32)(unsafe.Add(d.shm.base, d.ringOff+pos+4)) = flags
	*(*int64)(unsafe.Add(d.shm.base, d.ringOff+pos+8)) = ptsNs
	copy(unsafe.Slice((*byte)(unsafe.Add(d.shm.base, d.ringOff+pos+16)), len(au)), au)
	atomic.StoreUint64(d.shm.u64(offInWrite), w+rec) // release: data before the head moves
	d.appended.Add(1)
	_ = windows.SetEvent(d.shm.evFrame)
	return nil
}

// Stats reports per-session telemetry (all header reads, so the AU path stays JSON-free).
func (d *ProcDecSession) Stats() ProcDecStats {
	st := ProcDecStats{
		Name:        d.name,
		DecFrames:   atomic.LoadUint64(d.shm.u64(offDecFrames)),
		InDropped:   atomic.LoadUint64(d.shm.u64(offInDropped)),
		DecDropped:  atomic.LoadUint64(d.shm.u64(offDecDropped)),
		DecErrors:   atomic.LoadUint64(d.shm.u64(offDecErrors)),
		MtxTimeouts: atomic.LoadUint64(d.shm.u64(offDecMtxTimeo)),
		DecFlags:    atomic.LoadUint32((*uint32)(unsafe.Add(d.shm.base, offDecFlags))),
		Downgrades:  int(d.downgrades.Load()),
	}
	st.DecFPS = d.decRate.sample(st.DecFrames, time.Now())
	st.DecBusyMs = d.busyRate.mean(atomic.LoadUint64(d.shm.u64(offDecBusy)), st.DecFrames)
	if last := atomic.LoadInt64(d.shm.i64(offLastPubNs)); last > 0 {
		if now := childNowNs(); now > last {
			st.DecStaleMs = float64(now-last) / 1e6
		}
	}
	// Queue depth in AUs: what we appended minus what the child consumed. Rising = the decoder is
	// behind, which is the receive side's saturation signal.
	consumed := atomic.LoadUint64(d.shm.u64(offInRead))
	written := atomic.LoadUint64(d.shm.u64(offInWrite))
	if written >= consumed && d.ringSize > 0 {
		st.QueueDepth = int((written - consumed) / 1024) // KiB of undecoded bitstream
	}
	d.child.mu.Lock()
	st.Restarts = d.child.restarts
	d.child.mu.Unlock()
	st.ChildCPUPct = d.child.cpuPercent()
	return st
}

// IsHardware reports whether the child bound a true hardware decoder MFT (diagnostic).
func (d *ProcDecSession) IsHardware() bool {
	return atomic.LoadUint32((*uint32)(unsafe.Add(d.shm.base, offDecFlags)))&(1<<4) != 0
}

// ── destination health: a stale destination texture is a SILENTLY FROZEN picture ──

// watchDest runs the frozen-destination oracle on the registry-scan cadence. Same two independent
// detectors as watchSpout, because either alone looks healthy: a re-created sender can leave our
// handle opening a DEAD texture (frames "publish", nothing ever appears), and a publish clock that
// stopped while AUs keep arriving means the child is no longer landing frames.
func (d *ProcDecSession) watchDest() {
	defer close(d.watchDone)
	t := time.NewTicker(decWatchEvery)
	defer t.Stop()
	var prevFrames, prevAppended uint64
	for {
		select {
		case <-d.done:
			return
		case <-t.C:
		}
		if d.closed.Load() || d.recovering.Load() {
			continue
		}
		var newH uint64
		ok := false
		if d.resolve != nil {
			newH, _, _, _, ok = d.resolve()
		}
		d.dstMu.Lock()
		cur := d.dstHandle
		d.dstMu.Unlock()
		frames := atomic.LoadUint64(d.shm.u64(offDecFrames))
		appended := d.appended.Load()
		v, why := decCheck(decProbe{curHandle: cur, newHandle: newH, resolved: ok,
			decFrames: frames, prevFrames: prevFrames,
			appended: appended, prevAppended: prevAppended,
			lastPubNs: atomic.LoadInt64(d.shm.i64(offLastPubNs)), nowNs: childNowNs(),
			staleNs: int64(decRingRecovery)})
		prevFrames, prevAppended = frames, appended
		if v == spoutRecycleNow && d.recycle != nil {
			d.recycle(why)
		}
	}
}

// decProbe is one destination-health sample.
type decProbe struct {
	curHandle    uint64
	newHandle    uint64
	resolved     bool
	decFrames    uint64
	prevFrames   uint64
	appended     uint64
	prevAppended uint64
	lastPubNs    int64
	nowNs        int64
	staleNs      int64
}

// decCheck is the frozen-destination oracle, pure so it can be asserted without hardware.
//
// The "no progress" arm requires AUs to have ARRIVED in the interval: a route that is simply idle
// (peer paused, no frames on the wire) publishes nothing and is perfectly healthy, so treating
// silence alone as a fault would churn reopens on an idle route.
func decCheck(p decProbe) (spoutVerdict, string) {
	if !p.resolved {
		return spoutUnresolvable, "destination sender not resolvable"
	}
	if p.newHandle != 0 && p.curHandle != 0 && p.newHandle != p.curHandle {
		return spoutRecycleNow, "destination share handle changed (sender re-created)"
	}
	if p.staleNs > 0 && p.lastPubNs > 0 && p.decFrames == p.prevFrames &&
		p.appended > p.prevAppended && p.nowNs-p.lastPubNs > p.staleNs {
		return spoutRecycleNow, "AUs arriving but nothing published (frozen destination)"
	}
	return spoutHealthy, ""
}

// onDstGone handles the child's dstgone event: the destination is unusable but the decoder is
// intact, so the parent - not the child - decides. Same action as the staleness oracle.
func (d *ProcDecSession) onDstGone(reason string) {
	if d.recycle != nil {
		d.recycle("child reported dstgone: " + reason)
	}
}

// recycleDest closes + reopens the session with a FRESH destination handle. After decMaxRecycles
// failures the sender is pinned to the frame path: the session fails cleanly (the route
// re-establishes on the ffmpeg decoder) rather than publishing nothing forever.
func (d *ProcDecSession) recycleDest(why string) {
	if d.closed.Load() {
		return
	}
	d.dstMu.Lock()
	if time.Since(d.dstLastMove) < decWatchEvery {
		d.dstMu.Unlock()
		return // one recycle per tick however many detectors fired
	}
	d.dstLastMove = time.Now()
	d.dstRecycles++
	n := d.dstRecycles
	d.dstMu.Unlock()
	d.downgrades.Add(1)
	if n > decMaxRecycles {
		pinDecodeFrames(d.dstName)
		Warnf("mfenc: native decode destination %q unusable after %d reopen attempts (%s) - pinning this sender to the ffmpeg decode path",
			d.dstName, decMaxRecycles, why)
		d.fail("mfenc: native decode destination gone - " + why)
		return
	}
	Warnf("mfenc: native decode destination %q recycling (%s), attempt %d/%d", d.dstName, why, n, decMaxRecycles)
	d.recovering.Store(true)
	defer d.recovering.Store(false)
	closedCh := make(chan struct{})
	d.child.mu.Lock()
	d.child.closeWait[d.sid] = closedCh
	d.child.mu.Unlock()
	if d.child.send(map[string]any{"op": "close", "sid": d.sid}) == nil {
		select {
		case <-closedCh:
		case <-time.After(3 * time.Second):
		}
	}
	// The ring survives the reopen but its contents are stale bitstream the new decoder cannot use
	// without a keyframe: reset both heads so the session starts clean.
	atomic.StoreUint64(d.shm.u64(offInRead), 0)
	atomic.StoreUint64(d.shm.u64(offInWrite), 0)
	ev, err := d.child.openDecSession(d)
	if err != nil || !ev.OK {
		msg := "reopen refused"
		if err != nil {
			msg = err.Error()
		} else if ev.ErrDst != "" {
			msg = ev.ErrDst
		}
		Warnf("mfenc: native decode destination %q reopen failed: %s", d.dstName, msg)
	}
}

// ── frame-path pinning: senders whose native decode proved unusable ──
var (
	pinDecMu  sync.Mutex
	pinnedDec = map[string]bool{}
)

func pinDecodeFrames(name string) {
	if name == "" {
		return
	}
	pinDecMu.Lock()
	pinnedDec[name] = true
	pinDecMu.Unlock()
}

// DecodePinnedToFrames reports whether this destination sender is pinned to the ffmpeg decode path
// (a native decode session on it failed repeatedly). Callers skip the native request entirely.
func DecodePinnedToFrames(name string) bool {
	pinDecMu.Lock()
	defer pinDecMu.Unlock()
	return pinnedDec[name]
}

func (d *ProcDecSession) fail(msg string) {
	d.failMu.Lock()
	if d.failErr == nil {
		d.failErr = errors.New(msg)
	}
	d.failMu.Unlock()
}

func (d *ProcDecSession) failed() error {
	d.failMu.Lock()
	defer d.failMu.Unlock()
	return d.failErr
}

// Close ends the session (the child drains its decoder tail) and releases the shm.
func (d *ProcDecSession) Close() {
	if !d.closed.CompareAndSwap(false, true) {
		return
	}
	closedCh := make(chan struct{})
	d.child.mu.Lock()
	d.child.closeWait[d.sid] = closedCh
	delete(d.child.decs, d.sid)
	d.child.mu.Unlock()
	if d.child.send(map[string]any{"op": "close", "sid": d.sid}) == nil {
		select {
		case <-closedCh:
		case <-time.After(3 * time.Second):
		}
	}
	close(d.done)
	if d.watchDone != nil {
		<-d.watchDone // the watchdog reads the shm header: it must be gone before unmap
	}
	d.shm.close()
}
