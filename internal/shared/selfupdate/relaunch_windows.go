//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// relaunch launches the binary directly, fully detached from this (dying) process, then returns -
// the caller must exit promptly. The new instance is told via AwaitRestartEnv to wait out our
// single-instance lock as we exit (no fragile timing). extraEnv appends e.g. the relaunch cooldown.
//
// NOTE: the previous `cmd /c timeout & start` shim was BROKEN under DETACHED_PROCESS - with no
// console, `timeout` aborts and `start` never launches the app (verified: 0 processes after an
// update relaunch). A direct CreateProcess comes up healthy (all feature subprocesses run).
func relaunch(extraEnv []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(exe)
	c.Env = append(os.Environ(), AwaitRestartEnv+"=1")
	c.Env = append(c.Env, extraEnv...)
	// DETACHED_PROCESS (0x8) | CREATE_NEW_PROCESS_GROUP (0x200): outlive the parent's console +
	// process group so the new app survives our exit.
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}
