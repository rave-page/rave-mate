//go:build !windows

package sysexec

// SetSelfBelowNormal is a no-op off Windows (no cheap per-process priority-class equivalent wired).
func SetSelfBelowNormal(below bool) {}
