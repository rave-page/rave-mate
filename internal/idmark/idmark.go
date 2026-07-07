// Package idmark stores "ID" marks: files or directories holding unreleased tracks whose
// identity must never leak to any output (overlays, stream publisher, now-playing file,
// recorder tracklist, VR overlays). A directory mark matches recursively; matching is
// longest-prefix-wins with Windows path semantics (case-insensitive, / and \ equivalent).
// The session merger redacts its output through Match - raw data stays merger-internal.
//
// JSON in the config dir (not libdb): redaction must work even when the library DB feature
// is disabled/nil, and marks are tiny (mirrors library.Bookmarks).
package idmark

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Mark is what a marked track may still show. Title is always redacted to "ID".
type Mark struct {
	ShowArtist bool `json:"showArtist"` // keep the artist visible ("ID (Artist)" style)
	ShowLabel  bool `json:"showLabel"`  // keep label/album (release info) visible
}

// Entry is one marked path (file or directory) + its visibility flags.
type Entry struct {
	Path       string `json:"path"`
	ShowArtist bool   `json:"showArtist"`
	ShowLabel  bool   `json:"showLabel"`
}

// Store is a file-backed set of ID marks. Safe for concurrent use; persists on change.
type Store struct {
	file string
	mu   sync.RWMutex
	list []Entry
}

// Load opens (or starts) a store at file. Missing/corrupt file ⇒ empty store; "" ⇒ in-memory.
func Load(file string) *Store {
	s := &Store{file: file}
	if file == "" {
		return s
	}
	if raw, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(raw, &s.list)
	}
	return s
}

// List returns a copy of the entries (insertion order).
func (s *Store) List() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Entry{}, s.list...)
}

// Set adds or updates the mark for path (deduped on normalized path).
func (s *Store) Set(path string, m Mark) {
	s.mu.Lock()
	if i := s.indexOf(path); i >= 0 {
		s.list[i].ShowArtist, s.list[i].ShowLabel = m.ShowArtist, m.ShowLabel
	} else {
		s.list = append(s.list, Entry{Path: path, ShowArtist: m.ShowArtist, ShowLabel: m.ShowLabel})
	}
	s.mu.Unlock()
	s.save()
}

// Remove drops the mark for path (no-op if absent).
func (s *Store) Remove(path string) {
	s.mu.Lock()
	if i := s.indexOf(path); i >= 0 {
		s.list = append(s.list[:i], s.list[i+1:]...)
	}
	s.mu.Unlock()
	s.save()
}

// IsMarked reports whether this exact path has its own entry (menu toggle state -
// distinct from Match, which also matches through parent-directory marks).
func (s *Store) IsMarked(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.indexOf(path) >= 0
}

// Match resolves the mark governing path: the LONGEST marked prefix wins, so a file-level
// mark overrides its directory's. A directory mark matches itself + everything under it.
// Case-insensitive, separator-agnostic (Windows path semantics).
func (s *Store) Match(path string) (Mark, bool) {
	target := normPath(path)
	if target == "" {
		return Mark{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	best := -1
	bestLen := -1
	for i, e := range s.list {
		p := normPath(e.Path)
		if p == "" {
			continue
		}
		if target == p || strings.HasPrefix(target, p+"/") {
			if len(p) > bestLen {
				best, bestLen = i, len(p)
			}
		}
	}
	if best < 0 {
		return Mark{}, false
	}
	return Mark{ShowArtist: s.list[best].ShowArtist, ShowLabel: s.list[best].ShowLabel}, true
}

func (s *Store) indexOf(path string) int {
	n := normPath(path)
	for i, e := range s.list {
		if normPath(e.Path) == n {
			return i
		}
	}
	return -1
}

func (s *Store) save() {
	if s.file == "" {
		return
	}
	s.mu.RLock()
	raw, err := json.MarshalIndent(s.list, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return
	}
	_ = os.WriteFile(s.file, raw, 0o600)
}

// normPath canonicalizes for comparison: cleaned, forward slashes, lower-cased, no
// trailing slash. DJ libraries live on case-insensitive filesystems (Windows/macOS);
// folding everywhere keeps marks deterministic across sources that vary path casing.
func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(filepath.Clean(p), `\`, "/")
	p = strings.TrimSuffix(p, "/")
	return strings.ToLower(p)
}
