//go:build windows

package sysactivity

import (
	"syscall"
	"time"
	"unsafe"
)

type winActivity struct{}

func newActivity() Activity { return winActivity{} }

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
	procCreateSnapshot   = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW  = kernel32.NewProc("Process32FirstW")
	procProcess32NextW   = kernel32.NewProc("Process32NextW")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

// IdleDuration uses GetLastInputInfo (ms tick of last input) vs GetTickCount.
func (winActivity) IdleDuration() (time.Duration, bool) {
	lii := lastInputInfo{cbSize: uint32(unsafe.Sizeof(lastInputInfo{}))}
	r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if r == 0 {
		return 0, false
	}
	tick, _, _ := procGetTickCount.Call()
	now := uint32(tick)
	if now < lii.dwTime {
		return 0, true // GetTickCount wrapped (~49.7 days uptime); treat as just-active
	}
	return time.Duration(now-lii.dwTime) * time.Millisecond, true
}

const (
	th32csSnapProcess = 0x00000002
	maxPath           = 260
)

type processEntry32 struct {
	dwSize              uint32
	cntUsage            uint32
	th32ProcessID       uint32
	th32DefaultHeapID   uintptr
	th32ModuleID        uint32
	cntThreads          uint32
	th32ParentProcessID uint32
	pcPriClassBase      int32
	dwFlags             uint32
	szExeFile           [maxPath]uint16
}

// RunningProcesses walks the Toolhelp process snapshot.
func (winActivity) RunningProcesses() (map[string]bool, bool) {
	out := map[string]bool{}
	ok := walkProcesses(func(_ uint32, name string) { addProcessNames(out, name) })
	if !ok {
		return nil, false
	}
	return out, true
}

// listProcesses walks the same snapshot keeping pids (perf diagnosis).
func listProcesses() ([]ProcessInfo, bool) {
	var out []ProcessInfo
	ok := walkProcesses(func(pid uint32, name string) { out = append(out, ProcessInfo{PID: pid, Name: name}) })
	return out, ok
}

// walkProcesses visits every Toolhelp snapshot row; false if the snapshot failed.
func walkProcesses(visit func(pid uint32, name string)) bool {
	snap, _, _ := procCreateSnapshot.Call(th32csSnapProcess, 0)
	if snap == 0 || snap == ^uintptr(0) { // INVALID_HANDLE_VALUE
		return false
	}
	defer func() { _, _, _ = procCloseHandle.Call(snap) }()

	var pe processEntry32
	pe.dwSize = uint32(unsafe.Sizeof(pe))
	if r, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
		return false
	}
	for {
		visit(pe.th32ProcessID, syscall.UTF16ToString(pe.szExeFile[:]))
		if r, _, _ := procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&pe))); r == 0 {
			break
		}
	}
	return true
}
