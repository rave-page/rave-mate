//go:build linux

package sysexec

import (
	"os"
	"syscall"
)

// KillTree kills the child AND every process in its group - the real encoder behind a PATH ffmpeg
// launcher shim included. Hide put the child in its own group (pgid == pid), so one negative-pid
// SIGKILL reaps the whole tree. Falls back to a direct kill if the group signal fails (a child
// that was started without Hide, so without its own group).
func KillTree(p *os.Process) {
	if p == nil {
		return
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		_ = p.Kill()
	}
}
