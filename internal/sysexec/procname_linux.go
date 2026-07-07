//go:build linux

package sysexec

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// SetProcName sets the calling process's short name (/proc/self/comm, ≤15 chars) via
// prctl(PR_SET_NAME), so `top`, `htop` and `ps -o comm` show the role instead of "rave-mate".
// The parent already sets the longer argv[0] (see Named) for the full COMMAND column.
func SetProcName(name string) {
	if len(name) > 15 {
		name = name[:15]
	}
	b, err := unix.BytePtrFromString(name)
	if err != nil {
		return
	}
	_ = unix.Prctl(unix.PR_SET_NAME, uintptr(unsafe.Pointer(b)), 0, 0, 0)
}
