package overlayart

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"sync"
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEnsureFromFallbackCachesAndNormalizes(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, logbus.New(16))
	calls := 0
	r.SetFallback(func(_ context.Context, _ session.DeckSnapshot) ([]byte, bool) {
		calls++
		return pngBytes(t, 1200, 800), true // oversized PNG → must be scaled + re-encoded JPEG
	})

	d := session.DeckSnapshot{Deck: "A", ArtKey: "abc123"}
	path, ok := r.Ensure(context.Background(), d)
	if !ok || path == "" {
		t.Fatal("expected art from fallback")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("cached art not decodable: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("cached art should be jpeg, got %s", format)
	}
	if img.Width > maxDim || img.Height > maxDim {
		t.Errorf("cached art not scaled down: %dx%d", img.Width, img.Height)
	}

	// Second call is a cache hit - fallback not invoked again.
	if _, ok := r.Ensure(context.Background(), d); !ok {
		t.Fatal("expected cache hit")
	}
	if calls != 1 {
		t.Errorf("fallback should run once (cache hit second time), ran %d", calls)
	}
}

func TestEnsureNegativeCache(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, logbus.New(16))
	calls := 0
	r.SetFallback(func(_ context.Context, _ session.DeckSnapshot) ([]byte, bool) {
		calls++
		return nil, false // no art anywhere
	})
	d := session.DeckSnapshot{Deck: "A", ArtKey: "missing"}
	if _, ok := r.Ensure(context.Background(), d); ok {
		t.Fatal("expected miss")
	}
	if _, ok := r.Ensure(context.Background(), d); ok {
		t.Fatal("expected miss")
	}
	if calls != 1 {
		t.Errorf("negative cache should suppress re-probe, fallback ran %d times", calls)
	}
}

func TestEnsureNoKey(t *testing.T) {
	r := New(t.TempDir(), logbus.New(16))
	if _, ok := r.Ensure(context.Background(), session.DeckSnapshot{}); ok {
		t.Fatal("no art key → no art")
	}
}

// fakeStore is an in-memory overlayart.Store for tests.
type fakeStore struct {
	rows map[string]struct {
		data []byte
		mime string
	}
	byMeta map[string][]byte // "artist|title" → bytes (a unique name match)
	puts   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]struct {
		data []byte
		mime string
	}{}, byMeta: map[string][]byte{}}
}

func (s *fakeStore) Get(path string) ([]byte, string, bool) {
	r, ok := s.rows[path]
	return r.data, r.mime, ok
}
func (s *fakeStore) GetByMeta(artist, title string) ([]byte, bool) {
	b, ok := s.byMeta[artist+"|"+title]
	return b, ok && len(b) > 0
}
func (s *fakeStore) Put(path, _, _ string, data []byte, mime, _ string) {
	s.puts++
	s.rows[path] = struct {
		data []byte
		mime string
	}{data, mime}
}

// A store hit serves the persisted bytes (mirrored to the disk cache) without re-extracting.
func TestEnsureFromStore(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, logbus.New(16))
	st := newFakeStore()
	r.SetStore(st)
	norm, _ := normalize(pngBytes(t, 600, 400)) // pre-store a normalized JPEG
	st.rows["C:/music/a.flac"] = struct {
		data []byte
		mime string
	}{norm, "image/jpeg"}

	d := session.DeckSnapshot{Deck: "A", ArtKey: "k1", Path: "C:/music/a.flac"}
	path, ok := r.Ensure(context.Background(), d)
	if !ok || path == "" {
		t.Fatal("expected store hit to serve art")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store art not mirrored to disk cache: %v", err)
	}
	if st.puts != 0 {
		t.Errorf("store hit should not re-write the store, puts=%d", st.puts)
	}
}

