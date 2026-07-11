package libdb

import (
	"fmt"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func openTestCompatDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestNormPair(t *testing.T) {
	a, b := NormPair("b.mp3", "a.mp3")
	if a != "a.mp3" || b != "b.mp3" {
		t.Fatalf("norm: %q %q", a, b)
	}
	a, b = NormPair("a.mp3", "b.mp3")
	if a != "a.mp3" || b != "b.mp3" {
		t.Fatalf("stable: %q %q", a, b)
	}
}

func TestAddCompatPairs(t *testing.T) {
	d := openTestCompatDB(t)
	// 3 tracks (with a dup + empty) = C(3,2) = 3 pairs
	n, err := d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3", "c.mp3", "b.mp3", ""})
	if err != nil || n != 3 {
		t.Fatalf("add: %d %v", n, err)
	}
	// idempotent
	n, err = d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3"})
	if err != nil || n != 0 {
		t.Fatalf("re-add: %d %v", n, err)
	}
	// second kind on the same pair = new row
	n, err = d.AddCompatPairs("energy", []string{"a.mp3", "b.mp3"})
	if err != nil || n != 1 {
		t.Fatalf("second kind: %d %v", n, err)
	}
	if _, err := d.AddCompatPairs("bogus", []string{"a.mp3", "b.mp3"}); err == nil {
		t.Fatal("invalid kind should fail")
	}
	if _, err := d.AddCompatPairs("blend", []string{"a.mp3"}); err == nil {
		t.Fatal("single track should fail")
	}
}

func TestCompatForSymmetric(t *testing.T) {
	d := openTestCompatDB(t)
	if _, err := d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3"}); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"a.mp3", "b.mp3"} {
		rows, err := d.CompatFor(from)
		if err != nil || len(rows) != 1 {
			t.Fatalf("for %s: %v %v", from, rows, err)
		}
		want := "b.mp3"
		if from == "b.mp3" {
			want = "a.mp3"
		}
		if rows[0].Path != want || rows[0].Kind != "blend" {
			t.Fatalf("row: %+v", rows[0])
		}
	}
}

