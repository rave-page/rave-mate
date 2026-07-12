package remotecache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, c *Cache, peer, path string, mtime int64, data string) string {
	t.Helper()
	w, err := c.Writer(peer, path, mtime)
	if err != nil {
		t.Fatalf("Writer: %v", err)
	}
	if _, err := w.Write([]byte(data)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p, err := w.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return p
}

// age back-dates a cached file's mtime (LRU order control).
func age(t *testing.T, p string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
}

func TestHitMiss(t *testing.T) {
	c := New(t.TempDir(), 0)
	if _, ok := c.Lookup("peerA", `C:\music\a.mp3`, 100); ok {
		t.Fatal("miss expected on empty cache")
	}
	p := put(t, c, "peerA", `C:\music\a.mp3`, 100, "bytes")
	got, ok := c.Lookup("peerA", `C:\music\a.mp3`, 100)
	if !ok || got != p {
		t.Fatalf("hit expected: ok=%v got=%q want=%q", ok, got, p)
	}
	b, err := os.ReadFile(got)
	if err != nil || string(b) != "bytes" {
		t.Fatalf("content: %q err=%v", b, err)
	}
	if _, ok := c.Lookup("peerA", `C:\music\a.mp3`, 101); ok {
		t.Fatal("mtime change must miss")
	}
	if _, ok := c.Lookup("peerB", `C:\music\a.mp3`, 100); ok {
		t.Fatal("other peer must miss")
	}
	if !strings.HasSuffix(got, ".mp3") {
		t.Fatalf("extension kept: %q", got)
	}
}

func TestCommitDropsStaleMtimeVersion(t *testing.T) {
	c := New(t.TempDir(), 0)
	old := put(t, c, "p", "/m/a.mp3", 100, "v1")
	put(t, c, "p", "/m/a.mp3", 200, "v2")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("stale mtime version not dropped: %v", err)
	}
	if _, ok := c.Lookup("p", "/m/a.mp3", 200); !ok {
		t.Fatal("fresh version missing")
	}
}

func TestEvictionLRUOrder(t *testing.T) {
	c := New(t.TempDir(), 100)
	a := put(t, c, "p", "/m/a.mp3", 1, strings.Repeat("a", 40))
	b := put(t, c, "p", "/m/b.mp3", 1, strings.Repeat("b", 40))
	age(t, a, 3*time.Hour)
	age(t, b, 2*time.Hour)
	// third 40B entry pushes total to 120 > 100: oldest (a) goes, b + new stay
	nw := put(t, c, "p", "/m/c.mp3", 1, strings.Repeat("c", 40))
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatal("oldest entry should be evicted")
	}
	for _, p := range []string{b, nw} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("kept entry gone: %s (%v)", p, err)
		}
	}
}

func TestLookupTouchProtectsFromEviction(t *testing.T) {
	c := New(t.TempDir(), 100)
	a := put(t, c, "p", "/m/a.mp3", 1, strings.Repeat("a", 40))
	b := put(t, c, "p", "/m/b.mp3", 1, strings.Repeat("b", 40))
	age(t, a, 3*time.Hour)
	age(t, b, 2*time.Hour)
	if _, ok := c.Lookup("p", "/m/a.mp3", 1); !ok { // touch a → b becomes LRU
		t.Fatal("hit expected")
	}
	put(t, c, "p", "/m/c.mp3", 1, strings.Repeat("c", 40))
	if _, err := os.Stat(a); err != nil {
		t.Fatal("touched entry must survive eviction")
	}
	if _, err := os.Stat(b); !os.IsNotExist(err) {
		t.Fatal("untouched entry should be evicted")
	}
}

func TestNewCommitNeverSelfEvicted(t *testing.T) {
	c := New(t.TempDir(), 10) // cap smaller than the single entry
	p := put(t, c, "p", "/m/a.mp3", 1, strings.Repeat("a", 40))
	if _, err := os.Stat(p); err != nil {
		t.Fatal("just-committed entry must never evict itself")
	}
}

func TestPartCleanup(t *testing.T) {
	root := t.TempDir()
	c := New(root, 100)
	w, err := c.Writer("p", "/m/a.mp3", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("half"))
	w.Abort()
	if _, err := os.Stat(w.part); !os.IsNotExist(err) {
		t.Fatal("Abort must remove the .part temp")
	}
	// orphaned .part (crashed pull) is swept by the next evict pass once old enough
	w2, err := c.Writer("p", "/m/b.mp3", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w2.Write([]byte("orphan"))
	_ = w2.f.Close()
	age(t, w2.part, 2*time.Hour)
	put(t, c, "p", "/m/c.mp3", 1, strings.Repeat("c", 200)) // over cap → evict runs
	if _, err := os.Stat(w2.part); !os.IsNotExist(err) {
		t.Fatal("stale orphan .part should be swept")
	}
}

func TestPeerCollisionSafety(t *testing.T) {
	c := New(t.TempDir(), 0)
	a := put(t, c, "peerAAAAAAAA", "/m/same.mp3", 100, "A")
	b := put(t, c, "peerBBBBBBBB", "/m/same.mp3", 100, "B")
	if a == b {
		t.Fatal("two peers, same path+mtime must not collide")
	}
	ga, _ := c.Lookup("peerAAAAAAAA", "/m/same.mp3", 100)
	gb, _ := c.Lookup("peerBBBBBBBB", "/m/same.mp3", 100)
	ba, _ := os.ReadFile(ga)
	bb, _ := os.ReadFile(gb)
	if string(ba) != "A" || string(bb) != "B" {
		t.Fatalf("cross-peer content mixup: %q %q", ba, bb)
	}
}

func TestPurge(t *testing.T) {
	root := t.TempDir()
	c := New(root, 0)
	put(t, c, "p1", "/m/a.mp3", 1, "x")
	put(t, c, "p2", "/m/b.mp3", 1, "y")
	if err := c.Purge(); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	ents, _ := os.ReadDir(root)
	if len(ents) != 0 {
		t.Fatalf("cache not empty after purge: %d entries", len(ents))
	}
	if _, ok := c.Lookup("p1", "/m/a.mp3", 1); ok {
		t.Fatal("hit after purge")
	}
}

func TestSanitizedNames(t *testing.T) {
	c := New(t.TempDir(), 0)
	p := put(t, c, "no/de:id", `/mus ic/tr<ack>*?.mp3`, 5, "x")
	base := filepath.Base(p)
	for _, bad := range `<>:"/\|?*` {
		if strings.ContainsRune(base, bad) {
			t.Fatalf("unsanitized char %q in %q", bad, base)
		}
	}
}
