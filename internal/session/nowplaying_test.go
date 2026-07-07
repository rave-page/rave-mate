package session

import (
	"testing"
	"time"
)

func fv(v any) FieldValue { return FieldValue{Value: v} }

func TestDeriveNowPlayingPicksLoudestPlayingDeck(t *testing.T) {
	st := UnifiedState{
		Decks: map[string]map[string]FieldValue{
			"A": {FieldIsPlaying: fv(true), FieldTitle: fv("A-track"), FieldElapsedTime: fv(10.0)},
			"B": {FieldIsPlaying: fv(true), FieldTitle: fv("B-track"), FieldElapsedTime: fv(5.0)},
			"C": {FieldIsPlaying: fv(false), FieldTitle: fv("C-track")},
		},
		Channels: map[string]map[string]FieldValue{
			"1": {FieldFader: fv(0.2)}, // deck A quiet
			"2": {FieldFader: fv(0.9)}, // deck B loud
		},
	}
	np, ok := st.DeriveNowPlaying()
	if !ok || np.Deck != "B" {
		t.Fatalf("expected loudest playing deck B, got %q ok=%v", np.Deck, ok)
	}
}

func TestDeriveNowPlayingNoneWhenSilent(t *testing.T) {
	st := UnifiedState{Decks: map[string]map[string]FieldValue{
		"A": {FieldIsPlaying: fv(false)},
	}}
	if _, ok := st.DeriveNowPlaying(); ok {
		t.Fatal("expected no now-playing when nothing is playing")
	}
}

func TestDeriveNowPlayingFaderlessFallsBackToElapsed(t *testing.T) {
	// No channel data → both assumed audible (fader 1.0); elapsed breaks the tie.
	st := UnifiedState{Decks: map[string]map[string]FieldValue{
		"A": {FieldIsPlaying: fv(true), FieldElapsedTime: fv(3.0)},
		"D": {FieldIsPlaying: fv(true), FieldElapsedTime: fv(99.0)},
	}}
	np, ok := st.DeriveNowPlaying()
	if !ok || np.Deck != "D" {
		t.Fatalf("expected deck D (greater elapsed) without fader data, got %q", np.Deck)
	}
}

func TestDeriveNowPlayingSkipsStaleDeck(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	fvAt := func(v any, ts time.Time) FieldValue { return FieldValue{Value: v, TS: ts} }
	stale := now.Add(-3 * time.Minute)
	fresh := now.Add(-10 * time.Second)
	st := UnifiedState{Decks: map[string]map[string]FieldValue{
		// A died mid-play: isPlaying=true left behind, no updates in 3 min.
		"A": {FieldIsPlaying: fvAt(true, stale), FieldTitle: fvAt("ghost", stale)},
		"B": {FieldIsPlaying: fvAt(true, fresh), FieldTitle: fvAt("real", fresh)},
	}}
	np, ok := st.DeriveNowPlayingAt(now, NowPlayingStaleAfter)
	if !ok || np.Deck != "B" {
		t.Fatalf("stale deck must lose to fresh one: deck=%q ok=%v", np.Deck, ok)
	}
	// Only the ghost left → silence.
	st.Decks["B"][FieldIsPlaying] = fvAt(false, fresh)
	if _, ok := st.DeriveNowPlayingAt(now, NowPlayingStaleAfter); ok {
		t.Fatal("a lone stale deck must read as silence")
	}
	// Zero timestamps (synthetic states) are never stale.
	st.Decks["C"] = map[string]FieldValue{FieldIsPlaying: fv(true)}
	if np, ok := st.DeriveNowPlayingAt(now, NowPlayingStaleAfter); !ok || np.Deck != "C" {
		t.Fatalf("zero-TS deck must not be stale: deck=%q ok=%v", np.Deck, ok)
	}
	// maxAge<=0 disables the check.
	if np, ok := st.DeriveNowPlayingAt(now, 0); !ok || np.Deck == "" {
		_ = np
		t.Fatal("maxAge=0 must disable staleness")
	}
}
