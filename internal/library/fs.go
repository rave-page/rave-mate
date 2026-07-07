// Package library provides a native Fyne file browser + media metadata viewer.
package library

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// kind classifies a filesystem entry by media type.
type kind string

const (
	kindDirectory kind = "directory"
	kindVideo     kind = "video"
	kindAudio     kind = "audio"
	kindImage     kind = "image"
	kindOther     kind = "other"
)

// entry is a directory listing row - mirrors internal/studio/dispatch.go's shape.
type entry struct {
	name        string
	path        string
	isDirectory bool
	sizeBytes   int64
	modifiedAt  time.Time
	kind        kind
	extension   string // lowercase, no dot
}

var (
	videoExt = extSet(".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v", ".wmv", ".flv")
	audioExt = extSet(".mp3", ".wav", ".aac", ".m4a", ".flac", ".opus", ".ogg", ".aiff")
	imageExt = extSet(".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp")
)

func extSet(exts ...string) map[string]bool {
	m := make(map[string]bool, len(exts))
	for _, e := range exts {
		m[e] = true
	}
	return m
}

func classifyKind(isDir bool, ext string) kind {
	if isDir {
		return kindDirectory
	}
	dot := "." + ext
	switch {
	case videoExt[dot]:
		return kindVideo
	case audioExt[dot]:
		return kindAudio
	case imageExt[dot]:
		return kindImage
	default:
		return kindOther
	}
}

// readDir lists dir entries. Dirs first, then case-insensitive name order.
// Dotfiles excluded unless includeHidden.
func readDir(dir string, includeHidden bool) ([]entry, error) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	entries := make([]entry, 0, len(dirents))
	for _, de := range dirents {
		name := de.Name()
		if !includeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		isDir := info.IsDir()
		ext := ""
		if !isDir {
			ext = strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
		}
		entries = append(entries, entry{
			name:        name,
			path:        full,
			isDirectory: isDir,
			sizeBytes:   info.Size(),
			modifiedAt:  info.ModTime(),
			kind:        classifyKind(isDir, ext),
			extension:   ext,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].isDirectory, entries[j].isDirectory
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
	})
	return entries, nil
}

// humanSize formats bytes as a terse human-readable string.
func humanSize(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
