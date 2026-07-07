package filexfer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileEntry is one file in a transfer manifest. Path is slash-separated and relative to the
// receiver's download dir: a single-file send is [{Path: base}]; a directory send prefixes
// every entry with the directory's base name ("shots/2026/a.png").
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// maxManifestFiles bounds a hostile/degenerate manifest.
const maxManifestFiles = 100_000

// BuildManifest walks root (file or directory, recursive; regular files only - symlinks and
// empty dirs are skipped) and returns the display name, entries, and total bytes.
func BuildManifest(root string) (name string, files []FileEntry, total int64, err error) {
	root = filepath.Clean(root)
	fi, err := os.Stat(root)
	if err != nil {
		return "", nil, 0, err
	}
	name = filepath.Base(root)
	if !safeName(name) {
		return "", nil, 0, fmt.Errorf("filexfer: unsendable path %q", root)
	}
	if !fi.IsDir() {
		if !fi.Mode().IsRegular() {
			return "", nil, 0, fmt.Errorf("filexfer: not a regular file: %q", root)
		}
		return name, []FileEntry{{Path: name, Size: fi.Size()}}, fi.Size(), nil
	}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if len(files) >= maxManifestFiles {
			return fmt.Errorf("filexfer: too many files (max %d)", maxManifestFiles)
		}
		files = append(files, FileEntry{Path: filepath.ToSlash(filepath.Join(name, rel)), Size: info.Size()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return "", nil, 0, err
	}
	if len(files) == 0 {
		return "", nil, 0, errors.New("filexfer: directory has no files")
	}
	return name, files, total, nil
}

// checkManifest validates a received manifest: local, relative, slash-only paths (path-
// traversal guard - security invariant), unique, sane sizes/counts.
func checkManifest(files []FileEntry) error {
	if len(files) == 0 || len(files) > maxManifestFiles {
		return protoErrf("bad manifest: %d files", len(files))
	}
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if f.Size < 0 || !safeRel(f.Path) || seen[f.Path] {
			return protoErrf("bad manifest entry %q", f.Path)
		}
		seen[f.Path] = true
	}
	return nil
}

// safeRel accepts only clean, local, slash-separated relative paths.
func safeRel(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return false
	}
	native := filepath.FromSlash(p)
	return filepath.IsLocal(native) && filepath.ToSlash(filepath.Clean(native)) == p
}

// safeName accepts a single path component (no separators / traversal).
func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`+"\x00")
}
