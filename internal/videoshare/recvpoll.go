package videoshare

import "time"

// recvpoll.go - receive poll-loop state machine, cgo-free so it is testable untagged
// (receiver_spout.go drives it). Owns three decisions per poll tick:
//  1. FPS gate BEFORE ReceiveImage, connected or not. Every ReceiveImage runs
//     ReceiveSenderData → OpenSharedResource → IDXGIKeyedMutex::AcquireSync against the
//     PRODUCER's shared texture; 250 acquisitions/s serialize against the sending app's
//     and DWM's GPU submissions (system-wide pointer lag on the sender PC).
//  2. Buffer (re)size - one prompt pass when a real sender (re)appears.
//  3. Fast→idle interval backoff: only real activity (a frame, or a sender update with
//     usable dims) re-arms the 4ms poll. A stale/0x0 sender - the shim reporting
//     "needs (re)size" forever - must reach the 50ms idle poll, not spin at 250 Hz.

const (
	recvPollEvery = 4 * time.Millisecond  // ~250 Hz poll while frames flow; IsFrameNew gates actual work
	recvPollIdle  = 50 * time.Millisecond // backed-off poll once the sender goes quiet (no activity for recvIdleAfter)
	recvIdleAfter = 2 * time.Second       // quiet period before backing off (reconnect latency ≤ recvPollIdle)
)

// rave_spout_recv result codes (keep in sync with spout_shim.cpp).
const (
	recvErr      = -1 // ReceiveImage failed / no sender
	recvNoFrame  = 0  // connected, no new frame
	recvFrame    = 1  // new frame delivered into the buffer
	recvUpdated  = 2  // sender (re)connected or resized (IsUpdated) - real sender activity
	recvNeedSize = 3  // buffer absent/undersized, NO sender update - resize quietly
)

// recvAction is what one poll result asks the cgo loop to do.
type recvAction struct {
	resize   bool          // swap in a sizeBytes buffer (old one goes back to the pool)
	size     int           // bytes for the resize (validated: 0 unless resize)
	frame    bool          // deliver the filled buffer as a frame
	badGeom  bool          // shim reported an implausible w×h - refused, retry later
	interval time.Duration // poll interval after this result
}

// recvPoller folds shim results into interval + buffer decisions. One poll goroutine
// owns it; only the embedded gate is cross-goroutine (SetMaxFPS).
type recvPoller struct {
	gate     *fpsGate
	interval time.Duration
	lastAct  time.Time // last real sender activity (frame, or update with usable dims)
}

func newRecvPoller(gate *fpsGate, now time.Time) *recvPoller {
	return &recvPoller{gate: gate, interval: recvPollEvery, lastAct: now}
}

// allow reports whether this tick may call ReceiveImage at all. Applied BEFORE the
// readback and regardless of connection state - len(buf)==0 used to bypass the gate,
// keeping an unconnected receiver at the uncapped fast poll forever.
func (p *recvPoller) allow(now time.Time) bool {
	return p.gate == nil || p.gate.allow(now.UnixNano())
}

// apply folds one shim result into the state machine and returns the actions to take.
//
// Geometry is VALIDATED here, never trusted: the shim reads the sender's shared-memory info
// and can return torn values while a large shared texture is still being created (a
// 3840×2160 sender was observed reporting w=139846784 h=3840 on the first poll). Sizing a
// buffer from that allocated 2 TB and killed the media child with "runtime: cannot allocate
// memory". Implausible dims are refused (badGeom) and count as NO activity, so the poller
// backs off to the idle interval and retries until the sender info is coherent.
func (p *recvPoller) apply(code, w, h, bufLen int, now time.Time) recvAction {
	size, usable := FrameBytes(w, h)
	var act recvAction
	switch code {
	case recvFrame:
		act.frame = true
	case recvUpdated, recvNeedSize:
		act.badGeom = !usable
		if usable && bufLen != size {
			act.resize, act.size = true, size
		}
	}
	// recvNeedSize is NOT activity: a stale sender makes the shim report it on every
	// poll, and treating that as fresh frames is what kept the 250 Hz poll armed forever.
	if code == recvFrame || (code == recvUpdated && usable) {
		p.lastAct = now
		p.interval = recvPollEvery
	} else if p.interval != recvPollIdle && now.Sub(p.lastAct) > recvIdleAfter {
		p.interval = recvPollIdle
	}
	act.interval = p.interval
	return act
}
