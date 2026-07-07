//go:build !windows

package sysexec

import "os/exec"

// Named sets the child's argv[0] to a role-specific name (e.g. rave-mate-feature-player) so `ps`,
// `ps aux`, `pstree` and `top -c` show the role in the COMMAND column. execve still loads the real
// cmd.Path, so the binary is unchanged - only argv[0] differs. SetProcName (Linux) additionally
// fixes the short `comm` name.
func Named(cmd *exec.Cmd, label string) {
	if len(cmd.Args) > 0 {
		cmd.Args[0] = "rave-mate-" + label
	}
}
