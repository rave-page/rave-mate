//go:build windows

package serato

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

const explorerKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\`

// musicDir resolves the Windows Music known folder from the shell-folders registry so a
// relocated Music folder (moved to another drive, or redirected into OneDrive) is honored -
// %USERPROFILE%\Music is only the default, not a guarantee. Falls back to that default.
func musicDir() string {
	// "Shell Folders" = resolved absolute path; "User Shell Folders" = %VAR% form (expand).
	for _, sub := range []string{"Shell Folders", "User Shell Folders"} {
		if p := readMusicKey(explorerKey + sub); p != "" {
			return p
		}
	}
	if up := os.Getenv("USERPROFILE"); up != "" {
		return filepath.Join(up, "Music")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Music")
	}
	return "Music"
}

// readMusicKey reads the "My Music" value under an Explorer shell-folders key, expanding any
// %ENV% references. "" when absent/unreadable.
func readMusicKey(path string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue("My Music")
	if err != nil || v == "" {
		return ""
	}
	if ev, eerr := registry.ExpandString(v); eerr == nil && ev != "" {
		return ev
	}
	return v
}