// Concurrent Ensure of the same key (the 4-sinks-plus-/art-handler herd) must be single-flighted:
// the fallback/extraction runs ONCE, not 24×, and a follow-up resolves from the cache. Verifies the
// herd that caused the multi-minute stall is gone, and the disk write is race-safe (run under -race).
func TestEnsureConcurrentSameKey(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, logbus.New(16))
	var mu sync.Mutex
	calls := 0
	r.SetFallback(func(_ context.Context, _ session.DeckSnapshot) ([]byte, bool) {
		mu.Lock()
		calls++
		mu.Unlock()
		return pngBytes(t, 400, 400), true
	})
	d := session.DeckSnapshot{Deck: "A", ArtKey: "race1"}
	const n = 24
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Ensure(context.Background(), d)
		}()
	}
	wg.Wait()
	if calls > 2 { // single-flight: at most a couple ran (a straggler may re-acquire post-release)
		t.Fatalf("extraction not single-flighted: fallback ran %d times", calls)
	}
	if _, err := os.Stat(r.CachePath("race1")); err != nil {
		t.Fatalf("cache file missing after concurrent resolve: %v", err)
	}
	if _, ok := r.Ensure(context.Background(), d); !ok { // follow-up is a clean cache hit
		t.Fatal("follow-up Ensure should hit the cache")
	}
}

// A no-art marker in the store is a definitive miss (no fallback re-probe).
func TestEnsureStoreNoneMarker(t *testing.T) {
	r := New(t.TempDir(), logbus.New(16))
	st := newFakeStore()
	r.SetStore(st)
	calls := 0
	r.SetFallback(func(_ context.Context, _ session.DeckSnapshot) ([]byte, bool) {
		calls++
		return pngBytes(t, 10, 10), true
	})
	st.rows["C:/music/none.wav"] = struct {
		data []byte
		mime string
	}{nil, "none"}

	d := session.DeckSnapshot{Deck: "A", ArtKey: "k2", Path: "C:/music/none.wav"}
	if _, ok := r.Ensure(context.Background(), d); ok {
		t.Fatal("none-marker should be a miss")
	}
	if calls != 0 {
		t.Errorf("none-marker must not trigger fallback, ran %d", calls)
	}
}

// The backfill (EnsurePath) persists a none-marker for a file with no extractable art so it isn't
// re-probed. The live Ensure path must NOT (a transient timeout shouldn't poison a real cover).
func TestEnsurePathPersistsNoneMarker(t *testing.T) {
	r := New(t.TempDir(), logbus.New(16))
	st := newFakeStore()
	r.SetStore(st)
	if stored := r.EnsurePath(context.Background(), "C:/music/ghost.flac", "Ghost", "Nope"); stored {
		t.Fatal("nonexistent file → nothing stored")
	}
	if _, mime, analyzed := st.Get("C:/music/ghost.flac"); !analyzed || mime != "none" {
		t.Errorf("backfill miss should persist a none-marker, got analyzed=%v mime=%q", analyzed, mime)
	}
}

// A deck with no path yet (before Traktor sends filePath) resolves its cover by a unique
// artist+title match in the store - the user's "one exact name match = canonical" rule.
func TestEnsureResolvesByMeta(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, logbus.New(16))
	st := newFakeStore()
	r.SetStore(st)
	norm, _ := normalize(pngBytes(t, 300, 300))
	st.byMeta["Opsen|Consomme (Original Mix)"] = norm

	d := session.DeckSnapshot{Deck: "A", ArtKey: "nameKey", Artist: "Opsen", Title: "Consomme (Original Mix)"} // no Path
	path, ok := r.Ensure(context.Background(), d)
	if !ok || path == "" {
		t.Fatal("expected name-based resolution to serve art")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("name-resolved art not cached to disk: %v", err)
	}
}

// The live Ensure path must NOT persist a none-marker on a miss (could be a transient failure).
func TestEnsureLiveMissDoesNotPersistNone(t *testing.T) {
	r := New(t.TempDir(), logbus.New(16))
	st := newFakeStore()
	r.SetStore(st)
	d := session.DeckSnapshot{Deck: "A", ArtKey: "k3", Path: "C:/music/ghost.flac"}
	if _, ok := r.Ensure(context.Background(), d); ok {
		t.Fatal("nonexistent file → miss")
	}
	if _, _, analyzed := st.Get("C:/music/ghost.flac"); analyzed {
		t.Error("live Ensure must not persist a none-marker (transient-failure safety)")
	}
}
