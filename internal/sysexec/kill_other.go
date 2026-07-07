//go:build !windows

package sysexec

import "os"

// KillTree kills the process. (Killing a co-spawned ffmpeg via a process group is a
// follow-up; on Unix exec children aren't in a separate group by default.)
func KillTree(p *os.Process) { _ = p.Kill() }
