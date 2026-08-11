package reconcile

import (
	"testing"
	"time"

	"rave.page/mate/internal/musiclib"
)

func at(h, m int) time.Time { return time.Date(2026, 6, 3, h, m, 0, 0, time.Local) }

func sess(name string, plays ...musiclib.PlayedTrack) musiclib.Session {
	return musiclib.Session{Name: name, Played: plays}
}
func play(path string, deck int, t time.Time, dur float64) musiclib.PlayedTrack {
	return musiclib.PlayedTrack{Path: path, Deck: deck, StartedAt: t, DurationSec: dur}
}

func TestMatchSession_picksBestOverlapAndOffsets(t *testing.T) {
	// Recording 22:00–23:00. The matching set runs inside it; an unrelated set is far earlier.
	rec0, rec1 := at(22, 0), at(23, 0)
	theSet := sess("night",
		play("a.flac", 0, at(22, 1), 300),
		play("b.flac", 1, at(22, 6), 300),
		play("c.flac", 0, at(22, 12), 300),
	)
	other := sess("afternoon",
		play("x.flac", 0, at(15, 0), 300),
		play("y.flac", 1, at(15, 6), 300),
	)
	m, ok := MatchSession(rec0, rec1, []musiclib.Session{other, theSet})
	if !ok {
		t.Fatal("expected a match")
	}
	if m.SessionName != "night" {
		t.Fatalf("matched %q, want night", m.SessionName)
	}
	if len(m.Tracks) != 3 {
		t.Fatalf("got %d tracks, want 3", len(m.Tracks))
	}
	if m.Tracks[0].Offset != time.Minute || m.Tracks[0].Index != 1 || m.Tracks[0].Path != "a.flac" {
		t.Errorf("track0 = %+v, want offset 1m index 1 a.flac", m.Tracks[0])
	}
	if m.Tracks[2].Offset != 12*time.Minute {
		t.Errorf("track2 offset = %s, want 12m", m.Tracks[2].Offset)
	}
}

func TestMatchSession_noOverlapFails(t *testing.T) {
	rec0, rec1 := at(22, 0), at(23, 0)
	far := sess("yesterday", play("x.flac", 0, at(3, 0), 300))
	if _, ok := MatchSession(rec0, rec1, []musiclib.Session{far}); ok {
		t.Error("disjoint session must not match")
	}
}

func TestMatchSession_padIncludesEdgeTracks(t *testing.T) {
	// A track that started 80s before the recorder armed still belongs (within 90s pad).
	rec0, rec1 := at(22, 0), at(22, 30)
	s := sess("edge",
		play("pre.flac", 0, rec0.Add(-80*time.Second), 300), // before start, within pad
		play("mid.flac", 1, at(22, 10), 300),
	)
	m, ok := MatchSession(rec0, rec1, []musiclib.Session{s})
	if !ok || len(m.Tracks) != 2 {
		t.Fatalf("ok=%v tracks=%d, want 2", ok, len(m.Tracks))
	}
	if m.Tracks[0].Path != "pre.flac" || m.Tracks[0].Offset != 0 {
		t.Errorf("edge track offset should clamp to 0, got %+v", m.Tracks[0])
	}
}

func TestMatchSession_openEndedUsesSessionEnd(t *testing.T) {
	rec0 := at(22, 0)
	s := sess("live", play("a.flac", 0, at(22, 5), 300))
	m, ok := MatchSession(rec0, time.Time{}, []musiclib.Session{s})
	if !ok || len(m.Tracks) != 1 {
		t.Fatalf("open-ended match failed: ok=%v tracks=%d", ok, len(m.Tracks))
	}
}

// TestMatchSessionFull_extendsPastCrashEnd: the crash case - the recorder died
// at 22:20 but the DJ played on; full mode reconstructs the whole session.
func TestMatchSessionFull_extendsPastCrashEnd(t *testing.T) {
	rec0, rec1 := at(22, 0), at(22, 20) // EndedAt = crash point
	s := sess("night",
		play("a.flac", 0, at(22, 1), 300),
		play("b.flac", 1, at(22, 10), 300),
		play("c.flac", 0, at(22, 40), 300), // after the crash
		play("d.flac", 1, at(22, 50), 300),
	)
	if m, _ := MatchSession(rec0, rec1, []musiclib.Session{s}); len(m.Tracks) != 2 {
		t.Fatalf("window match got %d tracks, want 2", len(m.Tracks))
	}
	m, ok := MatchSessionFull(rec0, rec1, []musiclib.Session{s})
	if !ok {
		t.Fatal("expected a full match")
	}
	if len(m.Tracks) != 4 {
		t.Fatalf("full match got %d tracks, want 4", len(m.Tracks))
	}
	if m.Tracks[3].Path != "d.flac" || m.Tracks[3].Offset != 50*time.Minute {
		t.Errorf("track3 = %+v, want d.flac at 50m", m.Tracks[3])
	}
}

// TestMatchSessionFull_noOverlapStillFails: full mode never invents a match.
func TestMatchSessionFull_noOverlapStillFails(t *testing.T) {
	far := sess("yesterday", play("x.flac", 0, at(3, 0), 300))
	if _, ok := MatchSessionFull(at(22, 0), at(23, 0), []musiclib.Session{far}); ok {
		t.Error("disjoint session must not match in full mode either")
	}
}
