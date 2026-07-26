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
//
// There is one job per JobClass: same kill-on-close guarantee, different CPU discipline (see
// JobClass). Handles are created lazily, once per class.
var (
	jobs [jobClassCount]struct {
		once sync.Once
		h    windows.Handle
	}

	memJobOnce sync.Once
	memJobH    windows.Handle
)

// JOB_OBJECT_LIMIT_PROCESS_MEMORY caps each process in the job's committed memory. Defined
// locally so the build never depends on the constant's presence in x/sys/windows.
const jobLimitProcessMemory = 0x00000100

// JobClass is a child's CPU-scheduling contract - NOT its importance. Every class kills its
// children when the app dies; they differ only in the job's aggregate CPU cap.
type JobClass int

const (
	// JobRealtime: kill-on-close only, NO CPU cap. For children that must keep up with a live
	// frame rate - a throttled realtime encoder does not save work, it makes the SENDER pay full
	// capture+readback cost for frames the encoder never drains (the spout-share melt). Precedent:
	// mediaplayer/player.go deliberately spawns realtime children uncapped.
	JobRealtime JobClass = iota
	// JobBatch: 10% aggregate hard CPU cap across the whole class. Deferrable sweeps only (codec
	// probe test-encodes, gridfix analyze/train, the background worker pool) - work that may be
	// starved indefinitely without breaking a live stream.
	JobBatch
	// JobMedia: 70% aggregate hard CPU cap. Live media capture/stream children (webcam, VR stream,
	// mocap ingest): they must not sit in the 10% batch bucket - sharing it with a gridfix sweep
	// throttles a live capture to a few percent of a core and it drops frames - but they stay
	// bounded so a runaway ffmpeg can never take the whole machine.
	JobMedia

	jobClassCount = 3
)

// cpuRateFor is the job's aggregate hard CPU cap in hundredths of a percent (0 = uncapped).
func cpuRateFor(c JobClass) uint32 {
	switch c {
	case JobBatch:
		return 1000 // 10.00%
	case JobMedia:
		return 7000 // 70.00%
	default:
		return 0 // realtime: no cap
	}
}

func ensureJob(class JobClass) {
	if class < 0 || int(class) >= jobClassCount {
		class = JobRealtime
	}
	j := &jobs[class]
	j.once.Do(func() {
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
		if rate := cpuRateFor(class); rate > 0 {
			cpu := jobCPUInfo{ControlFlags: jobCPUEnable | jobCPUHardCap, CpuRate: rate}
			if _, err := windows.SetInformationJobObject(
				h, windows.JobObjectCpuRateControlInformation,
				uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)),
			); err != nil {
				_ = windows.CloseHandle(h)
				return
			}
		}
		j.h = h
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

// ensureMemJob builds (once) a kill-on-close job with a per-process committed-memory cap AND
// the given class's aggregate CPU rate cap (a process joins exactly one job, so the mem job
// must carry its own CPU discipline - the media child previously got the mem cap but NO CPU
// class). Cap + class are fixed by the first caller (only the media child uses it today); a
// runaway heap in an assigned child fails its next allocation → the child dies → its Host
// restarts it. OS hard backstop under the child's own GOMEMLIMIT + medialink.memWatchdog.
func ensureMemJob(capBytes uintptr, class JobClass) {
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
		if rate := cpuRateFor(class); rate > 0 {
			cpu := jobCPUInfo{ControlFlags: jobCPUEnable | jobCPUHardCap, CpuRate: rate}
			if _, err := windows.SetInformationJobObject(
				h, windows.JobObjectCpuRateControlInformation,
				uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)),
			); err != nil {
				_ = windows.CloseHandle(h)
				return
			}
		}
		memJobH = h
	})
}

// AssignToJobMemClass places a freshly-started child under a kill-on-close job carrying a
// per-process committed-memory cap of capMB AND class's CPU rate cap (first call fixes both).
// Best-effort - on failure the child still runs, just uncapped. capMB<=0 falls back to the
// plain per-class job.
func AssignToJobMemClass(p *os.Process, capMB int, class JobClass) {
	if p == nil {
		return
	}
	if capMB <= 0 {
		AssignToJobClass(p, class)
		return
	}
	ensureMemJob(uintptr(capMB)*1024*1024, class)
	if memJobH == 0 {
		AssignToJobClass(p, class) // mem-cap setup failed - at least keep kill-on-close + CPU class
		return
	}
	assign(memJobH, p)
}

// AssignToJobMem is AssignToJobMemClass with the uncapped realtime class.
func AssignToJobMem(p *os.Process, capMB int) { AssignToJobMemClass(p, capMB, JobRealtime) }

// AssignToJob places a freshly-started child process under the kill-on-close job. Best-effort:
// on failure the cooperative shutdown + taskkill path still applies. background=true selects the
// CPU-capped batch job (JobBatch); false selects the uncapped realtime job.
func AssignToJob(p *os.Process, background bool) {
	class := JobRealtime
	if background {
		class = JobBatch
	}
	AssignToJobClass(p, class)
}

// AssignToJobClass places a freshly-started child under the kill-on-close job for its class (see
// JobClass for the CPU discipline each one carries). Best-effort - on failure the child still runs,
// just outside the job.
func AssignToJobClass(p *os.Process, class JobClass) {
	if p == nil {
		return
	}
	ensureJob(class)
	if class < 0 || int(class) >= jobClassCount {
		class = JobRealtime
	}
	if h := jobs[class].h; h != 0 {
		assign(h, p)
	}
}

// assign opens the child with quota/terminate rights and puts it in hJob.
func assign(hJob windows.Handle, p *os.Process) {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	_ = windows.AssignProcessToJobObject(hJob, h)
}
