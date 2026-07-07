package libdb

import (
	"testing"
	"time"
)

func TestPlayedTracksUpsertAndQuery(t *testing.T) {
	d := openTmp(t)
	start := time.Date(2026, 6, 5, 22, 0, 0, 0, time.UTC)

	// Confirm-time insert (no end yet).
	if err := d.SavePlayedTrack(PlayedTrack{
		ID: "rec_1#0", RecordingID: "rec_1", Artist: "Daft Punk", Title: "Aerodynamic",
		Deck: "A", BPM: 123.0, StartedAt: start,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Finalize update (same id → overwrites with end time + late metadata).
	end := start.Add(4 * time.Minute)
	if err := d.SavePlayedTrack(PlayedTrack{
		ID: "rec_1#0", RecordingID: "rec_1", Artist: "Daft Punk", Title: "Aerodynamic",
		Album: "Discovery", Key: "Am", Deck: "A", BPM: 123.0, StartedAt: start, EndedAt: end,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A second track in the same set.
	if err := d.SavePlayedTrack(PlayedTrack{
		ID: "rec_1#1", RecordingID: "rec_1", Artist: "Justice", Title: "Genesis",
		Deck: "B", StartedAt: end,
	}); err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	got, err := d.PlayedTracksFor("rec_1")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 played tracks, got %d: %+v", len(got), got)
	}
	// Ordered by started_at ASC → Aerodynamic first.
	if got[0].Title != "Aerodynamic" || got[0].Album != "Discovery" || got[0].EndedAt.IsZero() {
		t.Fatalf("first row wrong (upsert didn't take): %+v", got[0])
	}
	if got[0].EndedAt.Sub(got[0].StartedAt) != 4*time.Minute {
		t.Fatalf("window = %v want 4m", got[0].EndedAt.Sub(got[0].StartedAt))
	}
	if got[1].Title != "Genesis" || got[1].Deck != "B" {
		t.Fatalf("second row wrong: %+v", got[1])
	}

	all, err := d.ListPlayedTracks(10)
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %v len=%d", err, len(all))
	}
	// ListPlayedTracks is newest-first → Genesis leads.
	if all[0].Title != "Genesis" {
		t.Fatalf("list order wrong: %+v", all)
	}
}

func TestPlayedTracksNilSafe(t *testing.T) {
	var d *DB
	if err := d.SavePlayedTrack(PlayedTrack{ID: "x"}); err != nil {
		t.Fatalf("nil save: %v", err)
	}
	if got, err := d.ListPlayedTracks(5); err != nil || got != nil {
		t.Fatalf("nil list: %v %+v", err, got)
	}
	if got, err := d.PlayedTracksFor("y"); err != nil || got != nil {
		t.Fatalf("nil for: %v %+v", err, got)
	}
}
