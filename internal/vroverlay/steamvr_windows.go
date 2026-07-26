//go:build windows

package vroverlay

import (
	"syscall"
	"time"
	"unsafe"
)

// We never start SteamVR ourselves: VR_Init(VRApplication_Overlay) LAUNCHES it through the runtime
// (only VRApplication_Background is non-launching; VR_IsHmdPresent / VR_IsRuntimeInstalled never
// launch either). This process walk is the gate in front of that call - a pure Toolhelp snapshot,
// zero OpenVR, so it is launch-incapable by construction.

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
	// CreateToolhelp32Snapshot is documented to fail with ERROR_BAD_LENGTH while processes churn and
	// to need a retry - the ONLY reason the old code failed open. Retry, then fail closed.
	svSnapRetries   = 4
	svSnapRetryWait = 25 * time.Millisecond
)

// svProcNames are the SteamVR processes that prove the runtime is already up: vrserver (the runtime
// server) and vrmonitor (its status window, present through startup before vrserver binds).
var svProcNames = [...]string{"vrserver.exe", "vrmonitor.exe"}

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

// steamvrRunning reports whether SteamVR is already up (svProcNames in the Toolhelp process
// snapshot; stdlib syscall, no dep).
//
// Fails CLOSED after svSnapRetries: this is the sole gate in front of the launch-capable
// VR_Init(VRApplication_Overlay). It used to fail OPEN ("a transient error shouldn't stop the
// overlay connecting") - one ERROR_BAD_LENGTH snapshot failure on a dev box with SteamVR installed
// but NO headset was enough to launch SteamVR into a crash loop, silently (the supervise loop had
// already logged its idle line, so the retry logged nothing). Asymmetric costs: a missed detection
// delays connecting by one 5s tick, a false positive hijacks the user's machine. Fail closed.
func steamvrRunning() bool {
	for i := 0; i < svSnapRetries; i++ {
		if up, ok := svSnapshotHasSteamVR(); ok {
			return up
		}
		time.Sleep(svSnapRetryWait)
	}
	return false
}

// svSnapshotHasSteamVR walks one process snapshot. ok=false means the snapshot itself failed (the
// caller retries); it NEVER reports "running" on failure.
func svSnapshotHasSteamVR() (up, ok bool) {
	snap, _, _ := svCreateSnapshot.Call(svSnapProcess, 0)
	if snap == 0 || snap == ^uintptr(0) {
		return false, false
	}
	defer func() { _, _, _ = svCloseHandle.Call(snap) }()
	var pe svProcessEntry32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	if r, _, _ := svProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
		return false, false
	}
	for {
		name := syscall.UTF16ToString(pe.szExeFile[:])
		for _, want := range svProcNames {
			if name == want {
				return true, true
			}
		}
		if r, _, _ := svProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
			break
		}
	}
	return false, true
}
