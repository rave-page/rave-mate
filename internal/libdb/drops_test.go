package libdb

import (
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func dropsDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestDropsCRUD(t *testing.T) {
	d := dropsDB(t)
	p := `C:\m\a.flac`
	if ds, err := d.Drops(p); err != nil || ds != nil {
		t.Fatalf("empty read: %v %v", ds, err)
	}
	if err := d.SetDrops(p, "art", "tit", 300, []float64{61000, 183000}); err != nil {
		t.Fatal(err)
	}
	ds, err := d.Drops(p)
	if err != nil || len(ds) != 2 || ds[0] != 61000 {
		t.Fatalf("read back: %v %v", ds, err)
	}
	all, err := d.AllDrops()
	if err != nil || len(all) != 1 || len(all[p]) != 2 {
		t.Fatalf("all: %v %v", all, err)
	}
	// journal recorded
	evs, err := d.ChangesForTrack(TrackHash("art", "tit", 300))
	if err != nil || len(evs) == 0 || evs[0].Field != "drops" {
		t.Fatalf("journal: %v %v", evs, err)
	}
	// clear → `[]` tombstone row stays (library sync propagates the clear)
	if err := d.SetDrops(p, "art", "tit", 300, nil); err != nil {
		t.Fatal(err)
	}
	if ds, _ := d.Drops(p); len(ds) != 0 {
		t.Fatalf("still present: %v", ds)
	}
	if all, _ := d.AllDrops(); len(all) != 0 {
		t.Fatalf("tombstone leaked into AllDrops: %v", all)
	}
	rows, err := d.DropRows()
	if err != nil {
		t.Fatal(err)
	}
	if ds, ok := rows[p]; !ok || len(ds) != 0 {
		t.Fatalf("tombstone missing from DropRows: %v", rows)
	}
	// clearing a never-marked track is a no-op: no tombstone
	if err := d.SetDrops(`C:\m\never.flac`, "a", "t", 100, nil); err != nil {
		t.Fatal(err)
	}
	rows, _ = d.DropRows()
	if _, ok := rows[`C:\m\never.flac`]; ok {
		t.Fatalf("no-op clear created a tombstone: %v", rows)
	}
}

func TestUpdateTrackCues(t *testing.T) {
	d := dropsDB(t)
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "c.nml"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tr := musiclib.Track{Path: `C:\m\b.mp3`, Title: "B", Artist: "A", DurationSec: 200,
		Cues: []musiclib.CuePoint{{Kind: musiclib.CueHot, Hotcue: 0, StartMs: 1000}}}
	ts, err := d.BeginTrackSync(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Add(tr); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Commit(); err != nil {
		t.Fatal(err)
	}
	newCues := append(tr.Cues, musiclib.CuePoint{Kind: musiclib.CuePlain, Hotcue: -1, StartMs: 5000, Name: "drop"})
	if err := d.UpdateTrackCues(tr, newCues); err != nil {
		t.Fatal(err)
	}
	got, err := d.LoadTracks(src.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("load: %v %v", got, err)
	}
	if len(got[0].Cues) != 2 || got[0].Cues[1].Name != "drop" {
		t.Fatalf("cues not updated: %+v", got[0].Cues)
	}
	evs, err := d.ChangesForTrack(TrackHash("A", "B", 200))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		if e.Field == "cues" && e.Origin == "manual" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cue change not journaled: %+v", evs)
	}
}
