//go:build windows

package sysexec

import (
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A Windows Job Object configured to kill every assigned process when the job's last handle
// closes - i.e. when this (parent) process exits for ANY reason: clean quit, panic, crash, or
// an external taskkill. Every spawned child (worker or feature host) is assigned to it, so
// subprocesses and their ffmpeg descendants can never be orphaned and left running after the
// app is gone. This is the OS-level backstop that complements cooperative shutdown.
var (
	jobOnce   sync.Once
	jobH      windows.Handle
	bgJobOnce sync.Once
	bgJobH    windows.Handle

	memJobOnce sync.Once
	memJobH    windows.Handle
)

// JOB_OBJECT_LIMIT_PROCESS_MEMORY caps each process in the job's committed memory. Defined
// locally so the build never depends on the constant's presence in x/sys/windows.
const jobLimitProcessMemory = 0x00000100

func ensureJob(background bool) {
	once := &jobOnce
	if background {
		once = &bgJobOnce
	}
	once.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(
			h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
		); err != nil {
			_ = windows.CloseHandle(h)
			return
		}
		if background {
			// 1000 = 10.00% CPU hard cap for the whole job object.
			cpu := jobCPUInfo{ControlFlags: jobCPUEnable | jobCPUHardCap, CpuRate: 1000}
			if _, err := windows.SetInformationJobObject(
				h, windows.JobObjectCpuRateControlInformation,
				uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)),
			); err != nil {
				_ = windows.CloseHandle(h)
				return
			}
			bgJobH = h
			return
		}
		jobH = h
	})
}

const (
	jobCPUEnable  = 0x1
	jobCPUHardCap = 0x4
)

type jobCPUInfo struct {
	ControlFlags uint32
	CpuRate      uint32
}

// ensureMemJob builds (once) a kill-on-close job with a per-process committed-memory cap. The
// cap is fixed by the first caller (only the media child uses it today); a runaway heap in an
// assigned child fails its next allocation → the child dies → its Host restarts it. This is the
// OS hard backstop under the child's own GOMEMLIMIT + medialink.memWatchdog.
func ensureMemJob(capBytes uintptr) {
	memJobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | jobLimitProcessMemory,
			},
			ProcessMemoryLimit: capBytes,
		}
		if _, err := windows.SetInformationJobObject(
			h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)),
		); err != nil {
			_ = windows.CloseHandle(h)
			return
		}
		memJobH = h
	})
}

// AssignToJobMem places a freshly-started child under a kill-on-close job with a per-process
// committed-memory cap of capMB (first call fixes the cap). Best-effort - on failure the child
// still runs, just uncapped. capMB<=0 falls back to the plain kill-on-close job.
func AssignToJobMem(p *os.Process, capMB int) {
	if p == nil {
		return
	}
	if capMB <= 0 {
		AssignToJob(p, false)
		return
	}
	ensureMemJob(uintptr(capMB) * 1024 * 1024)
	if memJobH == 0 {
		AssignToJob(p, false) // cap setup failed - at least keep kill-on-close
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	_ = windows.AssignProcessToJobObject(memJobH, h)
}

// AssignToJob places a freshly-started child process under the kill-on-close job. Best-effort:
// on failure the cooperative shutdown + taskkill path still applies. background=true uses the
// CPU-capped job for low-priority work.
func AssignToJob(p *os.Process, background bool) {
	if p == nil {
		return
	}
	ensureJob(background)
	hJob := jobH
	if background {
		hJob = bgJobH
	}
	if hJob == 0 {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	_ = windows.AssignProcessToJobObject(hJob, h)
}
