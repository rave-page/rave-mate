package webui

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/libdb"
)

func plRowForTest(kind, folder string) libdb.PlaylistRow {
	return libdb.PlaylistRow{ID: 1, Name: "x", Kind: kind, Folder: folder}
}

func TestFiSplitName(t *testing.T) {
	cases := []struct {
		in, artist, title string
	}{
		{"Artist - Title.mp3", "Artist", "Title"},
		{"01 - Artist - Title.flac", "Artist", "Title"},
		{"03. Artist - Title.wav", "Artist", "Title"},
		{"12_Artist - Title.mp3", "Artist", "Title"},
		{"2 Unlimited - Get Ready.mp3", "2 Unlimited", "Get Ready"}, // bare space after digits ≠ track number
		{"NoSeparator.mp3", "", "NoSeparator"},
		{"01 Artist Title.mp3", "", "01 Artist Title"}, // bare-space numbering left alone (ambiguous)
		{"A - B - C.mp3", "A", "B - C"},                // first separator wins
		{" - Title.mp3", "", "- Title"},                // empty artist side → title only
	}
	for _, c := range cases {
		a, ti := fiSplitName(c.in)
		if a != c.artist || ti != c.title {
			t.Errorf("fiSplitName(%q) = (%q, %q), want (%q, %q)", c.in, a, ti, c.artist, c.title)
		}
	}
}

func TestFiScanDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	hidden := filepath.Join(dir, ".git")
	for _, d := range []string{sub, hidden} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		filepath.Join(dir, "b.mp3"),
		filepath.Join(dir, "a.flac"),
		filepath.Join(dir, "notes.txt"), // not audio
		filepath.Join(sub, "c.wav"),
		filepath.Join(hidden, "d.mp3"), // dot-dir: skipped when recursive
	} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	flat, capped := fiScanDir(dir, false)
	if capped || len(flat) != 2 || filepath.Base(flat[0]) != "a.flac" || filepath.Base(flat[1]) != "b.mp3" {
		t.Fatalf("non-recursive = %v (capped %v), want sorted [a.flac b.mp3]", flat, capped)
	}

	rec, capped := fiScanDir(dir, true)
	if capped || len(rec) != 3 {
		t.Fatalf("recursive = %v (capped %v), want 3 files (dot-dir skipped)", rec, capped)
	}
	for _, p := range rec {
		if filepath.Base(p) == "d.mp3" {
			t.Fatalf("dot-dir file leaked into %v", rec)
		}
	}
}

func TestLibPlCanSendTraktor(t *testing.T) {
	mk := func(kind, folder string) bool {
		return libPlCanSendTraktor(plRowForTest(kind, folder))
	}
	if !mk("manual", `C:\music\incoming`) {
		t.Error("manual folder-bound playlist must offer Send to Traktor")
	}
	if mk("manual", "") || mk("smart", `C:\x`) || mk("imported", `C:\x`) {
		t.Error("non-folder / smart / imported playlists must not offer it")
	}
}
