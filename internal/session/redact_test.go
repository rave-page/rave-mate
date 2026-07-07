package session

import (
	"strings"
	"testing"
	"time"
)

// markDir marks everything under D:\Music\Promos (case-insensitive) with mark m.
func markDir(m Mark) RedactFunc {
	return func(path string) (Mark, bool) {
		p := strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
		if p == "d:/music/promos" || strings.HasPrefix(p, "d:/music/promos/") {
			return m, true
		}
		return Mark{}, false
	}
}

func applyDeck(t *testing.T, mr *Merger, fields map[string]any) {
	t.Helper()
	mr.Apply(Observation{
		Source: "traktor", Scope: Scope{Kind: ScopeDeck, ID: "A"},
		Fields: fields, Confidence: 1, TS: time.Now(),
	})
}

var promoFields = map[string]any{
	FieldTitle:  "Secret Anthem",
	FieldArtist: "DJ Test",
	FieldAlbum:  "Promo EP",
	FieldLabel:  "Secret Records",
	FieldBPM:    140.0,
	FieldPath:   `D:\Music\Promos\secret.mp3`,
}

func TestSnapshotRedactsMarkedDeck(t *testing.T) {
	mr := NewMerger()
	mr.SetRedactor(markDir(Mark{}))
	applyDeck(t, mr, promoFields)

	d := mr.Snapshot().Decks["A"]
	if got := StringField(d, FieldTitle); got != RedactedTitle {
		t.Fatalf("title: got %q", got)
	}
	if got := StringField(d, FieldArtist); got != "" {
		t.Fatalf("artist leaked: %q", got)
	}
	if got := StringField(d, FieldAlbum); got != "" {
		t.Fatalf("album leaked: %q", got)
	}
	if got := StringField(d, FieldLabel); got != "" {
		t.Fatalf("label leaked: %q", got)
	}
	if got := StringField(d, FieldPath); got != "" {
		t.Fatalf("path leaked: %q", got)
	}
	if bpm, _ := floatVal(d, FieldBPM); bpm != 140 {
		t.Fatalf("bpm must pass through, got %v", bpm)
	}
	// raw stays internal: unredact and re-check
	mr.SetRedactor(nil)
	if got := StringField(mr.Snapshot().Decks["A"], FieldTitle); got != "Secret Anthem" {
		t.Fatalf("raw lost: %q", got)
	}
}

func TestSnapshotMarkFlagCombinations(t *testing.T) {
	cases := []struct {
		mark                Mark
		wantArtist, wantLbl string
	}{
		{Mark{}, "", ""},
		{Mark{ShowArtist: true}, "DJ Test", ""},
		{Mark{ShowLabel: true}, "", "Secret Records"},
		{Mark{ShowArtist: true, ShowLabel: true}, "DJ Test", "Secret Records"},
	}
	for _, c := range cases {
		mr := NewMerger()
		mr.SetRedactor(markDir(c.mark))
		applyDeck(t, mr, promoFields)
		d := mr.Snapshot().Decks["A"]
		if got := StringField(d, FieldTitle); got != RedactedTitle {
			t.Fatalf("%+v: title %q", c.mark, got)
		}
		if got := StringField(d, FieldArtist); got != c.wantArtist {
			t.Fatalf("%+v: artist %q want %q", c.mark, got, c.wantArtist)
		}
		if got := StringField(d, FieldLabel); got != c.wantLbl {
			t.Fatalf("%+v: label %q want %q", c.mark, got, c.wantLbl)
		}
		if got := StringField(d, FieldAlbum); (c.mark.ShowLabel && got != "Promo EP") || (!c.mark.ShowLabel && got != "") {
			t.Fatalf("%+v: album %q", c.mark, got)
		}
	}
}

func TestSnapshotUnmarkedPassthrough(t *testing.T) {
	mr := NewMerger()
	mr.SetRedactor(markDir(Mark{}))
	f := map[string]any{FieldTitle: "Released", FieldArtist: "A", FieldPath: `D:\Music\Released\x.mp3`}
	applyDeck(t, mr, f)
	d := mr.Snapshot().Decks["A"]
	if StringField(d, FieldTitle) != "Released" || StringField(d, FieldArtist) != "A" {
		t.Fatal("unmarked track must pass through untouched")
	}
	if StringField(d, FieldPath) == "" {
		t.Fatal("unmarked path must survive (art lookups)")
	}
}

func TestEmittedUpdatesRedacted(t *testing.T) {
	mr := NewMerger()
	mr.SetRedactor(markDir(Mark{}))
	ch, unsub := mr.Subscribe()
	defer unsub()
	applyDeck(t, mr, promoFields)
	u := <-ch
	if got, _ := u.State[FieldTitle].(string); got != RedactedTitle {
		t.Fatalf("update title leaked: %q", got)
	}
	if got, _ := u.State[FieldArtist].(string); got != "" {
		t.Fatalf("update artist leaked: %q", got)
	}
	if got, _ := u.State[FieldPath].(string); got != "" {
		t.Fatalf("update path leaked: %q", got)
	}
}

// A later update WITHOUT a path field must still redact via the scope's held raw path
// (e.g. Traktor sends title-only refreshes after the load).
func TestUpdateWithoutPathUsesHeldPath(t *testing.T) {
	mr := NewMerger()
	mr.SetRedactor(markDir(Mark{}))
	applyDeck(t, mr, promoFields)
	ch, unsub := mr.Subscribe()
	defer unsub()
	applyDeck(t, mr, map[string]any{FieldTitle: "Secret Anthem (live edit)"})
	u := <-ch
	if got, _ := u.State[FieldTitle].(string); got != RedactedTitle {
		t.Fatalf("held-path redaction failed: %q", got)
	}
}

func TestMasterScopeRedacted(t *testing.T) {
	mr := NewMerger()
	mr.SetRedactor(markDir(Mark{}))
	mr.Apply(Observation{
		Source: "rekordbox-db", Scope: Scope{Kind: ScopeMaster},
		Fields:     map[string]any{FieldTitle: "Secret Anthem", FieldArtist: "DJ Test", FieldPath: `D:\Music\Promos\secret.mp3`},
		Confidence: 1, TS: time.Now(),
	})
	ms := mr.Snapshot().Master
	if StringField(ms, FieldTitle) != RedactedTitle || StringField(ms, FieldArtist) != "" {
		t.Fatalf("master scope leaked: %q / %q", StringField(ms, FieldTitle), StringField(ms, FieldArtist))
	}
}

// The overlay builder (feeds browser overlay, PNG cards → VR overlays, obs-ws, Spout)
// consumes Snapshot - verify the flattened DeckSnapshot inherits redaction.
func TestBuildOverlayInheritsRedaction(t *testing.T) {
	mr := NewMerger()
	mr.SetRedactor(markDir(Mark{ShowArtist: true}))
	f := map[string]any{}
	for k, v := range promoFields {
		f[k] = v
	}
	f[FieldIsPlaying] = true
	applyDeck(t, mr, f)
	ov := mr.Snapshot().BuildOverlay(time.Now(), 0)
	if len(ov.Decks) != 1 {
		t.Fatalf("want 1 deck, got %d", len(ov.Decks))
	}
	d := ov.Decks[0]
	if d.Title != RedactedTitle {
		t.Fatalf("overlay title leaked: %q", d.Title)
	}
	if d.Artist != "DJ Test" {
		t.Fatalf("ShowArtist lost in overlay: %q", d.Artist)
	}
	if d.Album != "" || d.Path != "" {
		t.Fatalf("overlay album/path leaked: %q / %q", d.Album, d.Path)
	}
}
