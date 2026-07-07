//go:build windows

package discovery

import "syscall"

// reuseControl sets SO_REUSEADDR before bind so multiple processes (and the OS's own
// mDNSResponder/Bonjour) can share UDP :5353 - and a second rave-mate on this host can bind.
func reuseControl(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return serr
}
