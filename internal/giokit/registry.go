package giokit

import (
	"image"
	"sort"
	"sync"
)

// Registry captures the labeled widget tree during layout so the control plane (ctl)
// can enumerate widgets + synthesize activations on a Gio surface - the Gio counterpart
// of the Fyne ctl snapshot/tap machinery.
//
// Protocol (all layout-side calls happen on the window's event-loop goroutine):
//   - Window host: BeginFrame → frame(gtx) → EndFrame each frame.
//   - Containers that translate their children (op.Offset) bracket the children with
//     PushOffset/PopOffset so Add records window-absolute bounds. giokit containers do
//     this already; custom layout code that offsets registrable widgets must too.
//   - Widgets: Add(kind, label, size, activate) at their layout position.
//   - Control plane (any goroutine): Snapshot() lists the last completed frame;
//     Activate(label) queues the widget's activation - executed at the next BeginFrame
//     on the UI goroutine (a queued widget.Clickable.Click() etc.), then the window is
//     invalidated so the frame actually runs.
//
// Wiring a ctl verb: the instance server resolves the target Gio window's Registry,
// answers `snapshot` from Snapshot(), and `tap <label>` via Activate(label). Not wired
// yet - see GIO_MIGRATION.md.
type Registry struct {
	mu         sync.Mutex
	cur        []Node // frame being built
	curAct     map[string]func()
	last       []Node // last completed frame (Snapshot source)
	lastAct    map[string]func()
	stack      []image.Point // absolute-origin stack; empty = origin (0,0)
	pending    []string      // labels queued for activation at next BeginFrame
	invalidate func()
}

// Node is one registered widget occurrence in a frame.
type Node struct {
	Kind   string          // "button", "toggle", "slider", "label", …
	Label  string          // stable accessible name (activation key)
	Bounds image.Rectangle // window coordinates (px)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// SetInvalidate wires the window's Invalidate so queued activations trigger a frame.
func (r *Registry) SetInvalidate(fn func()) {
	r.mu.Lock()
	r.invalidate = fn
	r.mu.Unlock()
}

// BeginFrame resets the building frame and runs queued activations (UI goroutine).
func (r *Registry) BeginFrame() {
	r.mu.Lock()
	pend := r.pending
	r.pending = nil
	acts := r.lastAct
	r.cur = r.cur[:0]
	r.curAct = make(map[string]func())
	r.stack = r.stack[:0]
	r.mu.Unlock()
	for _, label := range pend {
		if fn := acts[label]; fn != nil {
			fn()
		}
	}
}

// EndFrame publishes the built frame for Snapshot/Activate.
func (r *Registry) EndFrame() {
	r.mu.Lock()
	r.last = append(r.last[:0], r.cur...)
	r.lastAct = r.curAct
	r.mu.Unlock()
}

// PushOffset enters a container translated by delta (relative to the current origin).
// Nil-receiver safe.
func (r *Registry) PushOffset(delta image.Point) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.stack = append(r.stack, r.origin().Add(delta))
	r.mu.Unlock()
}

// PopOffset leaves the innermost container. Nil-receiver safe.
func (r *Registry) PopOffset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if n := len(r.stack); n > 0 {
		r.stack = r.stack[:n-1]
	}
	r.mu.Unlock()
}

// origin returns the current absolute origin (mu held).
func (r *Registry) origin() image.Point {
	if n := len(r.stack); n > 0 {
		return r.stack[n-1]
	}
	return image.Point{}
}

// Add records a widget of size at the current origin. activate (nil ok) is what a
// synthesized tap runs on the UI goroutine. Nil-receiver safe (unregistered surfaces).
func (r *Registry) Add(kind, label string, size image.Point, activate func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	o := r.origin()
	r.cur = append(r.cur, Node{Kind: kind, Label: label, Bounds: image.Rectangle{Min: o, Max: o.Add(size)}})
	if activate != nil && label != "" {
		if r.curAct == nil {
			r.curAct = make(map[string]func())
		}
		r.curAct[label] = activate
	}
	r.mu.Unlock()
}

// Snapshot returns the last completed frame's nodes, sorted top-to-bottom, left-to-right.
func (r *Registry) Snapshot() []Node {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	out := append([]Node(nil), r.last...)
	r.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bounds.Min.Y != out[j].Bounds.Min.Y {
			return out[i].Bounds.Min.Y < out[j].Bounds.Min.Y
		}
		return out[i].Bounds.Min.X < out[j].Bounds.Min.X
	})
	return out
}

// Activate queues label's activation for the next frame and invalidates the window.
// Returns false if the last frame had no activatable widget with that label.
func (r *Registry) Activate(label string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	_, ok := r.lastAct[label]
	if ok {
		r.pending = append(r.pending, label)
	}
	inval := r.invalidate
	r.mu.Unlock()
	if ok && inval != nil {
		inval()
	}
	return ok
}
