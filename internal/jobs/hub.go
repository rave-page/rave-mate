// Package jobs is a module-level fan-out for transcode jobs so a job survives a transient
// subscriber drop (e.g. a Local Studio WS reconnect) and a re-attaching client can replay
// buffered progress and keep receiving live updates. A job ends only on completion,
// explicit cancel, or the orphan grace expiring (no subscriber for orphanTimeout).
//
// Port of the Electron client's transcode-job hub. It wraps the worker supervisor:
// RunStream drives the job; cancelling the per-job context stands in for the Electron
// cancelTranscodeJob. Progress frames are full-state snapshots, so replaying the ring
// buffer to a late subscriber is order-tolerant (latest-wins on render) - a duplicate at
// the attach boundary is harmless.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/worker"
)

const (
	defaultMaxBuffer  = 4000
	defaultDoneRetain = 120 * time.Second // keep a finished job briefly for a late attach
	defaultOrphan     = 120 * time.Second // cancel a running job with no subscriber after this
)

// Runner runs a streaming worker job. *worker.Supervisor satisfies it.
type Runner interface {
	RunStream(ctx context.Context, typ, method string, params any, onProgress worker.ProgressFunc) (json.RawMessage, error)
}

// Frame is one buffered progress event (worker event name + raw JSON snapshot).
type Frame struct {
	Event string
	Data  json.RawMessage
}

// Progress receives live + replayed progress frames.
type Progress func(event string, data json.RawMessage)

// End receives the terminal result exactly once per subscriber.
type End func(EndResult)

// EndResult is a job's terminal state.
type EndResult struct {
	OK       bool
	Canceled bool
	Error    string
	Result   json.RawMessage
}

type sub struct {
	onProgress Progress
	onEnd      End
}

type entry struct {
	jobID   string
	buffer  []Frame
	done    bool
	end     *EndResult
	subs    map[*sub]struct{}
	cancel  context.CancelFunc
	orphanT *time.Timer
	evictT  *time.Timer
}

// Hub owns running transcode jobs and their subscribers.
type Hub struct {
	runner      Runner
	typ, method string

	maxBuffer  int
	doneRetain time.Duration
	orphan     time.Duration

	mu      sync.Mutex
	entries map[string]*entry
}

// New returns a Hub that runs jobs via r (the "transcode"/"transcode.run" worker method).
func New(r Runner) *Hub {
	return &Hub{
		runner:     r,
		typ:        "transcode",
		method:     "transcode.run",
		maxBuffer:  defaultMaxBuffer,
		doneRetain: defaultDoneRetain,
		orphan:     defaultOrphan,
		entries:    map[string]*entry{},
	}
}

// Handle detaches one subscription (idempotent).
type Handle struct {
	once   sync.Once
	detach func()
}

// Detach removes the subscription; if it was the last on a running job, the orphan grace
// timer starts.
func (h *Handle) Detach() {
	if h == nil {
		return
	}
	h.once.Do(h.detach)
}

// Start begins a job and subscribes. The job runs independent of this subscriber. A
// re-Start of a live job id is idempotent: it attaches instead (mirrors the Electron hub).
func (h *Hub) Start(jobID string, params any, onProgress Progress, onEnd End) *Handle {
	h.mu.Lock()
	if e, ok := h.entries[jobID]; ok {
		hd := h.addSubLocked(e, &sub{onProgress, onEnd})
		h.mu.Unlock()
		return hd
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &entry{jobID: jobID, subs: map[*sub]struct{}{}, cancel: cancel}
	h.entries[jobID] = e
	hd := h.addSubLocked(e, &sub{onProgress, onEnd})
	h.mu.Unlock()

	go func() {
		// debuglog.Recover (declared first → runs last) logs + contains the panic. The flagged
		// defer (runs first, before the panic is consumed) emits a terminal End so subscribers
		// don't hang on a dead job, then re-panics so the stack still reaches the debug log.
		ended := false
		defer debuglog.Recover(nil, "jobs", false)
		defer func() {
			if r := recover(); r != nil && !ended {
				h.markDone(e, EndResult{Error: fmt.Sprintf("job panic: %v", r)})
				panic(r)
			}
		}()
		res, err := h.runner.RunStream(ctx, h.typ, h.method, params, func(ev string, data json.RawMessage) {
			h.fanout(e, Frame{Event: ev, Data: append(json.RawMessage(nil), data...)})
		})
		ended = true
		switch {
		case ctx.Err() != nil:
			h.markDone(e, EndResult{Canceled: true, Error: "canceled"})
		case err != nil:
			h.markDone(e, EndResult{Error: err.Error()})
		default:
			h.markDone(e, EndResult{OK: true, Result: res})
		}
	}()
	return hd
}

// AttachResult is returned by Attach. The caller replays Buffer, then receives live frames;
// Done is non-nil when the job already finished.
type AttachResult struct {
	Found  bool
	Buffer []Frame
	Done   *EndResult
	Handle *Handle
}

// Attach re-subscribes to an existing job, snapshotting the progress buffer atomically with
// adding the subscriber (no interleave). Found=false when the job id is unknown/evicted.
func (h *Hub) Attach(jobID string, onProgress Progress, onEnd End) AttachResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[jobID]
	if !ok {
		return AttachResult{}
	}
	buf := append([]Frame(nil), e.buffer...)
	hd := h.addSubLocked(e, &sub{onProgress, onEnd})
	return AttachResult{Found: true, Buffer: buf, Done: e.end, Handle: hd}
}

