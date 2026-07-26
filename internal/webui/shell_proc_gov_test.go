package webui

// The activity governor is tuned for an in-proc window (UIAnimAllowed, GPU off, BELOW_NORMAL). After
// the split the inputs live on BOTH sides, so neither side may silently change observed behaviour:
//   - the CHILD owns the window, so focus/minimize/size-move originate there and are forwarded up;
//     the daemon's governor (and the eval gate) then see exactly what they saw in-proc.
//   - the DAEMON knows about a live stream, so Streaming is forwarded down; the child's own governor
//     then reaches the same below-normal verdict the single process used to make.

import (
	"encoding/json"
	"testing"
	"time"

	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/logbus"
)

// The child's window signals land on the daemon's governor + the eval gate's size-move latch.
func TestProcShellForwardsWindowStateToGovernor(t *testing.T) {
	restoreGovernor(t)
	s := &procShell{done: make(chan struct{}), log: logbus.New(16)}
	feed := func(w procWin) {
		raw, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		s.evWin(raw)
	}

	feed(procWin{Focused: true})
	if !governor.UIAnimAllowed() {
		t.Error("a focused, non-minimized, idle window must allow the UI tick")
	}
	if s.inSizeMove() {
		t.Error("size-move latch set without a drag")
	}

	feed(procWin{Focused: true, SizeMove: true})
	if !s.inSizeMove() {
		t.Fatal("the eval gate did not latch the child's drag - the daemon would keep pushing patches")
	}
	if governor.UIAnimAllowed() || governor.BackgroundAllowed() {
		t.Error("a drag must pause the UI tick AND heavy background work, as in-proc")
	}

	feed(procWin{Focused: true})
	if s.inSizeMove() || !governor.BackgroundAllowed() {
		t.Error("the drag never ended")
	}

	feed(procWin{Focused: true, Minimized: true})
	if governor.UIAnimAllowed() {
		t.Error("a minimized window must pause the UI tick")
	}

	feed(procWin{}) // lost focus
	if governor.UIAnimAllowed() {
		t.Error("an unfocused window must pause the UI tick")
	}
	if governor.Snapshot().Focused {
		t.Error("focus signal not applied")
	}
}

// A close-to-tray from the child is reported (log parity with the in-proc onWindowHidden) and must
// NOT be mistaken for a governor signal.
func TestProcShellHiddenEventIsNotAGovernorSignal(t *testing.T) {
	restoreGovernor(t)
	s := &procShell{done: make(chan struct{}), log: logbus.New(16)}
	seen := 0
	saved := onWindowHidden
	onWindowHidden = func() { seen++ }
	t.Cleanup(func() { onWindowHidden = saved })

	raw, _ := json.Marshal(procWin{Focused: true})
	s.evWin(raw)
	raw, _ = json.Marshal(procWin{Hidden: true})
	s.evWin(raw)
	if seen != 1 {
		t.Fatalf("hidden reports = %d, want 1", seen)
	}
	if !governor.Snapshot().Focused {
		t.Error("a hide report must not clobber the focus signal")
	}
}

// Streaming is forwarded DOWN, deduped, and re-sent on every (re)spawn so a restarted child is never
// left at NORMAL priority while a stream is live.
func TestProcShellForwardsStreamingToChild(t *testing.T) {
	restoreGovernor(t)
	s := &procShell{dir: make(chan procFrame, procDirQueueCap), done: make(chan struct{}), log: logbus.New(16)}

	s.onGovernor(governor.Signals{Streaming: true})
	s.onGovernor(governor.Signals{Streaming: true}) // deduped
	if got := procDirEvents(s); len(got) != 1 || got[0] != procEvStream {
		t.Fatalf("streaming forwarding = %v, want one %s frame", got, procEvStream)
	}
	s.onGovernor(governor.Signals{Streaming: false})
	if got := procDirEvents(s); len(got) != 1 {
		t.Fatalf("the stream ending was not forwarded (%v)", got)
	}

	// Ready (re)spawn: forced, so the child's fresh governor is seeded even if nothing changed.
	governor.SetStreaming(true)
	s.gens.Store(1) // pretend a first generation already happened → this is a restart
	s.onChildReady()
	if got := procDirEvents(s); len(got) != 1 || got[0] != procEvStream {
		t.Fatalf("a restarted child was not re-seeded with the streaming state (%v)", got)
	}

	// A retired shell never touches its lanes again.
	close(s.done)
	s.onGovernor(governor.Signals{Streaming: false})
	if got := procDirEvents(s); len(got) != 0 {
		t.Errorf("a retired shell still pushed %v", got)
	}
}

// The eval flusher's hold predicate reads the CHILD's latch under a procShell (and stays false for a
// virtual shell, which has no window of its own to be dragged).
func TestHoldEvalsSourcesTheShell(t *testing.T) {
	restoreGovernor(t)
	ps := &procShell{done: make(chan struct{}), log: logbus.New(16)}
	u := &UI{shell: ps, stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	if u.holdEvals() {
		t.Error("no drag, no hold")
	}
	ps.sizeMv.Store(true)
	if !u.holdEvals() {
		t.Error("the child's drag must hold the daemon's eval flusher")
	}

	vs := newVirtualShell(nil, func(string) {}, func(string) {})
	t.Cleanup(vs.terminate)
	u.shell = vs
	if u.holdEvals() {
		t.Error("a headless mirror must keep streaming while the local window is dragged")
	}
}

// ── helpers ──

func procDirEvents(s *procShell) []string {
	var out []string
	for {
		select {
		case f := <-s.dir:
			out = append(out, f.ev)
		default:
			return out
		}
	}
}

// restoreGovernor puts the process-wide governor back to its documented start state (window focused,
// nothing else) so these tests do not leak signals into the rest of the package.
func restoreGovernor(t *testing.T) {
	t.Helper()
	reset := func() {
		governor.SetStreaming(false)
		governor.SetSizeMove(false)
		governor.SetMinimized(false)
		governor.SetFocused(true)
	}
	reset()
	t.Cleanup(func() {
		reset()
		time.Sleep(time.Millisecond) // let any watcher goroutine settle before the next test reads
	})
}
