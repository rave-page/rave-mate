//go:build windows

package sysexec

import (
	"os"
	"os/exec"
	"strconv"
)

// KillTree kills the process AND its descendants (e.g. a running ffmpeg). Windows doesn't
// kill children when a parent dies, so use taskkill /T to terminate the whole tree.
func KillTree(p *os.Process) {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.Pid))
	Hide(cmd)
	_ = cmd.Run()
	_ = p.Kill() // fallback
}
