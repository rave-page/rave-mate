package filesink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

func TestWriteNowPlayingFiles(t *testing.T) {
	dir := t.TempDir()
	s := New(logbus.New(16), func() string { return dir })

	st := session.UnifiedState{
		Decks: map[string]map[string]session.FieldValue{
			"A": {
				session.FieldIsPlaying: {Value: true},
				session.FieldTitle:     {Value: "Strobe"},
				session.FieldArtist:    {Value: "deadmau5"},
				session.FieldBPM:       {Value: 128.0},
			},
		},
	}
	s.write(st)

	txt, err := os.ReadFile(filepath.Join(dir, "now_playing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(txt) != "deadmau5 - Strobe" {
		t.Fatalf("txt = %q", txt)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "now_playing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got nowPlayingJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "Strobe" || got.Artist != "deadmau5" || got.BPM != 128.0 || got.Deck != "A" || !got.IsPlaying {
		t.Fatalf("json = %+v", got)
	}

	// Per-deck text: A has the track, B is empty.
	if a, _ := os.ReadFile(filepath.Join(dir, "now_playing_A.txt")); string(a) != "deadmau5 - Strobe" {
		t.Fatalf("now_playing_A.txt = %q", a)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "now_playing_B.txt")); string(b) != "" {
		t.Fatalf("now_playing_B.txt should be empty, got %q", b)
	}
}

func TestWriteDecksFileMultiDeck(t *testing.T) {
	dir := t.TempDir()
	s := New(logbus.New(16), func() string { return dir })

	st := session.UnifiedState{
		Decks: map[string]map[string]session.FieldValue{
			"A": {session.FieldIsPlaying: {Value: true}, session.FieldTitle: {Value: "Strobe"}, session.FieldArtist: {Value: "deadmau5"}},
			"C": {session.FieldIsPlaying: {Value: true}, session.FieldTitle: {Value: "Opus"}, session.FieldArtist: {Value: "Eric Prydz"}},
		},
		Channels: map[string]map[string]session.FieldValue{
			"1": {session.FieldFader: {Value: 0.8}},
			"3": {session.FieldFader: {Value: 0.4}},
		},
	}
	s.write(st)

	raw, err := os.ReadFile(filepath.Join(dir, "now_playing_decks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ov session.Overlay
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Decks) != 2 {
		t.Fatalf("want 2 decks, got %d", len(ov.Decks))
	}
	if ov.Decks[0].Deck != "A" || ov.Decks[0].Fader != 0.8 || !ov.Decks[0].OnAir {
		t.Errorf("deck A: %+v", ov.Decks[0])
	}
	if ov.Decks[1].Deck != "C" || ov.Decks[1].Fader != 0.4 {
		t.Errorf("deck C: %+v", ov.Decks[1])
	}
	// Both decks have their own text file.
	if c, _ := os.ReadFile(filepath.Join(dir, "now_playing_C.txt")); string(c) != "Eric Prydz - Opus" {
		t.Fatalf("now_playing_C.txt = %q", c)
	}
}

func TestWriteClearsWhenSilent(t *testing.T) {
	dir := t.TempDir()
	s := New(logbus.New(16), func() string { return dir })
	// Playing → file has content.
	s.write(session.UnifiedState{Decks: map[string]map[string]session.FieldValue{
		"A": {session.FieldIsPlaying: {Value: true}, session.FieldTitle: {Value: "x"}},
	}})
	// Silent → text cleared.
	s.write(session.UnifiedState{Decks: map[string]map[string]session.FieldValue{
		"A": {session.FieldIsPlaying: {Value: false}},
	}})
	txt, _ := os.ReadFile(filepath.Join(dir, "now_playing.txt"))
	if string(txt) != "" {
		t.Fatalf("expected empty now_playing.txt when silent, got %q", txt)
	}
	if a, _ := os.ReadFile(filepath.Join(dir, "now_playing_A.txt")); string(a) != "" {
		t.Fatalf("expected empty now_playing_A.txt when silent, got %q", a)
	}
}
