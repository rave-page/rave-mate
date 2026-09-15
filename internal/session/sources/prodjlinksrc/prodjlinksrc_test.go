package prodjlinksrc

import (
	"testing"

	"rave.page/mate/internal/prodjlink"
	"rave.page/mate/internal/session"
)

// capture collects emitted Observations.
func capture() (func(session.Observation), *[]session.Observation) {
	var got []session.Observation
	return func(o session.Observation) { got = append(got, o) }, &got
}

// TestHandleStatus_WithResolver: a fake resolver fills title/artist/key alongside bpm/isPlaying,
// on the correct deck scope.
func TestHandleStatus_WithResolver(t *testing.T) {
	emit, got := capture()
	resolve := func(id uint32) (string, string, string, bool) {
		if id != 555 {
			return "", "", "", false
		}
		return "Strobe", "deadmau5", "4A", true
	}
	st := prodjlink.Status{
		Player:       2, // deck B
		TrackID:      555,
		Type:         prodjlink.TrackRekordbox,
		Playing:      true,
		EffectiveBPM: 128.0,
	}
	handleStatus(st, resolve, map[int]uint32{}, emit)

	if len(*got) != 1 {
		t.Fatalf("emits=%d, want 1", len(*got))
	}
	o := (*got)[0]
	if o.Source != session.SourceProDJLink {
		t.Errorf("source=%q", o.Source)
	}
	if o.Scope.Kind != session.ScopeDeck || o.Scope.ID != "B" {
		t.Errorf("scope=%+v, want deck/B", o.Scope)
	}
	if o.Confidence != confidence {
		t.Errorf("confidence=%v, want %v", o.Confidence, confidence)
	}
	if o.Fields[session.FieldIsPlaying] != true {
		t.Errorf("isPlaying=%v", o.Fields[session.FieldIsPlaying])
	}
	if o.Fields[session.FieldBPM] != 128.0 {
		t.Errorf("bpm=%v", o.Fields[session.FieldBPM])
	}
	if o.Fields[session.FieldTitle] != "Strobe" {
		t.Errorf("title=%v", o.Fields[session.FieldTitle])
	}
	if o.Fields[session.FieldArtist] != "deadmau5" {
		t.Errorf("artist=%v", o.Fields[session.FieldArtist])
	}
	if o.Fields[session.FieldKey] != "4A" {
		t.Errorf("key=%v", o.Fields[session.FieldKey])
	}
	if !o.Loaded {
		t.Error("loaded=false on first sight of a player, want true")
	}
}

// TestHandleStatus_NilResolver: no resolver ⇒ only bpm/isPlaying, no track text.
func TestHandleStatus_NilResolver(t *testing.T) {
	emit, got := capture()
	st := prodjlink.Status{
		Player:       1, // deck A
		TrackID:      42,
		Type:         prodjlink.TrackRekordbox,
		Playing:      false,
		EffectiveBPM: 174.0,
	}
	handleStatus(st, nil, map[int]uint32{}, emit)

	if len(*got) != 1 {
		t.Fatalf("emits=%d, want 1", len(*got))
	}
	o := (*got)[0]
	if o.Scope.ID != "A" {
		t.Errorf("deck=%q, want A", o.Scope.ID)
	}
	if o.Fields[session.FieldIsPlaying] != false {
		t.Errorf("isPlaying=%v, want false", o.Fields[session.FieldIsPlaying])
	}
	if o.Fields[session.FieldBPM] != 174.0 {
		t.Errorf("bpm=%v, want 174", o.Fields[session.FieldBPM])
	}
	for _, f := range []string{session.FieldTitle, session.FieldArtist, session.FieldKey} {
		if _, ok := o.Fields[f]; ok {
			t.Errorf("field %q present with nil resolver", f)
		}
	}
}

// TestHandleStatus_ResolverSkippedForNonRekordbox: resolver isn't consulted when the loaded
// track isn't a rekordbox track (or trackID==0) - bpm/isPlaying still emit.
func TestHandleStatus_ResolverSkippedForNonRekordbox(t *testing.T) {
	calls := 0
	resolve := func(uint32) (string, string, string, bool) { calls++; return "x", "y", "z", true }

	emit, got := capture()
	handleStatus(prodjlink.Status{
		Player:       3,
		TrackID:      99,
		Type:         prodjlink.TrackCDAudio, // not rekordbox
		EffectiveBPM: 120,
	}, resolve, map[int]uint32{}, emit)

	handleStatus(prodjlink.Status{
		Player:       3,
		TrackID:      0, // no track loaded
		Type:         prodjlink.TrackRekordbox,
		EffectiveBPM: 120,
	}, resolve, map[int]uint32{}, emit)

	if calls != 0 {
		t.Errorf("resolver called %d times, want 0", calls)
	}
	for _, o := range *got {
		if _, ok := o.Fields[session.FieldTitle]; ok {
			t.Error("unexpected title without a resolvable rekordbox track")
		}
		if o.Fields[session.FieldBPM] != 120.0 {
			t.Errorf("bpm=%v, want 120", o.Fields[session.FieldBPM])
		}
	}
}

// TestHandleStatus_IgnoresPlayerZero: player < 1 emits nothing.
func TestHandleStatus_IgnoresPlayerZero(t *testing.T) {
	emit, got := capture()
	handleStatus(prodjlink.Status{Player: 0, EffectiveBPM: 128}, nil, map[int]uint32{}, emit)
	if len(*got) != 0 {
		t.Errorf("emits=%d, want 0 for player 0", len(*got))
	}
}

// TestHandleStatus_LoadedBoundary: Loaded is true on first sight and on a track change, false on
// an unchanged trackID (the deck.loaded boundary the merger clears winners on).
func TestHandleStatus_LoadedBoundary(t *testing.T) {
	emit, got := capture()
	last := map[int]uint32{}
	step := func(id uint32) {
		handleStatus(prodjlink.Status{Player: 1, TrackID: id, EffectiveBPM: 100}, nil, last, emit)
	}
	step(10) // first sight → loaded
	step(10) // same track → not loaded
	step(20) // changed track → loaded

	if len(*got) != 3 {
		t.Fatalf("emits=%d, want 3", len(*got))
	}
	want := []bool{true, false, true}
	for i, w := range want {
		if (*got)[i].Loaded != w {
			t.Errorf("emit[%d].Loaded=%v, want %v", i, (*got)[i].Loaded, w)
		}
	}
}

// TestHandleStatus_NoBPMWhenZero: EffectiveBPM==0 omits the bpm field (a stopped/unanalyzed deck).
func TestHandleStatus_NoBPMWhenZero(t *testing.T) {
	emit, got := capture()
	handleStatus(prodjlink.Status{Player: 1, EffectiveBPM: 0, Playing: true}, nil, map[int]uint32{}, emit)
	if _, ok := (*got)[0].Fields[session.FieldBPM]; ok {
		t.Error("bpm field present when EffectiveBPM==0")
	}
}
