package jobs

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/worker"
)

// fakeRunner emits the given progress frames, then blocks until finish receives an error
// (nil = success) or the context is canceled.
type fakeRunner struct {
	progress []json.RawMessage
	finish   chan error
	result   json.RawMessage
	started  chan struct{}
	canceled chan struct{} // closed when RunStream returns due to ctx cancellation
}

func (f *fakeRunner) RunStream(ctx context.Context, _, _ string, _ any, onP worker.ProgressFunc) (json.RawMessage, error) {
	if f.started != nil {
		close(f.started)
	}
	for _, d := range f.progress {
		onP("progress", d)
	}
	select {
	case err := <-f.finish:
		return f.result, err
	case <-ctx.Done():
		if f.canceled != nil {
			close(f.canceled)
		}
		return nil, ctx.Err()
	}
}

func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

type collector struct {
	mu       sync.Mutex
	progress []string
	end      *EndResult
}

func (c *collector) onProgress(_ string, d json.RawMessage) {
	c.mu.Lock()
	c.progress = append(c.progress, string(d))
	c.mu.Unlock()
}
func (c *collector) onEnd(r EndResult) { c.mu.Lock(); c.end = &r; c.mu.Unlock() }
func (c *collector) ended() bool       { c.mu.Lock(); defer c.mu.Unlock(); return c.end != nil }
func (c *collector) progressLen() int  { c.mu.Lock(); defer c.mu.Unlock(); return len(c.progress) }

func TestStartProgressAndDone(t *testing.T) {
	fr := &fakeRunner{progress: []json.RawMessage{[]byte(`{"percent":10}`), []byte(`{"percent":50}`)}, finish: make(chan error, 1), result: []byte(`{"ok":true}`)}
	h := New(fr)
	c := &collector{}
	h.Start("j1", nil, c.onProgress, c.onEnd)
	waitFor(t, func() bool { return c.progressLen() == 2 })
	fr.finish <- nil
	waitFor(t, c.ended)
	if !c.end.OK || c.end.Canceled {
		t.Fatalf("end = %+v", c.end)
	}
}

func TestAttachReplaysBufferThenLive(t *testing.T) {
	fr := &fakeRunner{progress: []json.RawMessage{[]byte(`{"percent":25}`)}, finish: make(chan error, 1), started: make(chan struct{})}
	h := New(fr)
	first := &collector{}
	h.Start("j1", nil, first.onProgress, first.onEnd)
	<-fr.started
	waitFor(t, func() bool { return first.progressLen() == 1 })

	// Late attach replays the one buffered frame.
	late := &collector{}
	res := h.Attach("j1", late.onProgress, late.onEnd)
	if !res.Found || len(res.Buffer) != 1 || res.Done != nil {
		t.Fatalf("attach result: found=%v buf=%d done=%v", res.Found, len(res.Buffer), res.Done)
	}
	fr.finish <- nil
	waitFor(t, late.ended)
	if !late.end.OK {
		t.Fatalf("late end = %+v", late.end)
	}
}

func TestAttachUnknown(t *testing.T) {
	h := New(&fakeRunner{finish: make(chan error, 1)})
	if r := h.Attach("nope", nil, nil); r.Found {
		t.Fatal("unknown job should not be found")
	}
}

func TestAttachAfterDoneSeesResult(t *testing.T) {
	fr := &fakeRunner{finish: make(chan error, 1), result: []byte(`{"ok":true}`)}
	h := New(fr)
	h.doneRetain = time.Second
	c := &collector{}
	h.Start("j1", nil, c.onProgress, c.onEnd)
	fr.finish <- nil
	waitFor(t, c.ended)
	res := h.Attach("j1", nil, nil)
	if !res.Found || res.Done == nil || !res.Done.OK {
		t.Fatalf("attach after done: %+v", res)
	}
}

func TestOrphanCancels(t *testing.T) {
	fr := &fakeRunner{finish: make(chan error, 1), canceled: make(chan struct{})}
	h := New(fr)
	h.orphan = 15 * time.Millisecond
	c := &collector{}
	hd := h.Start("j1", nil, c.onProgress, c.onEnd)
	hd.Detach() // last subscriber gone → orphan timer cancels the job
	waitFor(t, func() bool { return closed(fr.canceled) })
	// The runner heard the cancel, but the hub records the canceled END asynchronously after the job
	// goroutine settles - wait for it (avoids the flake where Attach raced ahead of the recorded end).
	waitFor(t, func() bool {
		r := h.Attach("j1", nil, nil)
		if r.Handle != nil {
			r.Handle.Detach()
		}
		return r.Done != nil
	})
	// The detached subscriber correctly hears nothing; a fresh attach sees the canceled end.
	res := h.Attach("j1", nil, nil)
	if !res.Found || res.Done == nil || !res.Done.Canceled {
		t.Fatalf("orphaned job should be canceled, got %+v", res)
	}
}

func TestReattachBeforeOrphanKeepsAlive(t *testing.T) {
	fr := &fakeRunner{finish: make(chan error, 1), canceled: make(chan struct{})}
	h := New(fr)
	h.orphan = 40 * time.Millisecond
	hd := h.Start("j1", nil, func(string, json.RawMessage) {}, func(EndResult) {})
	hd.Detach()
	// Re-attach quickly with a real subscriber; the orphan timer must be cancelled.
	c := &collector{}
	res := h.Attach("j1", c.onProgress, c.onEnd)
	time.Sleep(100 * time.Millisecond)
	if closed(fr.canceled) || c.ended() {
		t.Fatal("job cancelled despite a live re-attach")
	}
	_ = res
	fr.finish <- nil
	waitFor(t, c.ended)
	if !c.end.OK {
		t.Fatalf("expected ok end, got %+v", c.end)
	}
}

func TestCancel(t *testing.T) {
	fr := &fakeRunner{finish: make(chan error, 1)}
	h := New(fr)
	c := &collector{}
	h.Start("j1", nil, c.onProgress, c.onEnd)
	h.Cancel("j1")
	waitFor(t, c.ended)
	if !c.end.Canceled {
		t.Fatalf("cancel → canceled end, got %+v", c.end)
	}
}

func TestBufferCap(t *testing.T) {
	frames := make([]json.RawMessage, 50)
	for i := range frames {
		frames[i] = []byte(`{"percent":1}`)
	}
	fr := &fakeRunner{progress: frames, finish: make(chan error, 1)}
	h := New(fr)
	h.maxBuffer = 10
	c := &collector{}
	h.Start("j1", nil, c.onProgress, c.onEnd)
	waitFor(t, func() bool { return c.progressLen() == 50 }) // all delivered live
	res := h.Attach("j1", nil, nil)
	if len(res.Buffer) != 10 { // but the ring keeps only the last maxBuffer
		t.Fatalf("buffer cap: got %d want 10", len(res.Buffer))
	}
	fr.finish <- nil
}

// compile-time: *worker.Supervisor satisfies Runner.
var _ Runner = (*worker.Supervisor)(nil)
