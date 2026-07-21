package session

import (
	"testing"
	"time"
)

func fvt(v any, ts time.Time) FieldValue { return FieldValue{Value: v, TS: ts, Confidence: 1} }

func TestBuildOverlayMultiDeck(t *testing.T) {
	now := time.Now()
	st := UnifiedState{
		Decks: map[string]map[string]FieldValue{
			"A": {
				FieldTitle:     fvt("Strobe", now),
				FieldArtist:    fvt("deadmau5", now),
				FieldIsPlaying: fvt(true, now),
				FieldBPM:       fvt(128.0, now),
				FieldPath:      fvt("/music/strobe.mp3", now),
			},
			"C": {
				FieldTitle:     fvt("Opus", now),
				FieldArtist:    fvt("Eric Prydz", now),
				FieldIsPlaying: fvt(true, now),
			},
			"B": { // loaded but paused - still shown
				FieldTitle:     fvt("Ghosts", now),
				FieldIsPlaying: fvt(false, now),
			},
			"D": {}, // empty - skipped
		},
		Channels: map[string]map[string]FieldValue{
			"1": {FieldFader: fvt(0.9, now), FieldEQLow: fvt(0.5, now)},
			"3": {FieldFader: fvt(0.0, now)}, // playing but fader down → not on air
		},
		Master: map[string]FieldValue{FieldBPM: fvt(128.0, now)},
	}

	ov := st.BuildOverlay(now, NowPlayingStaleAfter)
	if len(ov.Decks) != 3 {
		t.Fatalf("want 3 decks (A,B,C), got %d: %+v", len(ov.Decks), ov.Decks)
	}
	if ov.Decks[0].Deck != "A" || ov.Decks[1].Deck != "B" || ov.Decks[2].Deck != "C" {
		t.Fatalf("decks not in A→D order: %v %v %v", ov.Decks[0].Deck, ov.Decks[1].Deck, ov.Decks[2].Deck)
	}

	a := ov.Decks[0]
	if !a.OnAir || !a.HasFader || a.Fader != 0.9 {
		t.Errorf("deck A should be on air with fader 0.9: %+v", a)
	}
	if a.ArtKey == "" {
		t.Error("deck A should have an art key from its path")
	}

	b := ov.Decks[1]
	if b.OnAir {
		t.Errorf("deck B is paused, must not be on air: %+v", b)
	}

	c := ov.Decks[2]
	if c.OnAir {
		t.Errorf("deck C fader is 0, must not be on air: %+v", c)
	}
	if !c.HasFader {
		t.Errorf("deck C has channel fader data: %+v", c)
	}

	if ov.Master.Deck != "A" {
		t.Errorf("master should point at audible deck A, got %q", ov.Master.Deck)
	}
	if ov.Master.BPM != 128.0 {
		t.Errorf("master BPM want 128, got %v", ov.Master.BPM)
	}
}

func TestBuildOverlaySkipsStale(t *testing.T) {
	now := time.Now()
	old := now.Add(-5 * time.Minute)
	st := UnifiedState{
		Decks: map[string]map[string]FieldValue{
			"A": {FieldTitle: fvt("Old", old), FieldIsPlaying: fvt(true, old)},
		},
	}
	ov := st.BuildOverlay(now, NowPlayingStaleAfter)
	if len(ov.Decks) != 0 {
		t.Fatalf("stale deck should be excluded, got %d", len(ov.Decks))
	}
}

// rekordbox via MIDI reports play-state but no title; the audible playing deck should inherit the
// master.db latest-play metadata so the overlay still shows it.
func TestBuildOverlayDBTitleFallback(t *testing.T) {
	now := time.Now()
	st := UnifiedState{
		Decks: map[string]map[string]FieldValue{
			"A": {FieldIsPlaying: fvt(true, now)},  // MIDI: playing, no title
			"B": {FieldIsPlaying: fvt(false, now)}, // not playing
		},
		Master: map[string]FieldValue{
			FieldTitle:  fvt("Scrap Metal", now),
			FieldArtist: fvt("BSYHO", now),
			FieldBPM:    fvt(175.0, now),
		},
	}
	ov := st.BuildOverlay(now, NowPlayingStaleAfter)
	if len(ov.Decks) != 1 {
		t.Fatalf("want 1 deck (A via db fallback), got %d: %+v", len(ov.Decks), ov.Decks)
	}
	a := ov.Decks[0]
	if a.Deck != "A" || a.Title != "Scrap Metal" || a.Artist != "BSYHO" || a.BPM != 175.0 {
		t.Errorf("deck A should inherit master.db metadata: %+v", a)
	}
	if !a.IsPlaying {
		t.Error("deck A should be playing")
	}
	// A deck with its own title must NOT be overwritten by the db fallback.
	st.Decks["A"][FieldTitle] = fvt("Own Title", now)
	if ov2 := st.BuildOverlay(now, NowPlayingStaleAfter); ov2.Decks[0].Title != "Own Title" {
		t.Errorf("own title must win over db fallback: %q", ov2.Decks[0].Title)
	}
}

func TestArtKeyMetaFirstAndStable(t *testing.T) {
	// Track identity must NOT change when the path arrives late (or path sources disagree on
	// the string) - a mid-track key flip resets overlay gates + blanks waveform/art caches.
	k1 := artKey("", "deadmau5", "Strobe")
	if artKey("/Music/x.mp3", "deadmau5", "Strobe") != k1 || artKey(`D:\Music\x.mp3`, "deadmau5", "Strobe") != k1 {
		t.Error("art key must stay stable across path arrival / path-string variants")
	}
	if artKey("", "deadmau5", "Strobe") == artKey("", "Eric Prydz", "Opus") {
		t.Error("art key should differ by artist/title")
	}
	if artKey("/music/x.mp3", "", "") == "" {
		t.Error("path must key a track with no text metadata")
	}
	if artKey("", "", "") != "" {
		t.Error("empty inputs should yield empty art key")
	}
}

func TestBuildOverlayEnded(t *testing.T) {
	now := time.Now()
	deck := func(playing bool, elapsed, length float64) UnifiedState {
		return UnifiedState{Decks: map[string]map[string]FieldValue{"A": {
			FieldTitle:       fvt("T", now),
			FieldIsPlaying:   fvt(playing, now),
			FieldElapsedTime: fvt(elapsed, now),
			FieldTrackLength: fvt(length, now),
		}}}
	}
	for _, tc := range []struct {
		name  string
		st    UnifiedState
		ended bool
	}{
		{"stopped at end", deck(false, 299.4, 300), true},
		{"still playing at end", deck(true, 299.4, 300), false},
		{"paused mid-track", deck(false, 120, 300), false},
		{"no length known", deck(false, 120, 0), false},
	} {
		ov := tc.st.BuildOverlay(now, NowPlayingStaleAfter)
		if len(ov.Decks) != 1 || ov.Decks[0].Ended != tc.ended {
			t.Errorf("%s: ended=%v want %v (%+v)", tc.name, ov.Decks[0].Ended, tc.ended, ov.Decks)
		}
	}
}