// Cancel cancels a running job; its goroutine settles and fires End to all subscribers.
func (h *Hub) Cancel(jobID string) {
	h.mu.Lock()
	var c context.CancelFunc
	if e, ok := h.entries[jobID]; ok {
		c = e.cancel
	}
	h.mu.Unlock()
	if c != nil {
		c()
	}
}

// CancelAll cancels every tracked job.
func (h *Hub) CancelAll() {
	h.mu.Lock()
	cs := make([]context.CancelFunc, 0, len(h.entries))
	for _, e := range h.entries {
		cs = append(cs, e.cancel)
	}
	h.mu.Unlock()
	for _, c := range cs {
		c()
	}
}

// ── internals (locking discipline: mutate state under h.mu, invoke callbacks outside it
// to avoid re-entrancy deadlock when a callback calls Detach/Cancel) ──────────────────

func (h *Hub) addSubLocked(e *entry, s *sub) *Handle {
	e.subs[s] = struct{}{}
	if e.orphanT != nil {
		e.orphanT.Stop()
		e.orphanT = nil
	}
	return &Handle{detach: func() { h.detach(e, s) }}
}

func (h *Hub) detach(e *entry, s *sub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(e.subs, s)
	if len(e.subs) == 0 && !e.done {
		e.orphanT = time.AfterFunc(h.orphan, func() {
			h.mu.Lock()
			orphaned := len(e.subs) == 0 && !e.done
			cancel := e.cancel
			h.mu.Unlock()
			if orphaned {
				cancel()
			}
		})
	}
}

func (h *Hub) fanout(e *entry, f Frame) {
	h.mu.Lock()
	if e.done {
		h.mu.Unlock()
		return
	}
	e.buffer = append(e.buffer, f)
	if len(e.buffer) > h.maxBuffer {
		e.buffer = e.buffer[len(e.buffer)-h.maxBuffer:]
	}
	subs := snapshot(e)
	h.mu.Unlock()
	for _, s := range subs {
		safeProgress(s.onProgress, f)
	}
}

func (h *Hub) markDone(e *entry, r EndResult) {
	h.mu.Lock()
	if e.done {
		h.mu.Unlock()
		return
	}
	e.done = true
	e.end = &r
	if e.orphanT != nil {
		e.orphanT.Stop()
		e.orphanT = nil
	}
	e.evictT = time.AfterFunc(h.doneRetain, func() {
		h.mu.Lock()
		delete(h.entries, e.jobID)
		h.mu.Unlock()
	})
	subs := snapshot(e)
	h.mu.Unlock()
	for _, s := range subs {
		safeEnd(s.onEnd, r)
	}
}

func snapshot(e *entry) []*sub {
	out := make([]*sub, 0, len(e.subs))
	for s := range e.subs {
		out = append(out, s)
	}
	return out
}

func safeProgress(f Progress, fr Frame) {
	defer func() { _ = recover() }()
	if f != nil {
		f(fr.Event, fr.Data)
	}
}

func safeEnd(f End, r EndResult) {
	defer func() { _ = recover() }()
	if f != nil {
		f(r)
	}
}
