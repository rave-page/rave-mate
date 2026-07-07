package procstat

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	psapi              = syscall.NewLazyDLL("psapi.dll")
	procGetProcTimes   = kernel32.NewProc("GetProcessTimes")
	procGetCurrentProc = kernel32.NewProc("GetCurrentProcess")
	procGetMemInfo     = psapi.NewProc("GetProcessMemoryInfo")
)

type filetime struct{ low, high uint32 }

func (f filetime) ticks() uint64 { return uint64(f.high)<<32 | uint64(f.low) } // 100ns units

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS (amd64 layout).
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// osSample returns (process CPU seconds, wall seconds, RSS bytes, ok).
func osSample() (float64, float64, uint64, bool) {
	h, _, _ := procGetCurrentProc.Call()
	var creation, exit, kernel, user filetime
	r, _, _ := procGetProcTimes.Call(h,
		uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0, 0, 0, false
	}
	cpuSec := float64(kernel.ticks()+user.ticks()) * 1e-7 // 100ns ticks → seconds
	wallSec := float64(time.Now().UnixNano()) * 1e-9

	var pmc processMemoryCounters
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	var rss uint64
	if r, _, _ := procGetMemInfo.Call(h, uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb)); r != 0 {
		rss = uint64(pmc.workingSetSize)
	}
	return cpuSec, wallSec, rss, true
}
