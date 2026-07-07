//go:build !windows

package sysexec

import "os"

// AssignToJob is a no-op off Windows. On Unix the cooperative shutdown + KillTree handle child
// cleanup; a portable equivalent (prctl PDEATHSIG / setpgid) can be added if orphaning shows up.
func AssignToJob(*os.Process, bool) {}

// AssignToJobMem is a no-op off Windows (no per-process memory-cap equivalent wired yet; a
// cgroup/setrlimit RLIMIT_AS variant can be added if needed).
func AssignToJobMem(*os.Process, int) {}
