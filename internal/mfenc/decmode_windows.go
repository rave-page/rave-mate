package mfenc

import (
	"sync/atomic"
	"time"

	"rave.page/mate/internal/decmode"
)

// protoVerSWDec is the minimum child hello.ver that understands dir:"dec" sw/threads/keygate.
const protoVerSWDec = 6

// modeState is the per-session software-decode capacity sample (watchDest goroutine only): decoded
// frames and child busy time at the last tick, folded into capacity fps per coded height so the CPU
// cap is chosen from data (decmode.CapHeight). Bounded: at most one entry per coded height bucket.
type modeState struct {
	prevFrames, prevBusy uint64
	measured             map[int]float64 // coded height -> sustainable decode fps (EMA)
	capH                 int
}

// modeTick runs one adaptive-mode policy step (1 per decWatchEvery, off the frame path).
func (d *ProcDecSession) modeTick() {
	if d.mode == nil || d.closed.Load() {
		return
	}
	now := time.Now()
	h := DecHeadroom()
	hw := DecVerdict{Allow: true}
	if DecAdmit != nil {
		hw = DecAdmit(d.inW, d.inH, d.outW, d.outH, int(d.fps), d.hevc)
	}
	d.dstMu.Lock()
	held := d.dstHeld
	d.dstMu.Unlock()
	cpuNeed := uint64(CPUDecodeCostMB(d.inW, d.inH) + 0.5)
	d.sampleCapacity()
	in := modeInput(now, d.mode.Mode(), h.FreeMB, h.Present, hw, held, cpuNeed)
	if sw := d.mode.Tick(in); sw != nil {
		ok, why := d.switchMode(sw.To, sw.Reason)
		d.mode.Done(time.Now(), ok, why)
	}
	d.publishModeStatus(h.FreeMB, in.HWNeedMB, cpuNeed)
}

// sampleCapacity folds the child's decode busy time into a sustainable-fps estimate for the current
// coded height while in software mode (frames / busy seconds; EMA 0.3).
func (d *ProcDecSession) sampleCapacity() {
	frames := atomic.LoadUint64(d.shm.u64(offDecFrames))
	busy := atomic.LoadUint64(d.shm.u64(offDecBusy))
	reset := frames < d.modeSt.prevFrames || busy < d.modeSt.prevBusy // counters restart on a reopen
	df, db := frames-d.modeSt.prevFrames, busy-d.modeSt.prevBusy
	d.modeSt.prevFrames, d.modeSt.prevBusy = frames, busy
	if reset || !d.software.Load() || df < 10 || db == 0 {
		return
	}
	coded := atomic.LoadUint64(d.shm.u64(offDecCoded))
	ch := int(uint32(coded & 0xFFFFFFFF))
	if ch <= 0 {
		return
	}
	fps := float64(df) / (float64(db) / 1e9)
	b := bucketHeight(ch)
	if d.modeSt.measured == nil {
		d.modeSt.measured = map[int]float64{}
	}
	if old, ok := d.modeSt.measured[b]; ok {
		fps = 0.7*old + 0.3*fps
	}
	d.modeSt.measured[b] = fps
}

// bucketHeight maps a coded height to the CapHeight candidates (1080/720/540).
func bucketHeight(h int) int {
	switch {
	case h > 900:
		return 1080
	case h > 630:
		return 720
	default:
		return 540
	}
}

// switchMode executes a decode-mode switch on this session: close + reopen in the new mode on the same
// child session (keyframe-gated), CPU-mode sender cap set/lifted. A failed open restores the previous
// mode (reopened, so the route keeps a decoder). The destination is never touched.
func (d *ProcDecSession) switchMode(to decmode.Mode, reason string) (bool, string) {
	if to == decmode.Held {
		// The destination itself is gone: nothing to decode into. Keep the session idle; the route's
		// HELD admission owns recovery. (Not reached while DestOK is always true here.)
		return true, reason
	}
	prev := d.software.Load()
	next := to == decmode.CPU
	if prev == next {
		return true, reason
	}
	Warnf("mfenc: decode %q switching %s -> %s: %s", d.dstName, modeName(prev), modeName(next), reason)
	d.software.Store(next)
	d.applyCap(next)
	NoteDecodeResizeGrace() // coded size will change: a publish stall now is expected, not a wedge
	ev, err := d.reopenPipeline()
	if err == nil && ev.OK {
		d.requestKeyframe() // fresh decoder: ask for an IDR now, never wait out a GOP
		d.dstMu.Lock()
		d.dstHeld, d.dstRecycles = false, 0
		d.dstMu.Unlock()
		d.downgrades.Add(1)
		return true, reason
	}
	msg := "reopen refused"
	if err != nil {
		msg = err.Error()
	} else if ev.ErrDst != "" {
		msg = ev.ErrDst
	}
	Warnf("mfenc: decode %q switch to %s failed (%s) - restoring %s", d.dstName, modeName(next), msg, modeName(prev))
	d.software.Store(prev)
	d.applyCap(prev)
	if ev2, err2 := d.reopenPipeline(); err2 == nil && ev2.OK {
		d.requestKeyframe()
	} else {
		d.fail("mfenc: decode mode restore failed after a failed switch: " + msg)
	}
	return false, msg
}

// applyCap sets (software) or lifts (hardware) the sender coded-height cap for this destination.
func (d *ProcDecSession) applyCap(software bool) {
	if !software {
		d.modeSt.capH = 0
		d.capH.Store(0)
		return
	}
	d.modeSt.capH = decmode.CapHeight(decmode.CPU, int(d.fps+0.5), d.modeSt.measured)
	d.capH.Store(int32(d.modeSt.capH))
}

// publishModeStatus refreshes the visible state (and, in software mode, re-derives the cap from the
// latest capacity measurement - one bucket step at a time through CapHeight's thresholds).
func (d *ProcDecSession) publishModeStatus(free, hwNeed, cpuNeed uint64) {
	if d.mode == nil {
		return
	}
	if d.software.Load() {
		d.applyCap(true)
	}
	publishMode(DecodeModeStatus{Dest: d.dstName, Status: d.mode.Status(), CapH: d.modeSt.capH,
		FreeMB: free, HWNeed: hwNeed, CPUNeed: cpuNeed})
}

func modeName(software bool) string {
	if software {
		return "cpu"
	}
	return "hw"
}

// CodedCapHeight is this session's CPU-mode coded-height cap for ITS route's sender (0 = none).
func (d *ProcDecSession) CodedCapHeight() int { return int(d.capH.Load()) }

// SetKeyframeRequester installs the route's keyframe request (PLI); called once by the route.
func (d *ProcDecSession) SetKeyframeRequester(f func()) {
	if f != nil {
		d.kfReq.Store(&f)
	}
}

// requestKeyframe asks the sender for an IDR (no-op without a requester).
func (d *ProcDecSession) requestKeyframe() {
	if p := d.kfReq.Load(); p != nil {
		(*p)()
	}
}
