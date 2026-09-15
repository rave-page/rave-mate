package virtualdjsrc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStripLeadingTime: leading HH:MM[:SS] + separator stripped; no time token → unchanged.
func TestStripLeadingTime(t *testing.T) {
	cases := []struct{ in, want string }{
		{"22:15:03 : Artist - Title", "Artist - Title"},
		{"22:15:03 - Artist - Title", "Artist - Title"},
		{"22:15 - Artist - Title", "Artist - Title"},   // no seconds
		{"22:15:03\tArtist - Title", "Artist - Title"}, // tab separator
		{"9:05 : DJ - Track", "DJ - Track"},            // 1-digit hour
		{"Artist - Title", "Artist - Title"},           // no time token
		{"Track - No Time", "Track - No Time"},         // separator but no leading time
	}
	for _, c := range cases {
		if got := stripLeadingTime(c.in); got != c.want {
			t.Errorf("stripLeadingTime(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

// TestLastTrackTxtWithTimestamp: a Tracklist.txt whose lines carry a leading time token parses
// to the real artist/title (not "22:15:03…").
func TestLastTrackTxtWithTimestamp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Tracklist.txt")
	writeFile(t, p, "22:10:00 : Alpha - First\n22:15:03 : Bravo - Second\n")
	if artist, title := lastTrack(p); artist != "Bravo" || title != "Second" {
		t.Errorf("timestamped .txt: got artist=%q title=%q", artist, title)
	}
}

// TestLastTrackTxtNoTimestamp: a plain "Artist - Title" .txt still parses correctly.
func TestLastTrackTxtNoTimestamp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "Tracklist.txt")
	writeFile(t, p, "Alpha - First\nBravo - Second\n")
	if artist, title := lastTrack(p); artist != "Bravo" || title != "Second" {
		t.Errorf("plain .txt: got artist=%q title=%q", artist, title)
	}
}

// TestLastTrackM3U: #EXTINF lines parse to artist/title; path lines ignored.
func TestLastTrackM3U(t *testing.T) {
	p := filepath.Join(t.TempDir(), "2026-09-15.m3u")
	writeFile(t, p, "#EXTM3U\n#EXTINF:210,Alpha - First\nC:\\Music\\a.mp3\n#EXTINF:198,Bravo - Second\nC:\\Music\\b.mp3\n")
	if artist, title := lastTrack(p); artist != "Bravo" || title != "Second" {
		t.Errorf(".m3u: got artist=%q title=%q", artist, title)
	}
}

// TestNewestHistoryPrefersM3U: with both present, the .m3u wins even when the .txt is newer.
func TestNewestHistoryPrefersM3U(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "Tracklist.txt")
	m3u := filepath.Join(dir, "session.m3u")
	writeFile(t, txt, "22:00:00 : X - Y\n")
	writeFile(t, m3u, "#EXTINF:1,X - Y\n")
	// Make the .txt NEWER than the .m3u to prove preference is by type, not mtime.
	old, newer := time.Now().Add(-time.Hour), time.Now()
	if err := os.Chtimes(m3u, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(txt, newer, newer); err != nil {
		t.Fatal(err)
	}
	best, _, ok := newestHistory(dir)
	if !ok {
		t.Fatal("newestHistory: no file found")
	}
	if best != m3u {
		t.Errorf("newestHistory: got %q want the .m3u %q", best, m3u)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
