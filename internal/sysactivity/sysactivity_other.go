//go:build !windows

package sysactivity

import "time"

// noopActivity disables idle/process gating on non-Windows platforms (ok=false everywhere).
type noopActivity struct{}

func newActivity() Activity { return noopActivity{} }

func (noopActivity) IdleDuration() (time.Duration, bool)       { return 0, false }
func (noopActivity) RunningProcesses() (map[string]bool, bool) { return nil, false }

func listProcesses() ([]ProcessInfo, bool) { return nil, false }
