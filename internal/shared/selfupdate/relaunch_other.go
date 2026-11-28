//go:build !windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// relaunch starts a detached shell that waits ~2s for this process to exit (releasing the
// single-instance lock), then re-execs the swapped binary with this process's arguments. The exe
// path + args are passed as $0/$@ (never interpolated into the script) so shell metacharacters
// are inert. The caller must exit after. extraEnv appends e.g. the relaunch cooldown. Not for a
// systemd unit (the unit's cgroup kill takes the detached shell down with us): use Reexec there.
func relaunch(extraEnv []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command("/bin/sh", append([]string{"-c", `sleep 2; exec "$0" "$@"`, exe}, os.Args[1:]...)...)
	c.Env = append(os.Environ(), AwaitRestartEnv+"=1") // wait out the old lock (belt + braces with the sleep)
	c.Env = append(c.Env, extraEnv...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	return c.Start()
}
