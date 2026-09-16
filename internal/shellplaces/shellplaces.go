// Package shellplaces enumerates the OS "quick access" / bookmarked directories - the pinned +
// frequent folders a native file browser shows in its sidebar - so the in-app file browser mirrors
// them. Windows: the Quick Access shell folder over COM. Linux: GTK bookmarks + XDG user-dirs.
// macOS: empty (the Finder sidebar is a binary bookmark blob, not readably parseable). Results are
// de-duplicated, capped, and cached with a short TTL; enumerate OFF the render path (a cold network
// drive can stall the shell call).
package shellplaces

import (
	"bufio"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Place is one sidebar directory. Pinned marks a user pin where the OS exposes that flag (GTK
// bookmarks; Windows does not surface a per-item pinned flag - see shellplaces_windows.go).
type Place struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Pinned bool   `json:"pinned"`
}

const (
	cacheTTL  = 30 * time.Second // bounded staleness; Explorer pins rarely change mid-session
	maxPlaces = 40               // cap: a runaway frequent list never floods the sidebar
)

var (
	mu       sync.Mutex
	cached   []Place
	cachedAt time.Time
	cacheOK  bool
)

// Places returns the OS quick-access directories (pinned first where known), de-duplicated and
// capped. Cached with a short TTL. Safe to call from any goroutine; never panics.
func Places() []Place {
	mu.Lock()
	defer mu.Unlock()
	if cacheOK && time.Since(cachedAt) < cacheTTL {
		return append([]Place(nil), cached...)
	}
	cached = normalize(systemPlaces())
	cachedAt, cacheOK = time.Now(), true
	return append([]Place(nil), cached...)
}

// Invalidate drops the cache (call after the user pins/unpins, so the next read re-enumerates).
func Invalidate() {
	mu.Lock()
	cacheOK = false
	mu.Unlock()
}

// normalize sorts pinned-first (stable), drops blanks + duplicates (case-insensitive clean path),
// fills a missing name from the base, and caps the list.
func normalize(in []Place) []Place {
	// pinned first, original order preserved within each group
	pinned := make([]Place, 0, len(in))
	rest := make([]Place, 0, len(in))
	for _, p := range in {
		if p.Pinned {
			pinned = append(pinned, p)
		} else {
			rest = append(rest, p)
		}
	}
	ordered := append(pinned, rest...)
	seen := map[string]bool{}
	out := make([]Place, 0, len(ordered))
	for _, p := range ordered {
		if strings.TrimSpace(p.Path) == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(p.Path))
		if seen[key] {
			continue
		}
		seen[key] = true
		if strings.TrimSpace(p.Name) == "" {
			p.Name = filepath.Base(filepath.Clean(p.Path))
		}
		out = append(out, p)
		if len(out) >= maxPlaces {
			break
		}
	}
	return out
}

// parseGTKBookmarks reads a GTK ~/.config/gtk-3.0/bookmarks file: one "file:///path[ Label]" per
// line. These are user pins, so Pinned=true. Non-file:// lines (remote mounts) are skipped.
func parseGTKBookmarks(data string) []Place {
	var out []Place
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "file://") {
			continue
		}
		uri, label := line, ""
		if i := strings.IndexByte(line, ' '); i >= 0 {
			uri, label = line[:i], strings.TrimSpace(line[i+1:])
		}
		u, err := url.Parse(uri)
		if err != nil || u.Path == "" {
			continue
		}
		p := filepath.FromSlash(u.Path)
		if label == "" {
			label = filepath.Base(p)
		}
		out = append(out, Place{Path: p, Name: label, Pinned: true})
	}
	return out
}

// parseXDGUserDirs reads an XDG ~/.config/user-dirs.dirs file (XDG_MUSIC_DIR="$HOME/Music" lines),
// resolving $HOME against home. These are default folders, not pins, so Pinned=false.
func parseXDGUserDirs(data, home string) []Place {
	var out []Place
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "XDG_") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(line[:eq], "XDG_"), "_DIR")
		val := strings.Trim(strings.TrimSpace(line[eq+1:]), `"`)
		val = strings.ReplaceAll(val, "$HOME", home)
		if val == "" || val == home {
			continue
		}
		name := strings.Title(strings.ToLower(key)) //nolint:staticcheck // ASCII XDG keys only
		out = append(out, Place{Path: filepath.Clean(val), Name: name, Pinned: false})
	}
	return out
}
