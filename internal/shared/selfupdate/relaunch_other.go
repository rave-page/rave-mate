//go:build !windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// relaunch starts a detached shell that waits ~2s for this process to exit (releasing the
// single-instance lock), then re-execs the swapped binary. The exe path is passed as $0
// (not interpolated into the script) so a path with shell metacharacters is inert. The
// caller must exit after. extraEnv appends e.g. the relaunch cooldown.
func relaunch(extraEnv []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command("/bin/sh", "-c", `sleep 2; exec "$0"`, exe)
	c.Env = append(os.Environ(), AwaitRestartEnv+"=1") // wait out the old lock (belt + braces with the sleep)
	c.Env = append(c.Env, extraEnv...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	return c.Start()
}