func TestCompatForMany(t *testing.T) {
	d := openTestCompatDB(t)
	_, _ = d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3", "c.mp3"})
	_, _ = d.AddCompatPairs("energy", []string{"c.mp3", "d.mp3"})
	m, err := d.CompatForMany([]string{"a.mp3", "c.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m["a.mp3"]) != 2 {
		t.Fatalf("a: %+v", m["a.mp3"])
	}
	if len(m["c.mp3"]) != 3 { // a+b blend, d energy
		t.Fatalf("c: %+v", m["c.mp3"])
	}
	if len(m["b.mp3"]) != 0 { // not requested
		t.Fatalf("b leaked: %+v", m["b.mp3"])
	}
}

func TestRemoveCompat(t *testing.T) {
	d := openTestCompatDB(t)
	_, _ = d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3"})
	_, _ = d.AddCompatPairs("energy", []string{"a.mp3", "b.mp3"})
	if err := d.RemoveCompat("b.mp3", "a.mp3", "blend"); err != nil { // reversed order normalizes
		t.Fatal(err)
	}
	rows, _ := d.CompatFor("a.mp3")
	if len(rows) != 1 || rows[0].Kind != "energy" {
		t.Fatalf("after kind delete: %+v", rows)
	}
	if err := d.RemoveCompat("a.mp3", "b.mp3", ""); err != nil {
		t.Fatal(err)
	}
	if rows, _ := d.CompatFor("a.mp3"); len(rows) != 0 {
		t.Fatalf("after full delete: %+v", rows)
	}
}

func TestCompatForManyChunked(t *testing.T) {
	d := openTestCompatDB(t)
	_, _ = d.AddCompatPairs("blend", []string{"a.mp3", "z9999.mp3"}) // ends land in different chunks
	paths := make([]string, 0, 900)
	paths = append(paths, "a.mp3")
	for i := 0; i < 898; i++ { // unique fillers force multiple chunks
		paths = append(paths, fmt.Sprintf("x%04d.mp3", i))
	}
	paths = append(paths, "z9999.mp3", "a.mp3") // dup input must not double-record
	m, err := d.CompatForMany(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(m["a.mp3"]) != 1 || m["a.mp3"][0].Path != "z9999.mp3" {
		t.Fatalf("a: %+v", m["a.mp3"])
	}
	if len(m["z9999.mp3"]) != 1 || m["z9999.mp3"][0].Path != "a.mp3" {
		t.Fatalf("z: %+v", m["z9999.mp3"])
	}
}

func TestDividerExclusion(t *testing.T) {
	d := openTestCompatDB(t)
	src, err := d.EnsureSource("rave-mate", "C:/data/dividers")
	if err != nil {
		t.Fatal(err)
	}
	real, err := d.EnsureSource("traktor", "C:/traktor")
	if err != nil {
		t.Fatal(err)
	}
	sy, err := d.BeginTrackSync(real)
	if err != nil {
		t.Fatal(err)
	}
	if err := sy.Add(musiclib.Track{Path: "C:/music/a.mp3", Title: "Real Track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sy.Commit(); err != nil {
		t.Fatal(err)
	}
	div := musiclib.Track{Path: "C:/data/dividers/divider-dots-1.mp3", Title: "............", DurationSec: 2}
	if err := d.UpsertDividerTrack(src, div); err != nil {
		t.Fatal(err)
	}
	// collection working set excludes the divider (collection view, cloud/media sync, cleanup)
	all, err := d.LoadAllTracks()
	if err != nil || len(all) != 1 || all[0].Title != "Real Track" {
		t.Fatalf("LoadAllTracks: %+v %v", all, err)
	}
	// cross-software merge candidates exclude it too
	sourced, err := d.AllSourcedTracks()
	if err != nil || len(sourced) != 1 || sourced[0].Track.Title != "Real Track" {
		t.Fatalf("AllSourcedTracks: %+v %v", sourced, err)
	}
	// but it stays resolvable for playlist display + outbound filters
	divs, err := d.DividerTracks()
	if err != nil || len(divs) != 1 || divs[0].Title != "............" {
		t.Fatalf("DividerTracks: %+v %v", divs, err)
	}
	dp, _ := d.DividerPaths()
	if !dp[div.Path] {
		t.Fatalf("DividerPaths: %+v", dp)
	}
	// and lives inside a playlist like any entry
	pid, _ := d.CreatePlaylist("sorted", PlaylistManual, "")
	if _, err := d.AddToPlaylist(pid, "C:/music/a.mp3", div.Path); err != nil {
		t.Fatal(err)
	}
	paths, _ := d.PlaylistTracks(pid)
	if len(paths) != 2 || paths[1] != div.Path {
		t.Fatalf("playlist: %v", paths)
	}
}

func TestCompatSetAndSmartPrep(t *testing.T) {
	d := openTestCompatDB(t)
	// a—b, a—c direct; c—d second hop
	if _, err := d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3", "c.mp3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddCompatPairs("energy", []string{"c.mp3", "d.mp3"}); err != nil {
		t.Fatal(err)
	}
	set := d.CompatSet("a.mp3", 1)
	if !set["a.mp3"] || !set["b.mp3"] || !set["c.mp3"] || set["d.mp3"] || len(set) != 3 {
		t.Fatalf("depth1: %v", set)
	}
	set = d.CompatSet("a.mp3", 2)
	if !set["d.mp3"] || len(set) != 4 {
		t.Fatalf("depth2: %v", set)
	}
	if d.CompatSet("", 1) != nil {
		t.Fatal("no anchor → nil")
	}
	var nilDB *DB
	if nilDB.CompatSet("a.mp3", 1) != nil {
		t.Fatal("nil db → nil")
	}
	// SmartPrep bridges the rule to the set; zero prep without an anchor
	p := d.SmartPrep(musiclib.SmartRules{CompatWith: "a.mp3", CompatDepth: 2})
	if !p.Compat["d.mp3"] {
		t.Fatalf("prep: %v", p.Compat)
	}
	if p := d.SmartPrep(musiclib.SmartRules{}); p.Compat != nil {
		t.Fatalf("empty rule prep: %v", p.Compat)
	}
	if p := nilDB.SmartPrep(musiclib.SmartRules{CompatWith: "a.mp3"}); p.Compat != nil {
		t.Fatalf("nil db prep: %v", p.Compat)
	}
}
