package libdb

import (
	"testing"
	"time"
)

func TestSetRecordingsUpsertAndQuery(t *testing.T) {
	d := openTmp(t)
	start := time.Date(2026, 6, 5, 22, 0, 0, 0, time.UTC)

	// Insert (capture start: no end/bytes yet, unlinked).
	if err := d.SaveSetRecording(SetRecording{
		ID: "set_1", Path: "/sets/a.ogg", Format: "ogg", Mount: "/stream", StartedAt: start,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Upsert (capture end: link to a recording + final end/bytes).
	end := start.Add(90 * time.Minute)
	if err := d.SaveSetRecording(SetRecording{
		ID: "set_1", RecordingID: "rec_42", Path: "/sets/a.ogg", Format: "ogg", Mount: "/stream",
		StartedAt: start, EndedAt: end, Bytes: 12345,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := d.SetRecordingsFor("rec_42")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 linked, got %d", len(got))
	}
	s := got[0]
	if s.ID != "set_1" || s.Bytes != 12345 || s.EndedAt.IsZero() || !s.StartedAt.Equal(start) {
		t.Fatalf("upserted row wrong: %+v", s)
	}
	if s.EndedAt.Sub(s.StartedAt) != 90*time.Minute {
		t.Fatalf("window = %v want 90m", s.EndedAt.Sub(s.StartedAt))
	}

	all, err := d.ListSetRecordings(10)
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v len=%d", err, len(all))
	}
}

func TestSetRecordingsKindUnlinkedRelinkDelete(t *testing.T) {
	d := openTmp(t)
	start := time.Date(2026, 6, 6, 21, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	// Default kind = icecast; obs kind round-trips.
	if err := d.SaveSetRecording(SetRecording{ID: "ice_1", Path: "/sets/a.ogg", StartedAt: start, EndedAt: end}); err != nil {
		t.Fatalf("save ice: %v", err)
	}
	if err := d.SaveSetRecording(SetRecording{
		ID: "obs_1", Path: "/videos/a.mkv", Format: "mkv", Mount: "obs", Kind: SetKindOBS,
		StartedAt: start, EndedAt: end, Bytes: 99,
	}); err != nil {
		t.Fatalf("save obs: %v", err)
	}
	all, _ := d.ListSetRecordings(10)
	kinds := map[string]string{}
	for _, s := range all {
		kinds[s.ID] = s.Kind
	}
	if kinds["ice_1"] != SetKindIcecast || kinds["obs_1"] != SetKindOBS {
		t.Fatalf("kinds wrong: %+v", kinds)
	}

	// Both finished + unlinked → orphans; relink one → only the other remains.
	orphans, err := d.UnlinkedSetRecordings()
	if err != nil || len(orphans) != 2 {
		t.Fatalf("orphans: %v len=%d", err, len(orphans))
	}
	if err := d.RelinkSetRecording("obs_1", "rec_7"); err != nil {
		t.Fatalf("relink: %v", err)
	}
	orphans, _ = d.UnlinkedSetRecordings()
	if len(orphans) != 1 || orphans[0].ID != "ice_1" {
		t.Fatalf("after relink orphans=%+v", orphans)
	}
	linked, _ := d.SetRecordingsFor("rec_7")
	if len(linked) != 1 || linked[0].ID != "obs_1" || linked[0].Kind != SetKindOBS {
		t.Fatalf("linked=%+v", linked)
	}

	// Delete removes the row.
	if err := d.DeleteSetRecording("ice_1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, _ = d.ListSetRecordings(10)
	if len(all) != 1 || all[0].ID != "obs_1" {
		t.Fatalf("after delete all=%+v", all)
	}
}

func TestSetRecordingsNilSafe(t *testing.T) {
	var d *DB
	if err := d.SaveSetRecording(SetRecording{ID: "x"}); err != nil {
		t.Fatalf("nil save: %v", err)
	}
	if got, err := d.ListSetRecordings(5); err != nil || got != nil {
		t.Fatalf("nil list: %v %+v", err, got)
	}
	if got, err := d.SetRecordingsFor("y"); err != nil || got != nil {
		t.Fatalf("nil for: %v %+v", err, got)
	}
}
