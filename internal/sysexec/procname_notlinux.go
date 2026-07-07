//go:build !windows && !linux

package sysexec

// SetProcName is a no-op on non-Linux Unix (macOS/BSD have no portable, supported equivalent of
// prctl(PR_SET_NAME)). The argv[0] set by Named already covers `ps`'s COMMAND column there.
func SetProcName(string) {}
