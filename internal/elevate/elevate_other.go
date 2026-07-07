//go:build !windows

package elevate

import "os"

// IsElevated reports whether the process runs as root.
func IsElevated() bool { return os.Geteuid() == 0 }

// RunSelfElevated is unsupported off Windows for now (no UAC; a pkexec/sudo path can be added
// when a non-Windows desktop target needs it).
func RunSelfElevated(_ []string) (int, error) { return -1, ErrUnsupported }

// StartElevated is unsupported off Windows (no UAC); callers fall back to a normal start.
func StartElevated(_ string, _ []string, _ string) error { return ErrUnsupported }
