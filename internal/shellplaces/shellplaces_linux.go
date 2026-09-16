//go:build linux

package shellplaces

import (
	"os"
	"path/filepath"
)

// systemPlaces reads the file-manager pins: GTK bookmarks (Nautilus/Nemo/Caja sidebar, user pins)
// then XDG user-dirs (default folders). Missing files yield an empty slice.
func systemPlaces() []Place {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" && home != "" {
		cfg = filepath.Join(home, ".config")
	}
	var out []Place
	if cfg != "" {
		if data, err := os.ReadFile(filepath.Join(cfg, "gtk-3.0", "bookmarks")); err == nil {
			out = append(out, parseGTKBookmarks(string(data))...)
		}
		if data, err := os.ReadFile(filepath.Join(cfg, "user-dirs.dirs")); err == nil && home != "" {
			out = append(out, parseXDGUserDirs(string(data), home)...)
		}
	}
	return out
}
