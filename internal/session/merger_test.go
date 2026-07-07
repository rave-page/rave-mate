package session

import (
	"strings"
	"testing"
	"time"
)

// fakeClock lets tests advance time deterministically.
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time      { return f.t }
func (f *fakeClock) add(d time.Duration) { f.t = f.t.Add(d) }

func newTestMerger() (*Merger, *fakeClock) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	m := NewMerger()
	m.clock = c.now
	return m, c
}

func obs(src string, conf float64, ts time.Time, deck string, fields map[string]any) Observation {
	return Observation{Source: src, Confidence: conf, TS: ts, Scope: Scope{Kind: ScopeDeck, ID: deck}, Fields: fields}
}

func deckVal(t *testing.T, m *Merger, deck, field string) FieldValue {
	t.Helper()
	v, ok := m.Snapshot().DeckField(deck, field)
	if !ok {
		t.Fatalf("deck %s field %s missing", deck, field)
	}
	return v
}

func TestPriorityHigherWins(t *testing.T) {
	m, c := newTestMerger()
	// NML sets title first, then Traktor (higher priority for title) overrides it.
	m.Apply(obs(SourceNML, 0.9, c.now(), "A", map[string]any{FieldTitle: "from-nml"}))
	m.Apply(obs(SourceTraktor, 0.5, c.now(), "A", map[string]any{FieldTitle: "from-traktor"}))
	if got := deckVal(t, m, "A", FieldTitle); got.Value != "from-traktor" || got.Source != SourceTraktor {
		t.Fatalf("want traktor title, got %+v", got)
	}
}

func TestPriorityLowerDoesNotOverrideFresh(t *testing.T) {
	m, c := newTestMerger()
	m.Apply(obs(SourceTraktor, 0.5, c.now(), "A", map[string]any{FieldTitle: "traktor"}))
	// NML (lower priority for title) must not override a fresh higher-priority winner.
	m.Apply(obs(SourceNML, 0.99, c.now(), "A", map[string]any{FieldTitle: "nml"}))
	if got := deckVal(t, m, "A", FieldTitle); got.Value != "traktor" {
		t.Fatalf("lower-priority overrode fresh winner: %+v", got)
	}
}

func TestStaleHolderAgesOut(t *testing.T) {
	m, c := newTestMerger()
	m.Apply(obs(SourceTraktor, 0.5, c.now(), "A", map[string]any{FieldTitle: "traktor"}))
	c.add(ttl(FieldTitle) + time.Second) // traktor winner goes stale
	m.Apply(obs(SourceNML, 0.9, c.now(), "A", map[string]any{FieldTitle: "nml"}))
	if got := deckVal(t, m, "A", FieldTitle); got.Value != "nml" || got.Source != SourceNML {
		t.Fatalf("stale holder did not age out to lower-priority source: %+v", got)
	}
}

func TestSameSourceRefreshes(t *testing.T) {
	m, c := newTestMerger()
	m.Apply(obs(SourceTraktor, 0.5, c.now(), "A", map[string]any{FieldBPM: 128.0}))
	c.add(time.Second)
	m.Apply(obs(SourceTraktor, 0.5, c.now(), "A", map[string]any{FieldBPM: 130.0}))
	if got := deckVal(t, m, "A", FieldBPM); got.Value != 130.0 {
		t.Fatalf("same source did not refresh: %+v", got)
	}
}

func TestPriorityRespectsDefaultOrder(t *testing.T) {
	m, c := newTestMerger()
	// FieldEQHigh has no explicit priority entry → defaultPriority, where traktor outranks
	// midi.custom. Higher confidence must not flip it.
	m.Apply(obs(SourceMIDICustom, 0.9, c.now(), "A", map[string]any{FieldEQHigh: 0.8}))
	m.Apply(obs(SourceTraktor, 0.1, c.now(), "A", map[string]any{FieldEQHigh: 0.5}))
	if got := deckVal(t, m, "A", FieldEQHigh); got.Value != 0.5 || got.Source != SourceTraktor {
		t.Fatalf("expected higher-priority traktor to win: %+v", got)
	}
}

