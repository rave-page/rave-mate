package gridfix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// cacheEntry is one persisted detection (mirrors fix_grids.py's analysis_cache.json
// role; key carries the file identity so no mtime field is needed here).
type cacheEntry struct {
	Beats      []float64 `json:"beats"`
	Downbeats  []float64 `json:"downbeats"`
	Checkpoint string    `json:"checkpoint,omitempty"` // model checkpoint that produced it
	At         string    `json:"at"`                   // RFC3339Nano insert time (eviction order)
}

const (
	// cacheSaveInterval throttles persistence: Put saves at most once per interval;
	// Close always flushes. Bound: a crash loses at most the last 30s of detections.
	cacheSaveInterval = 30 * time.Second
	// cacheMaxEntries caps the map (evict oldest by At). ~100k tracks ≈ collection
	// scale; worst-case JSON is hundreds of MB (2h sets), typical libraries far less.
	cacheMaxEntries = 100000
)

// DetectionCache persists beat detections at <dataDir>/analysis_cache.json keyed
// by path|size|mtimeUnix so unchanged files never re-analyze. Single-writer via mu.
type DetectionCache struct {
	mu         sync.Mutex
	path       string
	m          map[string]cacheEntry
	dirty      bool
	lastSave   time.Time
	maxEntries int
}

// OpenDetectionCache loads (or initializes) the cache. A corrupt file starts
// fresh - the cache is disposable, never an error source.
func OpenDetectionCache(dataDir string) (*DetectionCache, error) {
	c := &DetectionCache{
		path:       filepath.Join(dataDir, "analysis_cache.json"),
		m:          map[string]cacheEntry{},
		maxEntries: cacheMaxEntries,
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &c.m); err != nil {
		c.m = map[string]cacheEntry{} // corrupt cache: rebuild from scratch
	}
	return c, nil
}

// key builds the identity key from a live stat; stat failure = no key.
func cacheKey(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().Unix()), nil
}

// Get returns the cached detection for path IF it was produced by checkpoint. A stat error,
// a changed file, OR a different model = miss - so switching to a re-trained model (or back to
// the builtin) re-analyzes instead of silently replaying the previous model's detection.
func (c *DetectionCache) Get(path, checkpoint string) (*Detection, bool) {
	k, err := cacheKey(path)
	if err != nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || e.Checkpoint != checkpoint {
		return nil, false
	}
	return &Detection{Beats: e.Beats, Downbeats: e.Downbeats}, true
}

// Put stores a detection (stat error = error: no identity to key on). Persists
// throttled per cacheSaveInterval; Close flushes the rest.
func (c *DetectionCache) Put(path string, d *Detection, checkpoint string) error {
	k, err := cacheKey(path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = cacheEntry{
		Beats:      d.Beats,
		Downbeats:  d.Downbeats,
		Checkpoint: checkpoint,
		At:         time.Now().UTC().Format(time.RFC3339Nano),
	}
	c.evictLocked()
	c.dirty = true
	if time.Since(c.lastSave) >= cacheSaveInterval {
		return c.saveLocked()
	}
	return nil
}

// evictLocked drops oldest-by-At entries above maxEntries. O(n) scan per insert
// once full - fine at the 100k cap, and inserts past cap are rare.
func (c *DetectionCache) evictLocked() {
	for len(c.m) > c.maxEntries {
		oldestK := ""
		var oldestT time.Time
		first := true
		for k, e := range c.m {
			t, err := time.Parse(time.RFC3339Nano, e.At)
			if err != nil {
				t = time.Time{} // unparsable = oldest
			}
			if first || t.Before(oldestT) {
				oldestK, oldestT, first = k, t, false
			}
		}
		delete(c.m, oldestK)
	}
}

// Len returns the entry count (stale entries for changed files included until evicted).
func (c *DetectionCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// Close flushes pending writes.
func (c *DetectionCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	return c.saveLocked()
}

// saveLocked writes atomically (tmp + rename).
func (c *DetectionCache) saveLocked() error {
	raw, err := json.Marshal(c.m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	c.dirty = false
	c.lastSave = time.Now()
	return nil
}
