// Package sysactivity reports OS-level signals used to gate automation schedules: how long the
// system has been idle (no user input) and which processes are running. Windows is implemented
// via stdlib syscalls (user32 GetLastInputInfo + kernel32 Toolhelp snapshot - no new dep,
// mirroring internal/midi's winmm pattern); other platforms return a no-op (ok=false) so
// idle/process gating is simply disabled there.
package sysactivity

import (
	"strings"
	"time"
)

// Activity reports OS activity signals. Methods return ok=false on unsupported platforms.
type Activity interface {
	// IdleDuration is how long since the last keyboard/mouse input.
	IdleDuration() (time.Duration, bool)
	// RunningProcesses is the set of running executable names, lowercased, stored both with and
	// without extension (e.g. "traktor.exe" and "traktor") for forgiving matching.
	RunningProcesses() (map[string]bool, bool)
}

// ProcessInfo is one running process (Toolhelp snapshot row): pid + exe name as reported.
type ProcessInfo struct {
	PID  uint32
	Name string
}

// ListProcesses enumerates running processes with pids (for perf diagnosis / per-PID CPU
// sampling). ok=false on unsupported platforms.
func ListProcesses() ([]ProcessInfo, bool) { return listProcesses() }

// New returns the platform Activity.
func New() Activity { return newActivity() }

// NormalizeName lowercases + trims a user-entered app name for matching against RunningProcesses.
func NormalizeName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Running reports whether name (e.g. "Traktor" or "Traktor.exe") is in the running set.
func Running(set map[string]bool, name string) bool {
	n := NormalizeName(name)
	return n != "" && set[n]
}

// addProcessNames inserts exe lowercased, both with and without its extension.
func addProcessNames(set map[string]bool, exe string) {
	exe = strings.ToLower(strings.TrimSpace(exe))
	if exe == "" {
		return
	}
	set[exe] = true
	if i := strings.LastIndexByte(exe, '.'); i > 0 {
		set[exe[:i]] = true
	}
}
