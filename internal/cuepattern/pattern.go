package cuepattern

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/musiclib"
)

// PatternCue is one cue in a pattern, positioned in beats relative to the anchor drop.
type PatternCue struct {
	Beats    float64          `json:"beats"` // signed offset from the drop (negative = before)
	Name     string           `json:"name,omitempty"`
	Kind     musiclib.CueKind `json:"kind"`               // hot | cue (memory) | loop
	Hotcue   int              `json:"hotcue"`             // preferred pad slot; -1 = memory/none
	LenBeats float64          `json:"lenBeats,omitempty"` // loop length in beats (0 = point)
}

// Pattern is a named, reusable cue layout anchored at a drop.
type Pattern struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Cues      []PatternCue `json:"cues"`
	CreatedAt string       `json:"createdAt,omitempty"` // RFC3339
	FromTrack string       `json:"fromTrack,omitempty"` // title of the track it was extracted from
}

// Store persists patterns as one JSON file (small, human-recoverable).
type Store struct {
	mu   sync.Mutex
	path string
	pats []Pattern
}

// OpenStore loads (or initializes) the pattern store in dir.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "patterns.json")}
	raw, err := os.ReadFile(s.path)
	if err == nil {
		if jerr := json.Unmarshal(raw, &s.pats); jerr != nil {
			return nil, fmt.Errorf("cuepattern store corrupt: %w", jerr)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// List returns all patterns, name-sorted.
func (s *Store) List() []Pattern {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Pattern(nil), s.pats...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Get returns a pattern by id.
func (s *Store) Get(id string) (Pattern, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pats {
		if p.ID == id {
			return p, true
		}
	}
	return Pattern{}, false
}

// Save inserts or replaces (by ID; empty ID gets one assigned) and persists.
func (s *Store) Save(p Pattern) (Pattern, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		p.ID = fmt.Sprintf("cp-%d", time.Now().UnixNano())
	}
	if p.CreatedAt == "" {
		p.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	replaced := false
	for i := range s.pats {
		if s.pats[i].ID == p.ID {
			s.pats[i], replaced = p, true
			break
		}
	}
	if !replaced {
		s.pats = append(s.pats, p)
	}
	return p, s.persistLocked()
}

// Delete removes a pattern by id and persists.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.pats {
		if s.pats[i].ID == id {
			s.pats = append(s.pats[:i], s.pats[i+1:]...)
			return s.persistLocked()
		}
	}
	return nil
}

func (s *Store) persistLocked() error {
	raw, err := json.MarshalIndent(s.pats, "", " ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
