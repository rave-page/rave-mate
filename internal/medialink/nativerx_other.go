//go:build !windows

package medialink

import "errors"

// parkUDPSocket: the native receive child exists on Windows only (native/zigenc); elsewhere routes keep
// the Go receive path.
func parkUDPSocket(packetConn) (uintptr, error) {
	return 0, errors.New("medialink: native receive is Windows-only")
}

func closeParkedSocket(uintptr) {}
