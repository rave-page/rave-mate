package libdb

import (
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func cacheDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedTracks imports trs as ONE refresh (a TrackSync deletes source rows not seen this pass,
// so all tracks for a source must be added together).
func seedTracks(t *testing.T, d *DB, srcID int64, trs ...musiclib.Track) {
	t.Helper()
	sy, err := d.BeginTrackSync(srcID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range trs {
		if err := sy.Add(tr); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sy.Commit(); err != nil {
		t.Fatal(err)
	}
}

// LoadAllTracks caches by tracksVer; a DELETE (which never journals to change_log) must still
// invalidate the snapshot - keying on LibraryVersion alone would leave the deleted track visible.
func TestLoadAllTracksCacheInvalidatesOnDelete(t *testing.T) {
	d := cacheDB(t)
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "c.nml"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	seedTracks(t, d, src.ID,
		musiclib.Track{Path: `C:\m\a.mp3`, Title: "A", Artist: "Z", DurationSec: 100},
		musiclib.Track{Path: `C:\m\b.mp3`, Title: "B", Artist: "Z", DurationSec: 100})

	before, err := d.LoadAllTracks() // populates the snapshot
	if err != nil || len(before) != 2 {
		t.Fatalf("initial load: %v %v", before, err)
	}
	lvBefore := d.LibraryVersion()

	res, err := d.DeleteTracksByPaths([]string{`C:\m\a.mp3`})
	if err != nil || res.TracksDeleted != 1 {
		t.Fatalf("delete: %+v %v", res, err)
	}
	// Guard the premise: a pure delete does NOT move LibraryVersion (change_log untouched)...
	if d.LibraryVersion() != lvBefore {
		t.Fatalf("delete unexpectedly moved LibraryVersion %d→%d", lvBefore, d.LibraryVersion())
	}
	// ...so only tracksVer keeps the snapshot honest.
	after, err := d.LoadAllTracks()
	if err != nil || len(after) != 1 || after[0].Path != `C:\m\b.mp3` {
		t.Fatalf("post-delete load stale: %+v %v", after, err)
	}
}

// A returned slice is a distinct backing array: callers may replace elements / their Cues field
// without corrupting the cache (webui does exactly this to s.tracks).
func TestLoadAllTracksReturnsIsolatedCopy(t *testing.T) {
	d := cacheDB(t)
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "c.nml"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	seedTracks(t, d, src.ID, musiclib.Track{Path: `C:\m\a.mp3`, Title: "A", Artist: "Z", DurationSec: 100,
		Cues: []musiclib.CuePoint{{Kind: musiclib.CueHot, StartMs: 1000}}})

	a, err := d.LoadAllTracks()
	if err != nil || len(a) != 1 {
		t.Fatalf("load a: %v %v", a, err)
	}
	a[0].Title = "MUT"                                          // whole-field replace on the returned element
	a[0].Cues = []musiclib.CuePoint{{StartMs: 9}, {StartMs: 8}} // replace .Cues slice (webui pattern)

	b, err := d.LoadAllTracks() // same tracksVer → served from cache
	if err != nil || len(b) != 1 {
		t.Fatalf("load b: %v %v", b, err)
	}
	if b[0].Title != "A" || len(b[0].Cues) != 1 || b[0].Cues[0].StartMs != 1000 {
		t.Fatalf("cache corrupted by caller mutation: %+v", b[0])
	}
	if &a[0] == &b[0] {
		t.Fatal("callers share a backing array")
	}
}
