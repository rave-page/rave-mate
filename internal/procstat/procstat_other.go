//go:build !windows

package procstat

// osSample is a no-op off Windows (RSS/CPU% unavailable without an OS-specific syscall).
func osSample() (float64, float64, uint64, bool) { return 0, 0, 0, false }
