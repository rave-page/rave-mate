// Package gpumem samples GPU dedicated-video-memory (VRAM) usage per adapter and per
// process, and drives a low-VRAM watchdog. Goal: attribute a live-set VRAM leak from a
// growth curve in the debug log, and warn the user BEFORE the driver starts refusing
// OpenGL/DirectX interop registrations with E_OUTOFVIDEOMEMORY (0x8876017C) - the failure
// that kills Spout senders/receivers in Resolume/OBS/VRChat mid-set while rave-mate itself
// logs nothing (the box, not rave-mate, hit the wall).
//
// Windows reads the kernel graphics manager directly via gdi32 D3DKMT (QueryStatistics over
// ADAPTER/SEGMENT/PROCESS_SEGMENT) - stdlib syscall, no cgo, no COM/DXGI (keeps vendor
// user-mode drivers out of the daemon), no new dep. This is the mechanism SystemInformer
// uses; Windows PDH "GPU Process Memory" counters are deliberately NOT used (observed
// returning corrupt TB-scale values on the target box). Other platforms return no adapters
// (fail open / no-op) so the watchdog is simply inert there.
//
// Runs in-proc in the daemon (not a featurehost child): pure read-only kernel syscalls, no
// vendor-driver entry, low throughput - same class as internal/sysactivity + gpuwatch, which
// is the DB-bound/low-throughput in-proc carve-out the repo rule allows.
package gpumem

// AdapterUsage is one real GPU adapter's dedicated-VRAM totals (aperture/shared-system
// segments excluded, so UsedMB matches nvidia-smi's "used").
type AdapterUsage struct {
	Name     string // driver registry name ("NVIDIA GeForce RTX 3060") or a pci fallback
	LUID     string // "high:low" - stable within a boot, used as the watchdog state key
	BudgetMB uint64 // sum of dedicated (non-aperture) segment CommitLimit
	UsedMB   uint64 // sum of dedicated segment BytesResident
	FreeMB   uint64 // BudgetMB - UsedMB (0 if used > budget)
}

// ProcUsage is one process's dedicated-VRAM commitment on a given adapter.
type ProcUsage struct {
	Name string
	PID  uint32
	MB   uint64
}

// AdapterProcs is the top-N VRAM consumers on one adapter, descending by MB.
type AdapterProcs struct {
	Adapter string
	Procs   []ProcUsage
}

// Sampler reads live GPU memory. Implementations are platform-specific + stateless.
type Sampler interface {
	// Sample returns dedicated-VRAM totals for every real adapter. Cheap (no process sweep).
	Sample() ([]AdapterUsage, error)
	// SampleProcesses returns the top-topN VRAM consumers per real adapter. Expensive: a full
	// process sweep (Toolhelp + OpenProcess + per-segment query). Bounded - see the windows impl.
	SampleProcesses(topN int) ([]AdapterProcs, error)
}

// NewSampler returns the platform Sampler (a no-op returning no adapters off Windows).
func NewSampler() Sampler { return newSampler() }
