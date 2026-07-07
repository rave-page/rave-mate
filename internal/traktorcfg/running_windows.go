//go:build windows

package traktorcfg

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsRunning reports whether Traktor.exe is currently running - callers MUST refuse to touch
// Settings.tsi when true, since Traktor rewrites it on exit and would clobber our change.
func IsRunning() (bool, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return false, err
	}
	for {
		if isTraktorExe(windows.UTF16ToString(e.ExeFile[:])) {
			return true, nil
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			return false, nil // ERROR_NO_MORE_FILES - end of list
		}
	}
}
