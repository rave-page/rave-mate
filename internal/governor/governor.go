// Package governor is rave-mate's activity governor: the single source of truth for "is now a bad
// time to spend CPU/GPU on non-essential work". It exists so rave-mate is a GOOD NEIGHBOUR by
// DEFAULT - it must never impair other apps on the machine (notably a live OBS/NVENC stream), with
// zero configuration.
//
// It reacts to two signal families:
//   - UI visibility: the window is focused / minimized / mid size-move (drag/resize).
//   - Streaming: an OBS stream is live on this machine (detected elsewhere; fed via SetStreaming).
//
// Consumers gate on it:
//   - UIAnimAllowed()   -> run the ~1 Hz webui graph/tick refresh? (paused when hidden/dragging/streaming)
//   - BackgroundAllowed()-> run heavy non-essential batch work? (fingerprinting/indexing/hydration:
//     paused while a stream is live OR the window is mid drag/resize; deferred work is re-run once
//     it's allowed again)
//   - WaitWhileBusy(ctx) -> block a long in-proc sweep loop in short slices while the above is false.
//
// It also right-sizes THIS process's Windows priority class: below-normal whenever a stream is live,
// the window isn't the user's focus, OR the user is mid drag/resize, normal otherwise - so an in-proc
// worker goroutine can never out-schedule OBS's (elevated) audio-encoder thread, nor starve the
// (software-composited) WebView2 window during a drag. Stream-critical paths (Spout out, peerlink
// media, MIDI/now-playing, overlays) are NOT gated here - they run in their own children (the media
// plane included, isolated by default since #44/WP-6) and keep feeding the stream.
//
// Two carve-outs to keep honest:
//   - `features.mediaLink.subprocess: false` puts the media plane back IN this process, where the
//     below-normal demotion above DOES apply to live routes. That is the legacy path, kept only as an
//     escape hatch; a route running under it will be de-prioritized while a stream is live.
//   - Realtime children (medialink encode/decode) are additionally kept out of the CPU-capped
//     background job object - see sysexec.JobClass.
package governor

import (
	"context"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/sysexec"
)

// Signals is an immutable snapshot of the governor's inputs.
type Signals struct {
	Focused   bool // rave-mate window is the foreground/active window
	Minimized bool // rave-mate window is minimized (iconic)
	SizeMove  bool // user is dragging/resizing the window right now
	Streaming bool // an OBS stream is live on this machine
}

type gov struct {
	mu        sync.Mutex
	focused   bool
	minimized bool
	sizeMove  bool
	streaming bool

	deferred map[string]func() // background work parked while streaming, keyed for dedup
	log      *logbus.Bus
	lastPrio bool // last applied "below-normal?" decision (avoid redundant syscalls)
	prioSet  bool
	watchers []func(Signals) // signal-change listeners (see OnChange)
}

// g is the process-wide singleton. Window starts focused (the app opens in the foreground).
var g = &gov{focused: true, deferred: map[string]func(){}}

// SetLog wires a logbus for state-transition logging (optional; safe if never called).
func SetLog(l *logbus.Bus) {
	g.mu.Lock()
	g.log = l
	g.mu.Unlock()
}

func (s *gov) apply() {
	// Below-normal whenever a stream is live, the window isn't the user's focus (minimized/
	// backgrounded), OR the user is mid drag/resize - during a size-move WebView2 repaints on the
	// CPU and an in-proc worker at NORMAL starves the UI thread (window trails the cursor).
	// Normal only when the user is actively looking at an idle rave-mate and nothing is streaming.
	below := s.streaming || s.minimized || !s.focused || s.sizeMove
	if s.prioSet && below == s.lastPrio {
		return
	}
	s.lastPrio, s.prioSet = below, true
	sysexec.SetSelfBelowNormal(below)
}

func (s *gov) drainDeferred() []func() {
	if len(s.deferred) == 0 {
		return nil
	}
	out := make([]func(), 0, len(s.deferred))
	for _, fn := range s.deferred {
		out = append(out, fn)
	}
	s.deferred = map[string]func(){}
	return out
}

