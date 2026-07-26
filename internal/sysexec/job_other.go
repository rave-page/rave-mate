//go:build !windows

package sysexec

import "os"

// JobClass mirrors the Windows job classes so callers compile everywhere; off Windows there is no
// job object, so the class only documents intent. See job_windows.go for the CPU discipline.
type JobClass int

const (
	JobRealtime JobClass = iota // uncapped: live encode/decode children
	JobBatch                    // 10% aggregate CPU cap: deferrable sweeps
	JobMedia                    // 70% aggregate CPU cap: live media capture/stream children
)

// AssignToJob is a no-op off Windows. On Unix the cooperative shutdown + KillTree handle child
// cleanup; a portable equivalent (prctl PDEATHSIG / setpgid) can be added if orphaning shows up.
func AssignToJob(*os.Process, bool) {}

// AssignToJobClass is a no-op off Windows (no job objects / per-job CPU caps).
func AssignToJobClass(*os.Process, JobClass) {}

// AssignToJobMem is a no-op off Windows (no per-process memory-cap equivalent wired yet; a
// cgroup/setrlimit RLIMIT_AS variant can be added if needed).
func AssignToJobMem(*os.Process, int) {}
