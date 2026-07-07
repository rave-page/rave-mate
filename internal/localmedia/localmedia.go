// Package localmedia is the shared local-filesystem browse surface: directory listing +
// well-known default paths, in the byte-exact shape the web client expects (web
// LocalDirectoryListing / LocalDefaultPaths, app/src/types/global.d.ts). One implementation
// drives both the Local Studio WS server (internal/studio) and the LAN peer-control RPC
// (internal/remotectl) so remote file-browsing streams the controlled machine's filesystem to
// the controller instead of popping a native dialog - same logic system everywhere.
package localmedia

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry is one directory entry (web LocalEntry).
type Entry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDirectory bool   `json:"isDirectory"`
	SizeBytes   int64  `json:"sizeBytes"`
	ModifiedAt  string `json:"modifiedAt"`
	CreatedAt   string `json:"createdAt"`
	Kind        string `json:"kind"` // directory|video|audio|image|other
	Extension   string `json:"extension"`
}

// Listing is a directory's contents (web LocalDirectoryListing). Parent is nil at the root.
type Listing struct {
	Path    string  `json:"path"`
	Parent  *string `json:"parent"`
	Entries []Entry `json:"entries"`
	Error   string  `json:"error,omitempty"`
}

// DefaultPaths are the well-known user folders (web LocalDefaultPaths).
type DefaultPaths struct {
	Home      string `json:"home"`
	Desktop   string `json:"desktop"`
	Documents string `json:"documents"`
	Downloads string `json:"downloads"`
	Music     string `json:"music"`
	Videos    string `json:"videos"`
	Pictures  string `json:"pictures"`
}

var (
	videoExt = set(".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v", ".wmv", ".flv")
	audioExt = set(".mp3", ".wav", ".aac", ".m4a", ".flac", ".opus", ".ogg", ".aiff")
	imageExt = set(".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp")
)

// Defaults returns the well-known user folders ("" for any that can't be resolved).
func Defaults() DefaultPaths {
	home, _ := os.UserHomeDir()
	join := func(sub string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(home, sub)
	}
	return DefaultPaths{
		Home:      home,
		Desktop:   join("Desktop"),
		Documents: join("Documents"),
		Downloads: join("Downloads"),
		Music:     join("Music"),
		Videos:    join("Videos"),
		Pictures:  join("Pictures"),
	}
}

// ListDirectory reads target and returns its entries (dirs first, then case-insensitive name).
// A read error is reported in Listing.Error (never panics); Entries is always non-nil.
func ListDirectory(target string, includeHidden bool) Listing {
	if strings.TrimSpace(target) == "" {
		return Listing{Path: target, Entries: []Entry{}, Error: "Invalid path"}
	}
	resolved := filepath.Clean(target)
	dirents, err := os.ReadDir(resolved)
	if err != nil {
		return Listing{Path: resolved, Entries: []Entry{}, Error: err.Error()}
	}

	entries := make([]Entry, 0, len(dirents))
	for _, de := range dirents {
		name := de.Name()
		if !includeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(resolved, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		ext := ""
		if !isDir {
			ext = strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
		}
		mod := info.ModTime().UTC().Format(time.RFC3339)
		entries = append(entries, Entry{
			Name:        name,
			Path:        full,
			IsDirectory: isDir,
			SizeBytes:   info.Size(),
			ModifiedAt:  mod,
			CreatedAt:   mod, // Go stdlib has no portable birthtime; mirror modifiedAt
			Kind:        classifyKind(isDir, ext),
			Extension:   ext,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDirectory != entries[j].IsDirectory {
			return entries[i].IsDirectory
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parent := filepath.Dir(resolved)
	var parentVal *string
	if parent != resolved {
		parentVal = &parent
	}
	return Listing{Path: resolved, Parent: parentVal, Entries: entries}
}

func classifyKind(isDir bool, ext string) string {
	if isDir {
		return "directory"
	}
	dot := "." + ext
	switch {
	case videoExt[dot]:
		return "video"
	case audioExt[dot]:
		return "audio"
	case imageExt[dot]:
		return "image"
	default:
		return "other"
	}
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}
