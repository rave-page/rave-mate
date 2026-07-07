//go:build !windows

package traktorcfg

// IsRunning is Windows-only (Traktor is Win/macOS; this build targets the Windows companion).
// On other platforms it reports false so callers don't block.
func IsRunning() (bool, error) { return false, nil }
