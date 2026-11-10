package gridfix

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// VerifiedGrid is a track the DJ marked as having a correct grid (BPM + marker) -
// the training set for model fine-tuning. BPM/StartMs are captured AT MARKING TIME
// so a later collection edit can't silently poison the dataset.
type VerifiedGrid struct {
	Path       string  `json:"path"`
	BPM        float64 `json:"bpm"`
	StartMs    float64 `json:"startMs"` // grid marker position
	VerifiedAt string  `json:"verifiedAt"`
}

// Pin tolerances: looser than float noise (173.999512 vs 174.000000) and NML rounding,
// tighter than any correction a plan would apply.
const (
	pinBPMTol     = 0.05 // BPM
	pinStartMsTol = 20.0 // grid marker (ms)
)

// Stale reports whether the pin no longer describes the collection's grid (bpm, startMs):
// the DJ software re-analyzed or the user re-gridded since marking. A stale pin must not
// protect the current grid - it vouches for one that no longer exists (2026-10-30: Traktor
// re-analyzed 1.5k pinned-or-fixed tracks; pins still said 173 where the collection said 174).
func (v VerifiedGrid) Stale(bpm, startMs float64) bool {
	return math.Abs(v.BPM-bpm) > pinBPMTol || math.Abs(v.StartMs-startMs) > pinStartMsTol
}

// VerifiedStore persists verified-grid marks as JSON under the gridfix data dir.
// Bounded by the user's collection size; single-writer via mu.
type VerifiedStore struct {
	mu   sync.Mutex
	path string
	m    map[string]VerifiedGrid
}

// OpenVerifiedStore loads (or initializes) the store at dataDir/verified.json.
func OpenVerifiedStore(dataDir string) (*VerifiedStore, error) {
	s := &VerifiedStore{path: filepath.Join(dataDir, "verified.json"), m: map[string]VerifiedGrid{}}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var list []VerifiedGrid
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	for _, v := range list {
		s.m[v.Path] = v
	}
	return s, nil
}

// Mark records (or refreshes) a verified grid.
func (s *VerifiedStore) Mark(path string, bpm, startMs float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[path] = VerifiedGrid{Path: path, BPM: bpm, StartMs: startMs,
		VerifiedAt: time.Now().UTC().Format(time.RFC3339)}
	return s.saveLocked()
}

// Unmark removes a verified grid.
func (s *VerifiedStore) Unmark(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, path)
	return s.saveLocked()
}

// Has reports whether path is marked verified.
func (s *VerifiedStore) Has(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[path]
	return ok
}

// Pinned returns the mark for path (ok=false when not verified).
func (s *VerifiedStore) Pinned(path string) (VerifiedGrid, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[path]
	return v, ok
}

// All returns the verified grids sorted by path.
func (s *VerifiedStore) All() []VerifiedGrid {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]VerifiedGrid, 0, len(s.m))
	for _, v := range s.m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Count returns the number of verified grids.
func (s *VerifiedStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

func (s *VerifiedStore) saveLocked() error {
	list := make([]VerifiedGrid, 0, len(s.m))
	for _, v := range s.m {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	raw, err := json.MarshalIndent(list, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
