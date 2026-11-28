//go:build windows

package videoshare

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	k32frame        = windows.NewLazySystemDLL("kernel32.dll")
	ntdllFrame      = windows.NewLazySystemDLL("ntdll.dll")
	procOpenSem     = k32frame.NewProc("OpenSemaphoreW")
	procNtQuerySema = ntdllFrame.NewProc("NtQuerySemaphore")
)

const semQueryState = 0x0001 // SEMAPHORE_QUERY_STATE

// senderFrameCount reads the count read-only (NtQuerySemaphore): a Wait+Release probe would dip
// the count under the sender's own Wait(0).
func senderFrameCount(name string) (int64, bool) {
	if name == "" || len(name) > 256 {
		return 0, false
	}
	p, err := windows.UTF16PtrFromString(name + "_Count_Semaphore")
	if err != nil {
		return 0, false
	}
	h, _, _ := procOpenSem.Call(semQueryState|windows.SYNCHRONIZE, 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return 0, false
	}
	defer func() { _ = windows.CloseHandle(windows.Handle(h)) }()
	var info struct{ cur, max int32 }
	if st, _, _ := procNtQuerySema.Call(h, 0, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), 0); uint32(st) != 0 { // NTSTATUS is 32-bit
		return 0, false
	}
	return int64(info.cur), true
}