func TestFaderPrefersMidiCustom(t *testing.T) {
	m, c := newTestMerger()
	// Fader is special-cased: the MIDI-custom map reports the true channel-fader position,
	// so it outranks traktor's onAirLevel→fader even at lower confidence.
	m.Apply(obs(SourceTraktor, 0.9, c.now(), "A", map[string]any{FieldFader: 0.85}))
	m.Apply(obs(SourceMIDICustom, 0.1, c.now(), "A", map[string]any{FieldFader: 1.0}))
	if got := deckVal(t, m, "A", FieldFader); got.Value != 1.0 || got.Source != SourceMIDICustom {
		t.Fatalf("expected midi.custom to win fader (true position): %+v", got)
	}
}

func TestConfidenceTiebreakAtEqualRank(t *testing.T) {
	m, c := newTestMerger()
	// Two unmapped sources share the lowest rank → confidence breaks the tie.
	m.Apply(obs("plugin.x", 0.3, c.now(), "A", map[string]any{FieldTitle: "low"}))
	m.Apply(obs("plugin.y", 0.7, c.now(), "A", map[string]any{FieldTitle: "high"}))
	if got := deckVal(t, m, "A", FieldTitle); got.Value != "high" || got.Source != "plugin.y" {
		t.Fatalf("higher confidence did not win at equal rank: %+v", got)
	}
	// A lower-confidence equal-rank source must not override.
	m.Apply(obs("plugin.z", 0.5, c.now(), "A", map[string]any{FieldTitle: "mid"}))
	if got := deckVal(t, m, "A", FieldTitle); got.Value != "high" {
		t.Fatalf("lower confidence overrode at equal rank: %+v", got)
	}
}

func TestDeckLoadedResets(t *testing.T) {
	m, c := newTestMerger()
	m.Apply(obs(SourceTraktor, 0.9, c.now(), "A", map[string]any{FieldTitle: "old", FieldBPM: 120.0}))
	// A new load clears the deck, then sets only the new fields.
	loaded := Observation{Source: SourceTraktor, Confidence: 0.9, TS: c.now(),
		Scope: Scope{Kind: ScopeDeck, ID: "A"}, Loaded: true,
		Fields: map[string]any{FieldTitle: "new"}}
	m.Apply(loaded)
	snap := m.Snapshot()
	if got, _ := snap.DeckField("A", FieldTitle); got.Value != "new" {
		t.Fatalf("loaded title wrong: %+v", got)
	}
	if _, ok := snap.DeckField("A", FieldBPM); ok {
		t.Fatalf("deck.loaded did not clear stale bpm")
	}
}

func TestSubscribeReceivesUpdates(t *testing.T) {
	m, c := newTestMerger()
	ch, unsub := m.Subscribe()
	defer unsub()
	m.Apply(obs(SourceTraktor, 0.9, c.now(), "B", map[string]any{FieldTitle: "x"}))
	select {
	case u := <-ch:
		if u.Type != "deck.update" || u.Scope.ID != "B" || u.State[FieldTitle] != "x" {
			t.Fatalf("unexpected update: %+v", u)
		}
	case <-time.After(time.Second):
		t.Fatal("no update received")
	}
}

func TestNoUpdateWhenNothingWins(t *testing.T) {
	m, c := newTestMerger()
	m.Apply(obs(SourceTraktor, 0.9, c.now(), "A", map[string]any{FieldTitle: "keep"}))
	ch, unsub := m.Subscribe()
	defer unsub()
	// Lower-priority, fresh holder present → rejected → no emit.
	m.Apply(obs(SourceNML, 0.9, c.now(), "A", map[string]any{FieldTitle: "drop"}))
	select {
	case u := <-ch:
		t.Fatalf("got update for rejected observation: %+v", u)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMergerStats(t *testing.T) {
	m, c := newTestMerger()
	m.Apply(obs(SourceTraktor, 0.5, c.now(), "A", map[string]any{FieldTitle: "x"}))
	m.Apply(obs(SourceTraktor, 0.5, c.now(), "A", map[string]any{FieldTitle: "x"})) // same source refreshes
	got := m.Stats()
	for _, want := range []string{"applies=2", "fieldsIn=2", "fieldsWon=2", "updatesEmitted=2", "scopes=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stats missing %q: %s", want, got)
		}
	}
}
