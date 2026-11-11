//go:build linux

package sysexec

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// job_linux.go - the Linux analog of the Windows job object (job_windows.go). There is no single
// kernel primitive that does kill-on-close + CPU cap + memory cap, so the guarantees are split:
//
//   - kill-on-close -> Hide's Pdeathsig=SIGKILL + KillTree's process-group reap (hide_linux.go,
//     kill_linux.go).
//   - CPU discipline -> setpriority (nice) on the child's process GROUP, applied here after start.
//   - memory cap    -> cgroup v2 memory.max on a per-child sub-cgroup WHEN the daemon's cgroup is
//     delegated (systemd --user `Delegate=yes`); otherwise a clean no-op. RLIMIT_AS is deliberately
//     NOT used: a Go child reserves a huge virtual address space, so an RLIMIT_AS cap that fits the
//     ffmpeg child would kill the Go featurehost child on startup. cgroup memory.max counts real
//     usage and works for BOTH the Go child and the ffmpeg child.
//
// AUTHORITATIVE production memory/CPU bound: the systemd --user unit's `MemoryMax=`/`CPUWeight=`
// (set by the install, caps the whole daemon+children tree). The per-child caps here are an
// opportunistic extra under that, under each Go child's own GOMEMLIMIT + medialink.memWatchdog.

// JobClass is a child's CPU-scheduling contract (matches the Windows classes). Every class relies
// on the same kill-on-close backstop; they differ only in the nice level applied to the group.
type JobClass int

const (
	JobRealtime JobClass = iota // nice 0 (untouched): live encode/decode children must keep frame rate
	JobBatch                    // nice 19: deferrable sweeps (codec probe, gridfix) - starvable
	JobMedia                    // nice 5: live media capture/stream children - bounded but responsive
)

// niceFor maps a class to its nice level; ok=false => leave priority untouched (realtime).
func niceFor(c JobClass) (int, bool) {
	switch c {
	case JobBatch:
		return 19, true
	case JobMedia:
		return 5, true
	default:
		return 0, false
	}
}

// AssignToJob applies the batch class when background, else realtime.
func AssignToJob(p *os.Process, background bool) {
	class := JobRealtime
	if background {
		class = JobBatch
	}
	AssignToJobClass(p, class)
}

// AssignToJobClass niceness-caps the child's process group per its class. Best-effort: on failure
// the child still runs, just un-niced. The child leads its own group (Hide set Setpgid), so
// setpriority on PRIO_PGRP covers it AND any descendant (the real encoder behind a PATH ffmpeg
// shim), regardless of when the shim forks.
func AssignToJobClass(p *os.Process, class JobClass) {
	if p == nil {
		return
	}
	nice, ok := niceFor(class)
	if !ok {
		return
	}
	// PRIO_PGRP on the leader pid (== pgid) hits the whole group; fall back to PRIO_PROCESS if the
	// child has no own group (started without Hide).
	if err := unix.Setpriority(unix.PRIO_PGRP, p.Pid, nice); err != nil {
		_ = unix.Setpriority(unix.PRIO_PROCESS, p.Pid, nice)
	}
}

// AssignToJobMem caps the child at capMB (realtime class).
func AssignToJobMem(p *os.Process, capMB int) { AssignToJobMemClass(p, capMB, JobRealtime) }

// AssignToJobMemClass applies the class's nice AND, when cgroup v2 is delegated, a memory.max cap
// of capMB on a per-child sub-cgroup. capMB<=0 is CPU-class only. Memory capping silently degrades
// to a no-op where the cgroup isn't delegated - the unit-level MemoryMax + the child's GOMEMLIMIT/
// memWatchdog are the bound there.
func AssignToJobMemClass(p *os.Process, capMB int, class JobClass) {
	if p == nil {
		return
	}
	AssignToJobClass(p, class)
	if capMB <= 0 {
		return
	}
	applyMemCgroup(p.Pid, int64(capMB)*1024*1024)
}

var (
	cgOnce   sync.Once
	cgBase   string // writable cgroup v2 dir with the memory controller delegated ("" = unusable)
	cgUsable bool
)

// cgroupMemReady reports (once) whether a per-child memory cgroup can be created under the daemon's
// own delegated cgroup v2 subtree, and returns that base. It PROVES it by creating a probe child
// cgroup and checking memory.max materialized (the memory controller is delegated), then removing
// it - never trusts mere directory writability.
func cgroupMemReady() (string, bool) {
	cgOnce.Do(func() {
		base, ok := cgroupV2Self()
		if !ok {
			return
		}
		probe := filepath.Join(base, "rm-probe-"+strconv.Itoa(os.Getpid()))
		if err := os.Mkdir(probe, 0o755); err != nil {
			return
		}
		_, statErr := os.Stat(filepath.Join(probe, "memory.max"))
		_ = os.Remove(probe)
		if statErr == nil {
			cgBase, cgUsable = base, true
		}
	})
	return cgBase, cgUsable
}

// cgroupV2Self resolves the daemon's cgroup v2 directory from /proc/self/cgroup (unified line
// "0::<path>"). ok=false on a cgroup-v1/hybrid host with no unified line.
func cgroupV2Self() (string, bool) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rel, found := strings.CutPrefix(line, "0::"); found {
			return filepath.Join("/sys/fs/cgroup", filepath.Clean("/"+rel)), true
		}
	}
	return "", false
}

// applyMemCgroup creates a per-child cgroup with memory.max=capBytes and moves pid into it.
// Best-effort + self-verifying: a write failure (memory controller not delegated into a freshly
// created child) removes the dir and degrades to no-op. The empty cgroup dir is left on child exit
// (cleanup is a documented follow-up; dirs are tiny and this path only runs when delegated).
func applyMemCgroup(pid int, capBytes int64) {
	base, ok := cgroupMemReady()
	if !ok {
		return
	}
	dir := filepath.Join(base, "rm-"+strconv.Itoa(pid))
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(capBytes, 10)), 0o644); err != nil {
		_ = os.Remove(dir)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		_ = os.Remove(dir)
		return
	}
}
