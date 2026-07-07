package automation

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/store"
)

func mustStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// collectEvents subscribes and returns a thread-safe accessor + the unsub.
func collectEvents(m *Service) (func() []RunEvent, func()) {
	var mu sync.Mutex
	var evs []RunEvent
	unsub := m.OnEvent(func(e RunEvent) {
		mu.Lock()
		evs = append(evs, e)
		mu.Unlock()
	})
	return func() []RunEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]RunEvent(nil), evs...)
	}, unsub
}

func waitFor(t *testing.T, get func() []RunEvent, pred func([]RunEvent) bool) []RunEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if evs := get(); pred(evs) {
			return evs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out; events=%+v", get())
	return nil
}

func hasState(evs []RunEvent, s RunState) bool {
	for _, e := range evs {
		if e.State == s {
			return true
		}
	}
	return false
}

func newTestSvc(t *testing.T) *Service {
	t.Helper()
	st := mustStore(t)
	return NewManager(st, nil, noPreset, noopLog{})
}

// TestStartRunOnceEmitsEvents: a once-mode move-to run emits starting→running→completed
// (no awaiting), relocates the file, and records a run.
func TestStartRunOnceEmitsEvents(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := filepath.Join(dir, "set.wav")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "done")
	a, _ := m.Save(Automation{Label: "mv", WatchDir: dir, Actions: []Action{{Type: ActionMove, OutputDir: dest}}})

	get, unsub := collectEvents(m)
	defer unsub()
	runID, err := m.StartRun(ModeOnce, a.ID, src)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if runID == "" {
		t.Fatal("empty runId")
	}
	evs := waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateCompleted) })
	if hasState(evs, StateAwaiting) {
		t.Fatal("once mode must not await confirmation")
	}
	if !hasState(evs, StateStarting) || !hasState(evs, StateRunning) {
		t.Fatalf("missing lifecycle events: %+v", evs)
	}
	if _, err := os.Stat(filepath.Join(dest, "set.wav")); err != nil {
		t.Fatalf("file not moved: %v", err)
	}
	if runs := m.Runs(10); len(runs) != 1 || runs[0].Status != "success" {
		t.Fatalf("runs=%+v", runs)
	}
}

// TestStartRunManualGate: manual mode pauses with awaiting-confirmation; CommitStep proceeds.
func TestStartRunManualGate(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := filepath.Join(dir, "set.wav")
	_ = os.WriteFile(src, []byte("data"), 0o644)
	dest := filepath.Join(dir, "done")
	a, _ := m.Save(Automation{Label: "mv", WatchDir: dir, Actions: []Action{{Type: ActionMove, OutputDir: dest}}})

	get, unsub := collectEvents(m)
	defer unsub()
	runID, err := m.StartRun(ModeManual, a.ID, src)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// Must pause and NOT have moved the file yet.
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateAwaiting) })
	if _, err := os.Stat(filepath.Join(dest, "set.wav")); err == nil {
		t.Fatal("file moved before commit")
	}
	if err := m.CommitStep(runID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateCompleted) })
	if _, err := os.Stat(filepath.Join(dest, "set.wav")); err != nil {
		t.Fatalf("file not moved after commit: %v", err)
	}
}

// TestStartRunManualSkip: SkipStep leaves the file in place; run completes as partial.
func TestStartRunManualSkip(t *testing.T) {
	dir := t.TempDir()
	m := newTestSvc(t)
	src := filepath.Join(dir, "set.wav")
	_ = os.WriteFile(src, []byte("data"), 0o644)
	dest := filepath.Join(dir, "done")
	a, _ := m.Save(Automation{Label: "mv", WatchDir: dir, Actions: []Action{{Type: ActionMove, OutputDir: dest}}})

	get, unsub := collectEvents(m)
	defer unsub()
	runID, _ := m.StartRun(ModeManual, a.ID, src)
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateAwaiting) })
	if err := m.SkipStep(runID); err != nil {
		t.Fatalf("skip: %v", err)
	}
	waitFor(t, get, func(e []RunEvent) bool { return hasState(e, StateCompleted) })
	if _, err := os.Stat(filepath.Join(dest, "set.wav")); err == nil {
		t.Fatal("skipped step must not move the file")
	}
}

// TestListEventsMatching exercises pickMatchingEvent windowing without the network.
func TestEventMatching(t *testing.T) {
	fileMs := mustParse(t, "2026-06-05T22:00:00Z")
	events := []rawEvent{
		{ID: "a", Title: "Early", StartsAt: "2026-06-05T10:00:00Z", EndsAt: "2026-06-05T12:00:00Z"},
		{ID: "b", Title: "Tonight", StartsAt: "2026-06-05T21:00:00Z", EndsAt: "2026-06-06T02:00:00Z", Venue: "Club X", Slug: "tonight"},
	}
	got := pickMatchingEvent(events, fileMs, defaultBufferMinutes)
	if got == nil || got.ID != "b" {
		t.Fatalf("expected event b, got %+v", got)
	}
	// A file far from any event window matches nothing.
	if pickMatchingEvent(events, mustParse(t, "2020-01-01T00:00:00Z"), 0) != nil {
		t.Fatal("should not match an out-of-window file")
	}
}

func mustParse(t *testing.T, s string) int64 {
	t.Helper()
	ms, ok := parseMs(s)
	if !ok {
		t.Fatalf("parse %q", s)
	}
	return ms
}
