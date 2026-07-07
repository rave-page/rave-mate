//go:build !windows

package discovery

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// reuseControl sets SO_REUSEADDR + SO_REUSEPORT before bind so multiple processes (and the
// host mDNS daemon) can share UDP :5353 - and a second rave-mate on this host can bind.
func reuseControl(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		if e := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); e != nil {
			serr = e
			return
		}
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err != nil {
		return err
	}
	return serr
}
