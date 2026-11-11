//go:build linux

package sysexec

import (
	"os/exec"
	"syscall"
)

// hide_linux.go - Linux child isolation set at the ONE pre-start seam every supervised child
// already calls (Hide). There is no console window to hide on Linux; instead this establishes the
// isolation the daemon relies on:
//
//   - Setpgid: the child leads its OWN process group (pgid == pid). KillTree then reaps the whole
//     group with one negative-pid signal, so a PATH ffmpeg that is really a launcher shim can't
//     orphan its encoder child (the webcam/capture.go orphan class, cross-platform now).
//   - Pdeathsig=SIGKILL: the kernel kills the child if the daemon dies - the Linux analog of the
//     Windows job object's KILL_ON_JOB_CLOSE backstop, covering a daemon crash/OOM where
//     cooperative shutdown never runs. Caveat: Pdeathsig is delivered when the THREAD that forked
//     exits, not strictly the process; Go keeps worker threads alive for the daemon's lifetime so
//     this holds in practice, and cooperative shutdown + KillTree remain the primary guarantee.
//
// StartDetached deliberately does NOT call Hide, so crash-recovery relaunches (SteamVR/VRChat)
// keep outliving rave-mate.
func Hide(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.Setpgid = true
	c.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

// LowPriority / BelowNormalPriority are no-ops on Linux: SysProcAttr has no pre-start niceness and
// the real CPU discipline is applied after start by AssignToJobClass (setpriority on the group).
func LowPriority(*exec.Cmd)         {}
func BelowNormalPriority(*exec.Cmd) {}
