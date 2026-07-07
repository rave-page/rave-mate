package aggregator

import (
	"context"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// fakeSource emits one observation when emitOnce is closed, then idles until ctx done.
type fakeSource struct {
	id       string
	emitOnce chan struct{}
}

func (f *fakeSource) ID() string                         { return f.id }
func (f *fakeSource) Capabilities() []session.Capability { return nil }
func (f *fakeSource) Start(ctx context.Context, emit func(session.Observation)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-f.emitOnce:
			emit(session.Observation{Source: f.id, Scope: session.Scope{Kind: session.ScopeDeck, ID: "A"},
				Fields: map[string]any{session.FieldTitle: "x"}})
			f.emitOnce = nil // don't re-select a nil-after-close repeatedly
		}
	}
}

// TestReceivingReflectsData: a started-but-silent source is Running but NOT Receiving; once
// it emits, Receiving flips true - the fix for the "live" badge that lied.
func TestReceivingReflectsData(t *testing.T) {
	merger := session.NewMerger()
	a := New(logbus.New(16), merger)
	src := &fakeSource{id: "fake", emitOnce: make(chan struct{})}
	a.AddSource(src, func() bool { return true })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Running but no data yet → not receiving.
	if got := find(a, "fake"); !got.Running || got.Receiving {
		t.Fatalf("before emit: running=%v receiving=%v, want running && !receiving", got.Running, got.Receiving)
	}

	close(src.emitOnce)
	waitFor(t, func() bool { return find(a, "fake").Receiving })

	// Stopping the source clears receiving.
	a.Stop()
	if got := find(a, "fake"); got.Receiving {
		t.Fatalf("after stop: receiving=true, want false")
	}
}

func find(a *Aggregator, id string) SourceInfo {
	for _, s := range a.Sources() {
		if s.ID == id {
			return s
		}
	}
	return SourceInfo{}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
