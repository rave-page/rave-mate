package libdb

import (
	"testing"

	"rave.page/mate/internal/musiclib"
)

// TestBeginTrackUpsertAdditive: the folder-playlist top-up path never deletes rows
// absent from the batch (unlike BeginTrackSync's refresh semantics).
func TestBeginTrackUpsertAdditive(t *testing.T) {
	d := openTmp(t)
	src, err := d.EnsureSource("folder", "/m/incoming")
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	syncAll(t, d, src, []musiclib.Track{
		{Path: "/m/incoming/a.mp3", Artist: "A", Title: "One"},
		{Path: "/m/incoming/b.mp3", Artist: "B", Title: "Two"},
	})

	sy, err := d.BeginTrackUpsert(src)
	if err != nil {
		t.Fatalf("begin upsert: %v", err)
	}
	if err := sy.Add(musiclib.Track{Path: "/m/incoming/c.mp3", Artist: "C", Title: "Three"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	res, err := sy.Commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.Added != 1 || res.Removed != 0 {
		t.Fatalf("upsert = %+v, want added=1 removed=0", res)
	}
	tracks, err := d.LoadTracks(src)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(tracks) != 3 { // a + b survived, c added
		t.Fatalf("got %d tracks, want 3", len(tracks))
	}
}

// TestEnsureSourceNoFirstSourcePromotion: folder sources (EnsureSource, no imported_at)
// must never displace a real import as FirstSource - remotectl/Fyne load from it.
func TestEnsureSourceNoFirstSourcePromotion(t *testing.T) {
	d := openTmp(t)
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "/c/collection.nml"}, 1)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	if _, err := d.EnsureSource("folder", "/m/incoming"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, ok, err := d.FirstSource()
	if err != nil || !ok {
		t.Fatalf("first: ok=%v err=%v", ok, err)
	}
	if first.ID != src.ID || first.App != "traktor" {
		t.Fatalf("FirstSource = %+v, want the traktor import", first)
	}
}

func TestHasTrackPath(t *testing.T) {
	d := openTmp(t)
	src, _ := d.EnsureSource("folder", "/m/incoming")
	syncAll(t, d, src, []musiclib.Track{{Path: "/m/incoming/a.mp3", Title: "One"}})
	if ok, err := d.HasTrackPath("/m/incoming/a.mp3"); err != nil || !ok {
		t.Fatalf("have = %v err=%v, want true", ok, err)
	}
	if ok, err := d.HasTrackPath("/m/incoming/missing.mp3"); err != nil || ok {
		t.Fatalf("missing = %v err=%v, want false", ok, err)
	}
	if ok, err := d.HasTrackPath(""); err != nil || ok {
		t.Fatalf("empty = %v err=%v, want false", ok, err)
	}
}

// TestUpdateTrackGrid: the folder-import beatgrid save writes BPM + marker onto the row,
// journals both fields, and bumps the tracks epoch (LoadAllTracks invalidation).
func TestUpdateTrackGrid(t *testing.T) {
	d := openTmp(t)
	src, _ := d.EnsureSource("folder", "/m/incoming")
	orig := musiclib.Track{Path: "/m/incoming/a.mp3", Artist: "A", Title: "One", DurationSec: 300}
	syncAll(t, d, src, []musiclib.Track{orig})

	ver := d.TracksVersion()
	grid := []musiclib.GridMarker{{PositionMs: 512.5, BPM: 174}}
	if err := d.UpdateTrackGrid(orig, 174, grid); err != nil {
		t.Fatalf("update: %v", err)
	}
	if d.TracksVersion() == ver {
		t.Fatal("tracksVer not bumped")
	}
	tracks, err := d.LoadTracks(src)
	if err != nil || len(tracks) != 1 {
		t.Fatalf("load: %v (%d)", err, len(tracks))
	}
	got := tracks[0]
	if got.BPM != 174 || len(got.Beatgrid) != 1 || got.Beatgrid[0].PositionMs != 512.5 || got.Beatgrid[0].BPM != 174 {
		t.Fatalf("row = bpm %v grid %+v, want 174 @ 512.5ms", got.BPM, got.Beatgrid)
	}
}
