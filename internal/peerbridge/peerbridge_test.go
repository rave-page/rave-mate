package peerbridge

import (
	"encoding/json"
	"testing"

	"rave.page/mate/internal/session"
)

func fv(v any) session.FieldValue { return session.FieldValue{Value: v} }

// TestNowPlayingFromCarriesAllDecks: every playing deck rides the payload; flat fields
// mirror the audible pick for older peers.
func TestNowPlayingFromCarriesAllDecks(t *testing.T) {
	st := session.UnifiedState{
		Decks: map[string]map[string]session.FieldValue{
			"A": {session.FieldIsPlaying: fv(true), session.FieldTitle: fv("Quiet"), session.FieldArtist: fv("X"), session.FieldBPM: fv(120.0)},
			"B": {session.FieldIsPlaying: fv(true), session.FieldTitle: fv("Loud"), session.FieldArtist: fv("Y"), session.FieldBPM: fv(128.0)},
			"C": {session.FieldIsPlaying: fv(false), session.FieldTitle: fv("Stopped")},
		},
		Channels: map[string]map[string]session.FieldValue{
			"1": {session.FieldFader: fv(0.1)},
			"2": {session.FieldFader: fv(1.0)},
		},
	}
	np := nowPlayingFrom(st)
	if len(np.Decks) != 2 {
		t.Fatalf("expected 2 playing decks on the wire, got %+v", np.Decks)
	}
	if !np.Playing || np.Deck != "B" || np.Title != "Loud" || np.Artist != "Y" {
		t.Fatalf("flat fields must mirror the audible deck: %+v", np)
	}
	for _, ds := range np.Decks {
		if ds.Fader == nil {
			t.Fatalf("fader known for deck %s but not sent: %+v", ds.Deck, ds)
		}
		if ds.Audible != (ds.Deck == "B") {
			t.Fatalf("audible mark wrong: %+v", ds)
		}
	}
}

// TestNowPlayingFromSilence: nothing playing → empty payload, Playing=false.
func TestNowPlayingFromSilence(t *testing.T) {
	np := nowPlayingFrom(session.UnifiedState{})
	if np.Playing || len(np.Decks) != 0 || np.Title != "" {
		t.Fatalf("silence must produce an empty payload: %+v", np)
	}
}

// TestAllDecksLegacySynthesis: a pre-decks payload (old sender) synthesizes one audible
// entry from the flat fields; new payloads pass Decks through.
func TestAllDecksLegacySynthesis(t *testing.T) {
	old := NowPlaying{Playing: true, Deck: "A", Title: "T", Artist: "R", BPM: 130}
	decks := old.AllDecks()
	if len(decks) != 1 || !decks[0].Audible || decks[0].Title != "T" || decks[0].Deck != "A" {
		t.Fatalf("legacy synthesis wrong: %+v", decks)
	}
	if (NowPlaying{}).AllDecks() != nil {
		t.Fatal("not-playing legacy payload must synthesize nothing")
	}
	multi := NowPlaying{Playing: true, Decks: []DeckState{{Deck: "A"}, {Deck: "B", Audible: true}}}
	if got := multi.AllDecks(); len(got) != 2 {
		t.Fatalf("decks list must pass through: %+v", got)
	}
}

// TestWireCompatOldPayload: frames from an old peer (no decks field) still decode, and
// new frames decode on both ends.
func TestWireCompatOldPayload(t *testing.T) {
	var np NowPlaying
	if err := json.Unmarshal([]byte(`{"playing":true,"deck":"A","title":"T","ts":1}`), &np); err != nil {
		t.Fatalf("old frame must decode: %v", err)
	}
	if np.Decks != nil {
		t.Fatalf("old frame must leave Decks nil: %+v", np)
	}
	f := 0.5
	raw, err := json.Marshal(NowPlaying{Playing: true, Decks: []DeckState{{Deck: "B", Fader: &f, Audible: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var back NowPlaying
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Decks) != 1 || *back.Decks[0].Fader != 0.5 {
		t.Fatalf("new frame round-trip failed: %v %+v", err, back)
	}
}
