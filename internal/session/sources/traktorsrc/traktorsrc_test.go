package traktorsrc

import (
	"testing"

	"rave.page/mate/internal/session"
	"rave.page/mate/internal/traktor"
)

// TestTranslateScopes asserts each Traktor event maps to the right scope + loaded flag.
func TestTranslateScopes(t *testing.T) {
	cases := []struct {
		ev       traktor.Event
		wantKind session.ScopeKind
		wantID   string
		loaded   bool
	}{
		{traktor.Event{Type: traktor.DeckUpdate, Deck: "A", State: map[string]any{"bpm": 128.0}}, session.ScopeDeck, "A", false},
		{traktor.Event{Type: traktor.DeckLoaded, Deck: "C", State: map[string]any{"title": "x"}}, session.ScopeDeck, "C", true},
		{traktor.Event{Type: traktor.ChannelUpdate, Channel: "2", State: map[string]any{"fader": 0.5}}, session.ScopeChannel, "2", false},
		{traktor.Event{Type: traktor.MasterClock, State: map[string]any{"bpm": 130.0}}, session.ScopeMaster, "", false},
	}
	for _, c := range cases {
		o := translate(c.ev)
		if o.Scope.Kind != c.wantKind || o.Scope.ID != c.wantID || o.Loaded != c.loaded || o.Source != session.SourceTraktor {
			t.Fatalf("%v → %+v", c.ev, o)
		}
	}
}

// TestNormalizeRenamesTraktorKeys confirms Traktor's filePath→path and onAirLevel→fader so
// cover art (needs the path) and the deck level meter (onAirLevel is Traktor's only level
// signal) populate the canonical fields. Other keys pass through untouched.
func TestNormalizeRenamesTraktorKeys(t *testing.T) {
	// deckLoaded with a file path.
	o := translate(traktor.Event{Type: traktor.DeckLoaded, Deck: "A", State: map[string]any{
		"title": "Control", "artist": "Sindicate", "filePath": `B:\Music\x.mp3`,
	}})
	if o.Fields[session.FieldPath] != `B:\Music\x.mp3` {
		t.Errorf("filePath should map to %q: %+v", session.FieldPath, o.Fields)
	}
	if _, ok := o.Fields["filePath"]; ok {
		t.Error("raw filePath key should be removed after rename")
	}
	if o.Fields[session.FieldTitle] != "Control" {
		t.Error("non-aliased keys must pass through")
	}

	// updateChannel with onAirLevel.
	c := translate(traktor.Event{Type: traktor.ChannelUpdate, Channel: "1", State: map[string]any{"onAirLevel": 0.8, "isOnAir": true}})
	if c.Fields[session.FieldFader] != 0.8 {
		t.Errorf("onAirLevel should map to fader: %+v", c.Fields)
	}
	if c.Fields["isOnAir"] != true {
		t.Error("isOnAir should pass through")
	}

	// No aliased keys → same map returned (no needless work).
	plain := map[string]any{"bpm": 128.0}
	if got := normalize(plain); got["bpm"] != 128.0 {
		t.Error("plain passthrough broken")
	}
}

// TestWireFidelity confirms a Traktor-only event stream produces merged Updates whose
// Type + scope + State match the legacy direct-from-Traktor ingest shape (deck.loaded =
// full replacement; deck.update carries the event's state verbatim).
func TestWireFidelity(t *testing.T) {
	m := session.NewMerger()
	ch, unsub := m.Subscribe()
	defer unsub()

	feed := []traktor.Event{
		{Type: traktor.DeckLoaded, Deck: "A", State: map[string]any{"title": "Song", "artist": "DJ", "bpm": 128.0}},
		{Type: traktor.DeckUpdate, Deck: "A", State: map[string]any{"title": "Song", "artist": "DJ", "bpm": 128.0, "isPlaying": true, "elapsedTime": 12.0}},
		{Type: traktor.ChannelUpdate, Channel: "1", State: map[string]any{"fader": 1.0}},
		{Type: traktor.MasterClock, State: map[string]any{"bpm": 128.0}},
	}
	wantType := []string{"deck.loaded", "deck.update", "channel.update", "master.clock"}
	wantDeck := []string{"A", "A", "", ""}
	wantChan := []string{"", "", "1", ""}

	for i, e := range feed {
		m.Apply(translate(e))
		u := <-ch
		if u.Type != wantType[i] {
			t.Fatalf("event %d type: got %q want %q", i, u.Type, wantType[i])
		}
		// scope id maps to the deck/channel the publisher will set.
		var deck, chn string
		switch u.Scope.Kind {
		case session.ScopeDeck:
			deck = u.Scope.ID
		case session.ScopeChannel:
			chn = u.Scope.ID
		}
		if deck != wantDeck[i] || chn != wantChan[i] {
			t.Fatalf("event %d scope: deck=%q chan=%q", i, deck, chn)
		}
		// State carries every non-nil field from the source event.
		for k, v := range e.State {
			if u.State[k] != v {
				t.Fatalf("event %d field %q: got %v want %v", i, k, u.State[k], v)
			}
		}
	}
}
