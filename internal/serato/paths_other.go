//go:build !windows

package serato

import (
	"os"
	"path/filepath"
)

// musicDir returns ~/Music (Serato's default parent for _Serato_ on macOS/Linux).
func musicDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Music")
	}
	return "Music"
}
