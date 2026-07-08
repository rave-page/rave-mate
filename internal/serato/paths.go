package serato

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultDir returns the per-user Serato dir: <Music>\_Serato_, where <Music> is the resolved
// Music known folder (redirection-aware on Windows - handles Music moved to another drive or
// OneDrive), ~/Music elsewhere. Serato's own Windows default is C:\Users\<user>\Music\_Serato_.
func DefaultDir() (string, error) {
	return filepath.Join(musicDir(), "_Serato_"), nil
}

// DetectSeratoDirs returns every EXISTING _Serato_ dir, deduped, default first: the Music-folder
// default plus, on Windows, each <drive>:\_Serato_ root (Serato writes one at the root of every
// external drive it imports music from). Empty when Serato has never run.
func DetectSeratoDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(p string) {
		key := strings.ToLower(filepath.Clean(p))
		if p != "" && !seen[key] && isDir(p) {
			seen[key] = true
			dirs = append(dirs, p)
		}
	}
	if def, err := DefaultDir(); err == nil {
		add(def)
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			add(string(c) + `:\_Serato_`)
		}
	}
	return dirs
}

// SuggestedDir is the pre-select value for the Serato-dir setting: the first existing _Serato_
// dir, else the Music-folder default path (which Serato creates on first run). Never empty.
func SuggestedDir() string {
	if dirs := DetectSeratoDirs(); len(dirs) > 0 {
		return dirs[0]
	}
	def, _ := DefaultDir()
	return def
}

// DrivesSeratoDirs returns existing Serato dirs (default + external-drive roots). Alias of
// DetectSeratoDirs kept for the libsync collection loader.
func DrivesSeratoDirs() []string { return DetectSeratoDirs() }

// LoadCollection reads `database V2` + every `Subcrates\*.crate` under seratoDir.
func LoadCollection(seratoDir string) ([]Track, []Crate, error) {
	var tracks []Track
	if f, err := os.Open(filepath.Join(seratoDir, "database V2")); err == nil {
		t, perr := ParseDatabase(f)
		_ = f.Close()
		if perr != nil {
			return nil, nil, fmt.Errorf("serato: parse database V2: %w", perr)
		}
		tracks = t
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}

	var crates []Crate
	crateDir := filepath.Join(seratoDir, "Subcrates")
	entries, err := os.ReadDir(crateDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return tracks, nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".crate") {
			continue
		}
		cf, oerr := os.Open(filepath.Join(crateDir, e.Name()))
		if oerr != nil {
			continue // unreadable crate: skip, don't fail the whole collection
		}
		c, cerr := ParseCrate(cf)
		_ = cf.Close()
		if cerr != nil {
			continue // garbage crate: skip
		}
		c.Name = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		crates = append(crates, c)
	}
	return tracks, crates, nil
}

// NewestSession returns the most-recently-modified file under History\Sessions\.
func NewestSession(seratoDir string) (path string, modTime time.Time, err error) {
	dir := filepath.Join(seratoDir, "History", "Sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".session") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if path == "" || info.ModTime().After(modTime) {
			path, modTime = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	if path == "" {
		return "", time.Time{}, fmt.Errorf("serato: no .session files in %s", dir)
	}
	return path, modTime, nil
}

// isDir reports whether p exists and is a directory.
func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
