package musiclib

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func cacheCounts(t *testing.T, c *SessionCache) (scanned, parsed int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastScanned, c.lastParsed
}

func TestSessionCacheParsesOnceThenReuses(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "history_2025y01m10d_20h00m00s.nml",
		historyNML("OldSet", "C:/:Music/:OldSet.flac", 3600.0))
	writeHistoryFile(t, dir, "history_2025y06m15d_22h30m00s.nml",
		historyNML("NewSet", "C:/:Music/:NewSet.flac", 5400.0))

	c := NewSessionCache()
	sessions, err := c.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	if sessions[0].Name != "history_2025y06m15d_22h30m00s.nml" {
		t.Errorf("not newest-first: %q", sessions[0].Name)
	}
	if sc, p := cacheCounts(t, c); sc != 2 || p != 2 {
		t.Fatalf("first load: scanned=%d parsed=%d, want 2/2", sc, p)
	}

	// Unchanged files → scanned but not re-parsed.
	sessions, err = c.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("second load: want 2 sessions, got %d", len(sessions))
	}
	if sc, p := cacheCounts(t, c); sc != 2 || p != 0 {
		t.Fatalf("second load: scanned=%d parsed=%d, want 2/0", sc, p)
	}
}

func TestSessionCacheReparsesOnChange(t *testing.T) {
	dir := t.TempDir()
	name := "history_2025y05m01d_18h00m00s.nml"
	writeHistoryFile(t, dir, name, historyNML("SetA", "C:/:Music/:SetA.flac", 3000.0))

	c := NewSessionCache()
	if _, err := c.Load(dir); err != nil {
		t.Fatal(err)
	}

	// Same size + mtime bumped → change detected via mtime.
	writeHistoryFile(t, dir, name, historyNML("SetB", "C:/:Music/:SetB.flac", 3000.0))
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, name), future, future); err != nil {
		t.Fatal(err)
	}
	sessions, err := c.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, p := cacheCounts(t, c); p != 1 {
		t.Fatalf("changed file: parsed=%d, want 1", p)
	}
	if len(sessions) != 1 || len(sessions[0].Played) != 1 ||
		sessions[0].Played[0].Path != resolveKey("C:/:Music/:SetB.flac") {
		t.Fatalf("stale content served: %+v", sessions)
	}
}

func TestSessionCacheNewAndDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "history_2025y01m10d_20h00m00s.nml",
		historyNML("OldSet", "C:/:Music/:OldSet.flac", 3600.0))

	c := NewSessionCache()
	if _, err := c.Load(dir); err != nil {
		t.Fatal(err)
	}

	// New file → only it parses.
	writeHistoryFile(t, dir, "history_2025y06m15d_22h30m00s.nml",
		historyNML("NewSet", "C:/:Music/:NewSet.flac", 5400.0))
	sessions, err := c.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	if _, p := cacheCounts(t, c); p != 1 {
		t.Fatalf("new file: parsed=%d, want 1", p)
	}

	// Deleted file → evicted from cache + result.
	if err := os.Remove(filepath.Join(dir, "history_2025y01m10d_20h00m00s.nml")); err != nil {
		t.Fatal(err)
	}
	sessions, err = c.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "history_2025y06m15d_22h30m00s.nml" {
		t.Fatalf("deleted file still served: %+v", sessions)
	}
	if len(c.entries) != 1 {
		t.Fatalf("cache not evicted: %d entries", len(c.entries))
	}
}

func TestSessionCacheBadFileNotRetried(t *testing.T) {
	dir := t.TempDir()
	writeHistoryFile(t, dir, "history_2025y04m01d_18h00m00s.nml", "not xml at all <<>>")

	c := NewSessionCache()
	sessions, err := c.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("bad file produced a session: %+v", sessions)
	}
	if _, p := cacheCounts(t, c); p != 1 {
		t.Fatalf("first load: parsed=%d, want 1", p)
	}
	if _, err := c.Load(dir); err != nil {
		t.Fatal(err)
	}
	if _, p := cacheCounts(t, c); p != 0 {
		t.Fatalf("bad file re-parsed: parsed=%d, want 0", p)
	}
}
