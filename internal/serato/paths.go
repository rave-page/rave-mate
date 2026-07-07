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

// DefaultDir returns the per-user Serato dir (%USERPROFILE%\Music\_Serato_ on Windows,
// ~/Music/_Serato_ elsewhere).
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Music", "_Serato_"), nil
}

// DrivesSeratoDirs returns existing Serato dirs: the per-user default plus, on Windows,
// every <drive>:\_Serato_ root (external-drive libraries). Non-Windows = just the default.
func DrivesSeratoDirs() []string {
	var dirs []string
	if def, err := DefaultDir(); err == nil && isDir(def) {
		dirs = append(dirs, def)
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			d := string(c) + `:\_Serato_`
			if isDir(d) {
				dirs = append(dirs, d)
			}
		}
	}
	return dirs
}

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
