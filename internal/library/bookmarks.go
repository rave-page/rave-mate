package library

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Bookmark is a saved directory favorite with a user label.
type Bookmark struct {
	Path    string    `json:"path"`
	Label   string    `json:"label"`
	AddedAt time.Time `json:"addedAt"`
}

// Bookmarks is a file-backed set of directory favorites (JSON, owner-only). Safe for
// concurrent use; persists on every change.
type Bookmarks struct {
	file string
	mu   sync.Mutex
	list []Bookmark
}

// LoadBookmarks opens (or starts) a bookmarks store at file. A missing/corrupt file
// yields an empty store. file may be "" for an in-memory-only store.
func LoadBookmarks(file string) *Bookmarks {
	b := &Bookmarks{file: file}
	if file == "" {
		return b
	}
	if raw, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(raw, &b.list)
	}
	return b
}

// List returns a copy of the bookmarks (insertion order).
func (b *Bookmarks) List() []Bookmark {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Bookmark{}, b.list...)
}

// Has reports whether path is bookmarked.
func (b *Bookmarks) Has(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.indexOf(path) >= 0
}

// Toggle adds path (with label) if absent, removes it if present; returns the new state.
func (b *Bookmarks) Toggle(path, label string) bool {
	b.mu.Lock()
	if i := b.indexOf(path); i >= 0 {
		b.list = append(b.list[:i], b.list[i+1:]...)
		b.mu.Unlock()
		b.save()
		return false
	}
	b.list = append(b.list, Bookmark{Path: path, Label: label, AddedAt: time.Now()})
	b.mu.Unlock()
	b.save()
	return true
}

// SetLabel relabels a bookmark (no-op if absent).
func (b *Bookmarks) SetLabel(path, label string) {
	b.mu.Lock()
	if i := b.indexOf(path); i >= 0 {
		b.list[i].Label = label
	}
	b.mu.Unlock()
	b.save()
}

func (b *Bookmarks) indexOf(path string) int {
	for i, bm := range b.list {
		if bm.Path == path {
			return i
		}
	}
	return -1
}

func (b *Bookmarks) save() {
	if b.file == "" {
		return
	}
	b.mu.Lock()
	raw, err := json.MarshalIndent(b.list, "", "  ")
	b.mu.Unlock()
	if err != nil {
		return
	}
	tmp := b.file + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil { // owner-only
		_ = os.Rename(tmp, b.file)
	}
}
