//go:build windows

package perfmon

import (
	"sort"
	"syscall"
	"time"
	"unsafe"

	"rave.page/mate/internal/sysactivity"
)

// System-wide + per-process CPU sampling for the "is it us or something else on this
// box" section of `ctl perf`. Two samples d apart, on demand at report time - no
// permanent per-PID state. Stdlib syscall only (kernel32/psapi), no new dep.

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	psapi              = syscall.NewLazyDLL("psapi.dll")
	procGetSystemTimes = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryEx = kernel32.NewProc("GlobalMemoryStatusEx")
	procOpenProcess    = kernel32.NewProc("OpenProcess")
	procCloseHandle    = kernel32.NewProc("CloseHandle")
	procGetProcTimes   = kernel32.NewProc("GetProcessTimes")
	procGetMemInfo     = psapi.NewProc("GetProcessMemoryInfo")
)

const processQueryLimitedInformation = 0x1000

type filetime struct{ low, high uint32 }

func (f filetime) ticks() uint64 { return uint64(f.high)<<32 | uint64(f.low) } // 100ns units

// memoryStatusEx mirrors MEMORYSTATUSEX.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

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

// systemTimes reads GetSystemTimes (idle/kernel/user cumulative ticks; kernel includes idle).
func systemTimes() (idle, kernel, user uint64, ok bool) {
	var i, k, u filetime
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	return i.ticks(), k.ticks(), u.ticks(), r != 0
}

// procTimes reads one process's cumulative CPU ticks via an open handle.
func procTimes(h uintptr) (uint64, bool) {
	var creation, exit, kernel, user filetime
	r, _, _ := procGetProcTimes.Call(h,
		uintptr(unsafe.Pointer(&creation)), uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0, false
	}
	return kernel.ticks() + user.ticks(), true
}

// sysProbe incrementally samples system CPU% + physical memory at the monitor's
// 1 Hz tick: CPU% = delta vs the previous GetSystemTimes read - no sleep, unlike
// sysSnapshot's on-demand two-point pass. First tick warms up (ok=false).
type sysProbe struct {
	prevIdle, prevKernel, prevUser uint64
	has                            bool
}

func (p *sysProbe) tick() (cpuPct, usedMB, totalMB float64, ok bool) {
	i, k, u, tok := systemTimes()
	if !tok {
		return 0, 0, 0, false
	}
	var mem memoryStatusEx
	mem.length = uint32(unsafe.Sizeof(mem))
	if r, _, _ := procGlobalMemoryEx.Call(uintptr(unsafe.Pointer(&mem))); r == 0 {
		return 0, 0, 0, false
	}
	usedMB = float64(mem.totalPhys-mem.availPhys) / (1024 * 1024)
	totalMB = float64(mem.totalPhys) / (1024 * 1024)
	if p.has {
		busy := (k - p.prevKernel) - (i - p.prevIdle) + (u - p.prevUser)
		if total := (k - p.prevKernel) + (u - p.prevUser); total > 0 {
			cpuPct = float64(busy) / float64(total) * 100
		}
		ok = true
	}
	p.prevIdle, p.prevKernel, p.prevUser, p.has = i, k, u, true
	return cpuPct, usedMB, totalMB, ok
}

// sysSnapshot samples system CPU + memory + per-process CPU/working-set over d.
func sysSnapshot(d time.Duration) SysStat {
	st := SysStat{}
	i0, k0, u0, ok := systemTimes()
	if !ok {
		st.Err = "GetSystemTimes failed"
		return st
	}

	// Open every visible process once; sample times before + after the same sleep.
	type tracked struct {
		pid   uint32
		name  string
		h     uintptr
		cpu0  uint64
		valid bool
	}
	var procs []tracked
	if list, lok := sysactivity.ListProcesses(); lok {
		for _, p := range list {
			if p.PID == 0 {
				continue // System Idle Process
			}
			h, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(p.PID))
			if h == 0 {
				continue // protected/system process - skip
			}
			t := tracked{pid: p.PID, name: p.Name, h: h}
			t.cpu0, t.valid = procTimes(h)
			procs = append(procs, t)
		}
	}
	wall0 := time.Now()
	time.Sleep(d)
	wallSec := time.Since(wall0).Seconds()

	i1, k1, u1, ok := systemTimes()
	if ok {
		busy := (k1 - k0) - (i1 - i0) + (u1 - u0)
		if total := (k1 - k0) + (u1 - u0); total > 0 {
			st.CPUPct = float64(busy) / float64(total) * 100
		}
	}
	for _, t := range procs {
		if t.valid && wallSec > 0 {
			if cpu1, tok := procTimes(t.h); tok && cpu1 >= t.cpu0 {
				pct := float64(cpu1-t.cpu0) * 1e-7 / wallSec * 100 // 100ns ticks → % of one core
				var pmc processMemoryCounters
				pmc.cb = uint32(unsafe.Sizeof(pmc))
				var ws float64
				if r, _, _ := procGetMemInfo.Call(t.h, uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb)); r != 0 {
					ws = float64(pmc.workingSetSize) / (1024 * 1024)
				}
				st.Procs = append(st.Procs, ProcStat{PID: int(t.pid), Name: t.name, CPUPct: pct, WSMB: ws})
			}
		}
		_, _, _ = procCloseHandle.Call(t.h)
	}
	sort.Slice(st.Procs, func(a, b int) bool { return st.Procs[a].CPUPct > st.Procs[b].CPUPct })

	var mem memoryStatusEx
	mem.length = uint32(unsafe.Sizeof(mem))
	if r, _, _ := procGlobalMemoryEx.Call(uintptr(unsafe.Pointer(&mem))); r != 0 {
		st.MemTotalMB = float64(mem.totalPhys) / (1024 * 1024)
		st.MemUsedMB = float64(mem.totalPhys-mem.availPhys) / (1024 * 1024)
	}
	st.OK = st.MemTotalMB > 0
	if !st.OK {
		st.Err = "GlobalMemoryStatusEx failed"
	}
	return st
}