func set(field *bool, v bool, name string) {
	g.mu.Lock()
	if *field == v {
		g.mu.Unlock()
		return
	}
	wasStreaming := g.streaming // capture BEFORE mutating (field may alias &g.streaming)
	*field = v
	g.apply()
	var resume []func()
	if name == "streaming" && wasStreaming && !v {
		resume = g.drainDeferred() // stream ended - release parked background work
	}
	log := g.log
	sig := Signals{Focused: g.focused, Minimized: g.minimized, SizeMove: g.sizeMove, Streaming: g.streaming}
	watchers := make([]func(Signals), len(g.watchers))
	copy(watchers, g.watchers)
	g.mu.Unlock()
	for _, fn := range watchers { // fired OUTSIDE the lock: a watcher may read Snapshot()
		fn(sig)
	}
	if log != nil {
		// streaming transitions are rare + meaningful (Info); focus/minimize/drag flap
		// with normal window use - Debug keeps them out of the main view.
		if name == "streaming" {
			log.Info("governor", name+" changed", map[string]any{"value": v})
		} else {
			log.Debug("governor", name+" changed", map[string]any{"value": v})
		}
	}
	for _, fn := range resume {
		go fn()
	}
}

// SetFocused reports the rave-mate window gained/lost foreground focus.
func SetFocused(b bool) { set(&g.focused, b, "focused") }

// SetMinimized reports the window was minimized/restored.
func SetMinimized(b bool) { set(&g.minimized, b, "minimized") }

// SetSizeMove reports a window drag/resize started/ended.
func SetSizeMove(b bool) { set(&g.sizeMove, b, "sizemove") }

// SetStreaming reports an OBS stream on this machine went live/ended. On end, any background work
// parked via WhenBackgroundAllowed is released.
func SetStreaming(b bool) { set(&g.streaming, b, "streaming") }

// OnChange registers a listener for signal changes (called after the priority decision is applied,
// outside the lock). Used where a signal must reach ANOTHER process that cannot observe it - the
// webui procShell forwards Streaming to its window child so the child's own governor reaches the
// same below-normal verdict the single in-proc process used to make. Listeners must not block.
func OnChange(fn func(Signals)) {
	if fn == nil {
		return
	}
	g.mu.Lock()
	g.watchers = append(g.watchers, fn)
	g.mu.Unlock()
}

// Snapshot returns the current signals.
func Snapshot() Signals {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Signals{Focused: g.focused, Minimized: g.minimized, SizeMove: g.sizeMove, Streaming: g.streaming}
}

// UIAnimAllowed reports whether the ~1 Hz webui graph/tick refresh should run. Paused when the
// window is hidden (minimized), not focused, mid drag/resize, or a stream is live - none of those
// need rave-mate's own graphs repainting, and repainting competes with the encoder for GPU/CPU.
func UIAnimAllowed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.focused && !g.minimized && !g.sizeMove && !g.streaming
}

// BackgroundAllowed reports whether heavy, non-essential batch work (audio fingerprinting, library
// indexing/sync sweeps, catalog hydration) may run now. False while a stream is live (that work is
// the CPU offender that starves OBS's audio encoder) OR while the user is dragging/resizing the
// window (a heavy in-proc loop then starves the software WebView2 compositor -> visible drag lag).
// None of the gated work is stream- or UI-critical, so deferring it is always safe.
func BackgroundAllowed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.streaming && !g.sizeMove
}

// busyPollInterval is how often WaitWhileBusy re-checks BackgroundAllowed while parked.
const busyPollInterval = 150 * time.Millisecond

// WaitWhileBusy blocks in short slices while heavy background work is disallowed (a stream is live
// or the window is mid drag/resize), so a long in-proc sweep yields the CPU to the UI/encoder
// instead of starving them. Returns immediately once work is allowed again, or promptly when ctx is
// done (a nil ctx never cancels). Call at the head of each heavy sweep iteration - amortize (every N
// items) on very tight loops so the check itself is negligible.
func WaitWhileBusy(ctx context.Context) {
	for !BackgroundAllowed() {
		if ctx == nil {
			time.Sleep(busyPollInterval)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(busyPollInterval):
		}
	}
}

// WhenBackgroundAllowed runs fn now if background work is allowed; otherwise parks it (deduped by
// key - a repeated key overwrites the pending fn) and runs it when the stream ends. Use for
// deferrable heavy work (e.g. fingerprinting a just-finished capture while a stream is still live).
func WhenBackgroundAllowed(key string, fn func()) {
	g.mu.Lock()
	if !g.streaming {
		g.mu.Unlock()
		fn()
		return
	}
	g.deferred[key] = fn
	log := g.log
	g.mu.Unlock()
	if log != nil {
		log.Info("governor", "background work deferred (stream live)", map[string]any{"key": key})
	}
}
