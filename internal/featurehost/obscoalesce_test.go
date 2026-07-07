package featurehost

import (
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/session"
)

type obsCollector struct {
	mu  sync.Mutex
	got []session.Observation
}

func (c *obsCollector) emit(o session.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, o)
}

func (c *obsCollector) snapshot() []session.Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]session.Observation(nil), c.got...)
}

func deckObs(fields map[string]any, loaded bool) session.Observation {
	return session.Observation{
		Source: session.SourceTraktor,
		Scope:  session.Scope{Kind: session.ScopeDeck, ID: "A"},
		Fields: fields, Confidence: 0.9, Loaded: loaded,
	}
}

// Burst of continuous ticks → leading edge + trailing flush only, final value wins.
func TestObsCoalescerBurstRateLimited(t *testing.T) {
	col := &obsCollector{}
	c := newObsCoalescer(50*time.Millisecond, col.emit)

	for i := 0; i < 100; i++ {
		c.Add(deckObs(map[string]any{
			session.FieldTitle:       "Same Track", // full-state frames: unchanged text every tick
			session.FieldElapsedTime: float64(i),
		}, false))
	}
	time.Sleep(200 * time.Millisecond) // let the trailing flush fire

	got := waitObs(t, col, 2) // ≥2: leading + trailing
	if len(got) > 5 {
		t.Fatalf("burst not coalesced: %d emissions for 100 ticks", len(got))
	}
	last := got[len(got)-1]
	if v, _ := last.Fields[session.FieldElapsedTime].(float64); v != 99 {
		t.Fatalf("latest-wins violated: final elapsed %v, want 99", last.Fields[session.FieldElapsedTime])
	}
}

// waitObs polls until the collector holds ≥n observations (avoids timing flakes).
func waitObs(t *testing.T, col *obsCollector, n int) []session.Observation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := col.snapshot()
		if len(got) >= n || time.Now().After(deadline) {
			if len(got) < n {
				t.Fatalf("want ≥%d emissions, got %d", n, len(got))
			}
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A discrete field changing value forwards immediately, merged with buffered state.
func TestObsCoalescerDiscreteChangeImmediate(t *testing.T) {
	col := &obsCollector{}
	c := newObsCoalescer(time.Hour, col.emit) // huge window: only discrete changes get out

	c.Add(deckObs(map[string]any{session.FieldTitle: "One", session.FieldElapsedTime: 1.0}, false))
	if got := col.snapshot(); len(got) != 1 { // first sight of a discrete value = change
		t.Fatalf("leading discrete not immediate: %d", len(got))
	}
	// Same title again → continuous-only in effect → buffered.
	c.Add(deckObs(map[string]any{session.FieldTitle: "One", session.FieldElapsedTime: 2.0}, false))
	if got := col.snapshot(); len(got) != 1 {
		t.Fatalf("unchanged discrete flushed: %d", len(got))
	}
	// isPlaying flip → immediate, carrying the buffered elapsed.
	c.Add(deckObs(map[string]any{session.FieldTitle: "One", session.FieldIsPlaying: true}, false))
	got := col.snapshot()
	if len(got) != 2 {
		t.Fatalf("discrete flip not immediate: %d", len(got))
	}
	if v, _ := got[1].Fields[session.FieldElapsedTime].(float64); v != 2.0 {
		t.Fatalf("buffered fields dropped on discrete flush: %+v", got[1].Fields)
	}
}

// Loaded boundary forwards immediately and discards pre-load buffered state.
func TestObsCoalescerLoadedDropsPending(t *testing.T) {
	col := &obsCollector{}
	c := newObsCoalescer(time.Hour, col.emit)

	c.Add(deckObs(map[string]any{session.FieldTitle: "Old", session.FieldElapsedTime: 100.0}, false))
	c.Add(deckObs(map[string]any{session.FieldElapsedTime: 101.0}, false)) // buffered
	c.Add(deckObs(map[string]any{session.FieldTitle: "New"}, true))        // boundary

	got := col.snapshot()
	if len(got) != 2 {
		t.Fatalf("want 2 emissions (leading + loaded), got %d", len(got))
	}
	loaded := got[1]
	if !loaded.Loaded {
		t.Fatal("boundary lost Loaded flag")
	}
	if _, ok := loaded.Fields[session.FieldElapsedTime]; ok {
		t.Fatalf("stale pre-load state merged into boundary: %+v", loaded.Fields)
	}
}

// Scopes rate-limit independently.
func TestObsCoalescerPerScope(t *testing.T) {
	col := &obsCollector{}
	c := newObsCoalescer(time.Hour, col.emit)

	c.Add(deckObs(map[string]any{session.FieldElapsedTime: 1.0}, false))
	b := session.Observation{
		Source: session.SourceTraktor,
		Scope:  session.Scope{Kind: session.ScopeDeck, ID: "B"},
		Fields: map[string]any{session.FieldElapsedTime: 5.0},
	}
	c.Add(b)
	if got := col.snapshot(); len(got) != 2 {
		t.Fatalf("scopes not independent: %d emissions", len(got))
	}
}
