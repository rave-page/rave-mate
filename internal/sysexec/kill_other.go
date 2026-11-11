//go:build !windows && !linux

package sysexec

import "os"

// KillTree kills the process. (On darwin/bsd a co-spawned ffmpeg via a process group is a
// follow-up; exec children aren't in a separate group by default. Linux does the group kill -
// see kill_linux.go.)
func KillTree(p *os.Process) { _ = p.Kill() }
