package videoshare

import (
	"sync/atomic"
	"time"
)

// handoff.go - pixel-OWNERSHIP handoff from a caller to a backend worker that owns a GL context on
// a locked OS thread (SendImage must run on the context's thread, so a worker is unavoidable).
//
// Both Sender.Send and FrameSender.Send document "owned by the caller after return", and every
// medialink.Sink is contractually forbidden from retaining Payload past Write - internal/mediapipe's
// decoder recycles its raw-frame buffer the instant Write returns (decode.go pumpFrames). The old
// fire-and-forget cap-1 queue therefore handed a buffer to a cgo worker that read it LATER, while
// the decoder was already filling it with the next frame: torn frames at 4K, and the frame a
// newer one displaced was dropped without telling its producer anything at all.
//
// Send now waits until the worker has finished READING the pixels.
//
// Bound: exactly ONE frame in flight per worker channel (cap 1). A frame displaced by a newer one
// is reclaimed immediately (its producer returns, counted as not-sent). A frame still QUEUED when
// the budget expires is reclaimed by the sender and dropped. A frame the worker has already
// CLAIMED cannot be reclaimed - it holds the pointer - so the wait continues and the stall is
// reported once; that is the same exposure as the bounded close-join, and the only memory-safe
// answer to a worker wedged in a driver call.
const handoffBudget = 2 * time.Second

// job state: queued → reading (worker claims) → finished. Only the transition winner closes done.
const (
	jobQueued int32 = iota
	jobReading
	jobFinished
)

// frameJob is one pixel buffer in flight to a worker, plus its completion signal.
type frameJob struct {
	pix    []byte // the caller's buffer: valid until done is closed
	w, h   int
	state  atomic.Int32
	readOK atomic.Bool // the worker really consumed the pixels (vs displaced/expired)
	done   chan struct{}
}

func newFrameJob(pix []byte, w, h int) *frameJob {
	return &frameJob{pix: pix, w: w, h: h, done: make(chan struct{})}
}

// claim marks the job as being read; false = someone already reclaimed it (skip it).
func (j *frameJob) claim() bool { return j.state.CompareAndSwap(jobQueued, jobReading) }

// finish releases the producer after the worker is done touching pix.
func (j *frameJob) finish(read bool) {
	j.readOK.Store(read)
	if j.state.CompareAndSwap(jobReading, jobFinished) {
		close(j.done)
	}
}

// reclaim takes an UNCLAIMED job back (displaced by a newer frame, or the budget expired) and
// releases its producer. false = the worker already holds the pointer: only it may finish.
func (j *frameJob) reclaim() bool {
	if j.state.CompareAndSwap(jobQueued, jobFinished) {
		close(j.done)
		return true
	}
	return false
}

// deliverJob pushes j onto a cap-1 channel, reclaiming any frame it displaces (newest-wins) so no
// producer is ever left waiting on a frame that will never be read.
func deliverJob(ch chan *frameJob, j *frameJob) {
	for {
		select {
		case ch <- j:
			return
		default:
			select {
			case old := <-ch:
				old.reclaim()
			default:
			}
		}
	}
}

// handoff delivers pix to a worker and blocks until the pixels have been read (or the frame was
// dropped unread). Returns whether the worker consumed them. stuck is called at most once, when
// the wait exceeds the budget with the worker already holding the pointer.
func handoff(ch chan *frameJob, pix []byte, w, h int, budget time.Duration, stuck func()) bool {
	j := newFrameJob(pix, w, h)
	deliverJob(ch, j)
	tm := time.NewTimer(budget)
	defer tm.Stop()
	select {
	case <-j.done:
		return j.readOK.Load()
	case <-tm.C:
	}
	if j.reclaim() {
		return false // never picked up: walking away cannot race a reader
	}
	if stuck != nil {
		stuck()
	}
	<-j.done // the worker has the pointer; returning here would be a use-after-recycle
	return j.readOK.Load()
}
