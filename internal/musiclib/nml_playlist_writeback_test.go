package musiclib

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const playlistFixture = `<?xml version="1.0" encoding="UTF-8"?>
<NML VERSION="20">
  <HEAD COMPANY="native" PROGRAM="Traktor"></HEAD>
  <COLLECTION ENTRIES="3">
    <ENTRY TITLE="A"><LOCATION DIR="/:Music/:" FILE="a.mp3" VOLUME="C:"></LOCATION><INFO GENRE="Techno"></INFO></ENTRY>
    <ENTRY TITLE="B"><LOCATION DIR="/:Music/:" FILE="b.mp3" VOLUME="C:"></LOCATION><INFO GENRE="DnB"></INFO></ENTRY>
    <ENTRY TITLE="C"><LOCATION DIR="/:Music/:" FILE="c.mp3" VOLUME="C:"></LOCATION><INFO GENRE="House"></INFO></ENTRY>
  </COLLECTION>
  <PLAYLISTS>
    <NODE TYPE="FOLDER" NAME="$ROOT">
      <SUBNODES COUNT="2">
        <NODE TYPE="FOLDER" NAME="Crates">
          <SUBNODES COUNT="1">
            <NODE TYPE="PLAYLIST" NAME="Other">
              <PLAYLIST ENTRIES="1" TYPE="LIST" UUID="0123456789abcdef0123456789abcdef">
                <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="C:/:Music/:c.mp3"></PRIMARYKEY></ENTRY>
              </PLAYLIST>
            </NODE>
          </SUBNODES>
        </NODE>
        <NODE TYPE="PLAYLIST" NAME="Existing">
          <PLAYLIST ENTRIES="1" TYPE="LIST" UUID="fedcba9876543210fedcba9876543210">
            <ENTRY><PRIMARYKEY TYPE="TRACK" KEY="C:/:Music/:a.mp3"></PRIMARYKEY></ENTRY>
          </PLAYLIST>
        </NODE>
      </SUBNODES>
    </NODE>
  </PLAYLISTS>
</NML>`

func findPlaylist(t *testing.T, path, name string) *Playlist {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	pls, err := ParseNMLPlaylists(f)
	if err != nil {
		t.Fatalf("parse playlists: %v", err)
	}
	for i := range pls {
		if pls[i].Name == name {
			return &pls[i]
		}
	}
	return nil
}

func TestUpsertNMLPlaylistCreate(t *testing.T) {
	path := writeFixture(t, playlistFixture)
	pa := resolveLocation("C:", "/:Music/:", "a.mp3")
	pb := resolveLocation("C:", "/:Music/:", "b.mp3")
	missing := resolveLocation("C:", "/:Music/:", "missing.mp3") // not in COLLECTION → skipped

	added, err := UpsertNMLPlaylist(path, "GRID_PREP", []string{pa, pb, missing})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if added != 2 {
		t.Fatalf("added=%d; want 2", added)
	}
	pl := findPlaylist(t, path, "GRID_PREP")
	if pl == nil {
		t.Fatal("GRID_PREP not round-trippable via ParseNMLPlaylists")
	}
	if pl.Folder != "" {
		t.Errorf("folder=%q; want root", pl.Folder)
	}
	if len(pl.Paths) != 2 || pl.Paths[0] != pa || pl.Paths[1] != pb {
		t.Errorf("paths=%v; want [%s %s]", pl.Paths, pa, pb)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `COUNT="3"`) {
		t.Error("$ROOT SUBNODES COUNT not bumped to 3")
	}
	if !strings.Contains(s, `ENTRIES="2"`) {
		t.Error("new playlist ENTRIES != 2")
	}
	if got := len(regexp.MustCompile(`UUID="[0-9a-f]{32}"`).FindAllString(s, -1)); got != 3 {
		t.Errorf("uuid count=%d; want 3", got)
	}
	// Unrelated playlists untouched.
	if other := findPlaylist(t, path, "Other"); other == nil || len(other.Paths) != 1 || other.Folder != "Crates" {
		t.Errorf("Other playlist disturbed: %+v", other)
	}
	tmps, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmps) != 0 {
		t.Errorf("temp files not cleaned: %v", tmps)
	}
}

func TestUpsertNMLPlaylistExistingDedupe(t *testing.T) {
	path := writeFixture(t, playlistFixture)
	pa := resolveLocation("C:", "/:Music/:", "a.mp3")
	pb := resolveLocation("C:", "/:Music/:", "b.mp3")

	// a already present, b listed twice → exactly one addition.
	added, err := UpsertNMLPlaylist(path, "Existing", []string{pa, pb, pb})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if added != 1 {
		t.Fatalf("added=%d; want 1", added)
	}
	pl := findPlaylist(t, path, "Existing")
	if pl == nil || len(pl.Paths) != 2 || pl.Paths[0] != pa || pl.Paths[1] != pb {
		t.Fatalf("paths after upsert: %+v", pl)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `ENTRIES="2"`) {
		t.Error("ENTRIES not updated to 2")
	}
	if strings.Contains(string(out), `COUNT="3"`) {
		t.Error("SUBNODES COUNT changed on upsert-into-existing")
	}

	// Idempotent: nothing new → no write.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	added2, err := UpsertNMLPlaylist(path, "Existing", []string{pa, pb})
	if err != nil || added2 != 0 {
		t.Fatalf("re-upsert: added=%d err=%v; want 0/nil", added2, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("no-op upsert rewrote the file")
	}
}

func TestUpsertNMLPlaylistNested(t *testing.T) {
	path := writeFixture(t, playlistFixture)
	pb := resolveLocation("C:", "/:Music/:", "b.mp3")
	added, err := UpsertNMLPlaylist(path, "Other", []string{pb})
	if err != nil || added != 1 {
		t.Fatalf("nested upsert: added=%d err=%v", added, err)
	}
	pl := findPlaylist(t, path, "Other")
	if pl == nil || pl.Folder != "Crates" || len(pl.Paths) != 2 {
		t.Fatalf("nested playlist after upsert: %+v", pl)
	}
}

func TestRemoveFromNMLPlaylist(t *testing.T) {
	path := writeFixture(t, playlistFixture)
	pa := resolveLocation("C:", "/:Music/:", "a.mp3")

	removed, err := RemoveFromNMLPlaylist(path, "Existing", []string{pa})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d; want 1", removed)
	}
	pl := findPlaylist(t, path, "Existing")
	if pl == nil || len(pl.Paths) != 0 {
		t.Fatalf("playlist after prune: %+v", pl)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `ENTRIES="0"`) {
		t.Error("ENTRIES not updated to 0")
	}
	// Other playlist keeps its entry.
	if other := findPlaylist(t, path, "Other"); other == nil || len(other.Paths) != 1 {
		t.Errorf("Other playlist disturbed: %+v", other)
	}

	// Nothing left to remove → no write.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	removed2, err := RemoveFromNMLPlaylist(path, "Existing", []string{pa})
	if err != nil || removed2 != 0 {
		t.Fatalf("re-remove: removed=%d err=%v; want 0/nil", removed2, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("no-op remove rewrote the file")
	}

	// Unknown playlist → 0, no error, no write.
	if n, err := RemoveFromNMLPlaylist(path, "Nope", []string{pa}); err != nil || n != 0 {
		t.Fatalf("unknown playlist: removed=%d err=%v", n, err)
	}
}
