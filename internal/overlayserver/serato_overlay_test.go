package overlayserver

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"rave.page/mate/internal/session"
)

// TestSeratoPerDeckOverlay drives the exact fields seratosrc now emits (two decks, isPlaying,
// no fader) through the merger + on-air gate and asserts both surface as on-air deck cards in
// /state - the end-to-end path the OBS overlay renders. Guards the two former bugs: no
// isPlaying (deck gated out) and single-deck collapse.
func TestSeratoPerDeckOverlay(t *testing.T) {
	m, base := startTestServer(t)
	now := time.Now()
	apply := func(id, title string) {
		m.Apply(session.Observation{
			Source: session.SourceSerato,
			TS:     now,
			Scope:  session.Scope{Kind: session.ScopeDeck, ID: id},
			Fields: map[string]any{
				session.FieldIsPlaying: true,
				session.FieldTitle:     title,
				session.FieldArtist:    title + " Artist",
			},
			Loaded: true,
		})
	}
	apply("A", "Track A")
	apply("B", "Track B")

	var ov session.Overlay
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/state")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = json.Unmarshal(raw, &ov)
		if len(ov.Decks) == 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if len(ov.Decks) != 2 {
		t.Fatalf("want 2 Serato deck cards in /state, got %d: %+v", len(ov.Decks), ov.Decks)
	}
	byDeck := map[string]session.DeckSnapshot{}
	for _, d := range ov.Decks {
		byDeck[d.Deck] = d
	}
	for _, id := range []string{"A", "B"} {
		d, ok := byDeck[id]
		if !ok {
			t.Fatalf("deck %s missing from overlay", id)
		}
		if !d.IsPlaying || !d.OnAir {
			t.Errorf("deck %s must be playing + on-air (unknown fader => on-air): %+v", id, d)
		}
	}
	if byDeck["A"].Title != "Track A" || byDeck["B"].Title != "Track B" {
		t.Errorf("deck titles wrong: A=%q B=%q", byDeck["A"].Title, byDeck["B"].Title)
	}
}
