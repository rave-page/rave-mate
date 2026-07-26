package videoshare

import (
	"sync"
	"testing"
	"time"
)

// handoff_test.go - proves the pixel-ownership contract by execution. The oracle is a CANARY the
// producer stamps into its buffer and then overwrites the moment Send returns (exactly what
// mediapipe's decoder does: pumpFrames recycles the frame buffer as soon as Write returns). A
// worker that reads the buffer later must still see the canary.
//
// TestHandoffCanaryTornByFireAndForget is the non-vacuity arm: the OLD semantics (deliver onto a
// cap-1 channel and return) tears under the same oracle. If that arm ever stops failing, this
// harness has stopped detecting the bug and the other arms are worthless.

const (
	canary = 0xAB
	recycl = 0xCD
)

// asyncWorker reads jobs after a delay, like a cgo SendImage on a locked OS thread.
type asyncWorker struct {
	mu   sync.Mutex
	seen []byte // first byte of each frame as the worker saw it
	lag  time.Duration
	ack  bool // finish the job (new semantics) vs never ack (old semantics)
	wg   sync.WaitGroup
}

func (w *asyncWorker) run(ch chan *frameJob, stop chan struct{}) {
	defer w.wg.Done()
	for {
		select {
		case <-stop:
			return
		case j := <-ch:
			if !j.claim() {
				continue
			}
			time.Sleep(w.lag) // the driver call takes time; the producer must not recycle yet
			w.mu.Lock()
			w.seen = append(w.seen, j.pix[0])
			w.mu.Unlock()
			if w.ack {
				j.finish(true)
			}
		}
	}
}

func (w *asyncWorker) frames() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.seen...)
}

// TestHandoffWaitsForWorkerRead: with the ack contract the worker always sees the canary.
func TestHandoffWaitsForWorkerRead(t *testing.T) {
	ch := make(chan *frameJob, 1)
	stop := make(chan struct{})
	w := &asyncWorker{lag: 5 * time.Millisecond, ack: true}
	w.wg.Add(1)
	go w.run(ch, stop)
	defer func() { close(stop); w.wg.Wait() }()

	buf := make([]byte, 64)
	for i := 0; i < 8; i++ {
		for j := range buf {
			buf[j] = canary
		}
		if !handoff(ch, buf, 4, 4, handoffBudget, nil) {
			t.Fatalf("frame %d: worker did not consume the pixels", i)
		}
		for j := range buf { // the producer recycles the instant Send returns
			buf[j] = recycl
		}
	}
	got := w.frames()
	if len(got) != 8 {
		t.Fatalf("worker saw %d frames, want 8", len(got))
	}
	for i, b := range got {
		if b != canary {
			t.Fatalf("frame %d torn: worker read 0x%02X, want the canary 0x%02X", i, b, canary)
		}
	}
}

// TestHandoffCanaryTornByFireAndForget: the pre-fix path (queue + return) reads recycled bytes.
func TestHandoffCanaryTornByFireAndForget(t *testing.T) {
	ch := make(chan *frameJob, 1)
	stop := make(chan struct{})
	w := &asyncWorker{lag: 5 * time.Millisecond, ack: true}
	w.wg.Add(1)
	go w.run(ch, stop)
	defer func() { close(stop); w.wg.Wait() }()

	buf := make([]byte, 64)
	for j := range buf {
		buf[j] = canary
	}
	deliverJob(ch, newFrameJob(buf, 4, 4)) // old semantics: hand off and walk away
	for j := range buf {
		buf[j] = recycl
	}
	deadline := time.Now().Add(time.Second)
	for len(w.frames()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := w.frames()
	if len(got) != 1 {
		t.Fatalf("worker saw %d frames, want 1", len(got))
	}
	if got[0] != recycl {
		t.Fatalf("fire-and-forget read 0x%02X - the canary oracle no longer detects the torn read", got[0])
	}
}

// TestHandoffDisplacedFrameReleasesProducer: a frame a newer one displaces must not leave its
// producer waiting out the whole budget (the old deliver dropped it silently).
func TestHandoffDisplacedFrameReleasesProducer(t *testing.T) {
	ch := make(chan *frameJob, 1)
	a := newFrameJob(make([]byte, 4), 1, 1)
	deliverJob(ch, a)
	done := make(chan bool, 1)
	go func() { done <- handoff(ch, make([]byte, 4), 1, 1, handoffBudget, nil) }()
	select {
	case <-a.done:
	case <-time.After(time.Second):
		t.Fatal("displaced frame never released its producer")
	}
	if a.readOK.Load() {
		t.Fatal("displaced frame reported as read")
	}
	// the displacing frame is still queued: budget-expiry must reclaim it, not block forever
	if got := handoffReclaimQueued(ch); !got {
		t.Fatal("queued frame could not be reclaimed")
	}
	select {
	case <-done:
	case <-time.After(3 * handoffBudget):
		t.Fatal("handoff never returned for an unread queued frame")
	}
}

// handoffReclaimQueued drains + reclaims whatever is queued (test helper: no worker exists).
func handoffReclaimQueued(ch chan *frameJob) bool {
	select {
	case j := <-ch:
		return j.reclaim()
	default:
		return false
	}
}

// TestHandoffBudgetExpiryDropsUnclaimed: no worker at all - Send returns within the budget and
// reports the frame as not consumed, instead of blocking a route forever.
func TestHandoffBudgetExpiryDropsUnclaimed(t *testing.T) {
	ch := make(chan *frameJob, 1)
	start := time.Now()
	if handoff(ch, make([]byte, 4), 1, 1, 20*time.Millisecond, nil) {
		t.Fatal("reported consumed with no worker running")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("budget expiry took %v", d)
	}
}

// TestHandoffWaitsOutAClaimedFrame: once the worker holds the pointer the budget must NOT let the
// producer walk away - that would reopen the use-after-recycle the ack exists to close.
func TestHandoffWaitsOutAClaimedFrame(t *testing.T) {
	ch := make(chan *frameJob, 1)
	stop := make(chan struct{})
	w := &asyncWorker{lag: 120 * time.Millisecond, ack: true}
	w.wg.Add(1)
	go w.run(ch, stop)
	defer func() { close(stop); w.wg.Wait() }()

	var stuckN int
	buf := make([]byte, 8)
	for i := range buf {
		buf[i] = canary
	}
	if !handoff(ch, buf, 2, 1, 10*time.Millisecond, func() { stuckN++ }) {
		t.Fatal("a claimed frame must be waited out, not abandoned")
	}
	if stuckN != 1 {
		t.Fatalf("stuck reported %d times, want exactly 1", stuckN)
	}
	if got := w.frames(); len(got) != 1 || got[0] != canary {
		t.Fatalf("worker read %v, want one canary frame", got)
	}
}
