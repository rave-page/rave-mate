package gridfix

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeAudioStub(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectionCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := writeAudioStub(t, dir, "a.mp3", "x")
	c, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(f); ok {
		t.Fatal("empty cache returned a hit")
	}
	det := &Detection{Beats: []float64{0.25, 0.75, 1.25}, Downbeats: []float64{0.25}}
	if err := c.Put(f, det, "ckpt-1"); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get(f)
	if !ok || len(got.Beats) != 3 || got.Beats[1] != 0.75 || len(got.Downbeats) != 1 {
		t.Fatalf("hit mismatch: ok=%v det=%+v", ok, got)
	}
	if c.Len() != 1 {
		t.Fatalf("Len=%d want 1", c.Len())
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// reopen: persisted entry survives
	c2, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Get(f); !ok {
		t.Fatal("persisted entry lost after reopen")
	}
}

func TestDetectionCacheInvalidatesOnFileChange(t *testing.T) {
	dir := t.TempDir()
	f := writeAudioStub(t, dir, "a.mp3", "x")
	c, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put(f, &Detection{Beats: []float64{1}}, ""); err != nil {
		t.Fatal(err)
	}
	// size change → new key → miss (mtime granularity too coarse to rely on)
	writeAudioStub(t, dir, "a.mp3", "xx")
	if _, ok := c.Get(f); ok {
		t.Fatal("changed file served stale detection")
	}
}

func TestDetectionCacheStatErrors(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope.mp3")
	if _, ok := c.Get(missing); ok {
		t.Fatal("missing file returned a hit")
	}
	if err := c.Put(missing, &Detection{}, ""); err == nil {
		t.Fatal("Put on missing file must error (no identity to key on)")
	}
}

func TestDetectionCacheEviction(t *testing.T) {
	dir := t.TempDir()
	c, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.maxEntries = 2
	a := writeAudioStub(t, dir, "a.mp3", "a")
	b := writeAudioStub(t, dir, "b.mp3", "b")
	f3 := writeAudioStub(t, dir, "c.mp3", "c")
	for _, p := range []string{a, b, f3} {
		if err := c.Put(p, &Detection{Beats: []float64{1}}, ""); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct At timestamps
	}
	if c.Len() != 2 {
		t.Fatalf("Len=%d want 2 after eviction", c.Len())
	}
	if _, ok := c.Get(a); ok {
		t.Fatal("oldest entry not evicted")
	}
	for _, p := range []string{b, f3} {
		if _, ok := c.Get(p); !ok {
			t.Fatalf("newer entry evicted: %s", p)
		}
	}
}

func TestDetectionCacheCorruptFileStartsFresh(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "analysis_cache.json"), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := OpenDetectionCache(dir)
	if err != nil {
		t.Fatalf("corrupt cache must open fresh, got %v", err)
	}
	if c.Len() != 0 {
		t.Fatalf("Len=%d want 0", c.Len())
	}
}
