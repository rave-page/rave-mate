//go:build windows

package vroverlay

import (
	"syscall"
	"unsafe"
)

// We never start SteamVR ourselves: VR_Init(Overlay) would launch vrserver, so the reconnect loop
// re-opened SteamVR the instant the user closed it. Gate Init on vrserver.exe actually running.

var (
	svKernel32        = syscall.NewLazyDLL("kernel32.dll")
	svCreateSnapshot  = svKernel32.NewProc("CreateToolhelp32Snapshot")
	svProcess32FirstW = svKernel32.NewProc("Process32FirstW")
	svProcess32NextW  = svKernel32.NewProc("Process32NextW")
	svCloseHandle     = svKernel32.NewProc("CloseHandle")
)

const (
	svSnapProcess = 0x00000002
	svMaxPath     = 260
)

type svProcessEntry32 struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [svMaxPath]uint16
}

// steamvrRunning reports whether SteamVR's server process (vrserver.exe) is up. Walks the Toolhelp
// process snapshot (stdlib syscall, no dep). Fails OPEN (true) if the snapshot can't be taken, so a
// transient error doesn't permanently stop the overlay from connecting.
func steamvrRunning() bool {
	snap, _, _ := svCreateSnapshot.Call(svSnapProcess, 0)
	if snap == 0 || snap == ^uintptr(0) {
		return true
	}
	defer func() { _, _, _ = svCloseHandle.Call(snap) }()
	var pe svProcessEntry32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	if r, _, _ := svProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
		return true
	}
	for {
		if syscall.UTF16ToString(pe.szExeFile[:]) == "vrserver.exe" {
			return true
		}
		if r, _, _ := svProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
			break
		}
	}
	return false
}
