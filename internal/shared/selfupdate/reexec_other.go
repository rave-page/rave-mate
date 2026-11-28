//go:build !windows

package selfupdate

import (
	"os"
	"syscall"
)

// Reexec replaces this process image with exe (same argv + env, plus AwaitRestartEnv): same PID,
// so a systemd unit keeps tracking it with or without Restart=. Go opens every fd O_CLOEXEC, so
// listeners/sockets/pipes close across the exec; stop child processes BEFORE calling. Returns
// only on failure.
func Reexec(exe string) error {
	return syscall.Exec(exe, os.Args, append(os.Environ(), AwaitRestartEnv+"=1"))
}
