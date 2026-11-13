//go:build windows

package medialink

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// parkUDPSocket duplicates the route's bound UDP socket into THIS process as a handle Go never reads
// (no net poller, no IOCP reads issued on it). The native receive session re-duplicates it into the
// child on every (re)attach; closing the udpConn afterwards leaves the socket alive through it.
func parkUDPSocket(pc packetConn) (uintptr, error) {
	sc, ok := pc.(syscall.Conn)
	if !ok {
		return 0, errors.New("medialink: route socket exposes no handle")
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var parked windows.Handle
	var derr error
	if err := rc.Control(func(fd uintptr) {
		var info windows.WSAProtocolInfo
		if derr = windows.WSADuplicateSocket(windows.Handle(fd), windows.GetCurrentProcessId(), &info); derr != nil {
			return
		}
		if info.AddressFamily != windows.AF_INET { // the native receiver speaks sockaddr_in only
			derr = errors.New("medialink: route socket is not AF_INET")
			return
		}
		parked, derr = windows.WSASocket(-1, -1, -1, &info, 0, windows.WSA_FLAG_OVERLAPPED) // FROM_PROTOCOL_INFO
	}); err != nil {
		return 0, err
	}
	if derr != nil {
		return 0, derr
	}
	return uintptr(parked), nil
}

func closeParkedSocket(h uintptr) { _ = windows.Closesocket(windows.Handle(h)) }
