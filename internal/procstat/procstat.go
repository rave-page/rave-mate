// Package procstat samples this process's own CPU / memory / goroutine footprint so it can be carried
// in telemetry (e.g. VR perf). A Sampler keeps the last CPU-time reading to compute a delta-based
// percent; cross-platform heap/goroutine come from the runtime, RSS is OS-specific.
package procstat

import "runtime"

// Stats is one self-footprint snapshot.
type Stats struct {
	CPUPercent float64 `json:"cpuPct"`     // process CPU% over the interval (100 = one full core)
	RSSMB      float64 `json:"rssMB"`      // resident set / working set (MiB)
	HeapMB     float64 `json:"heapMB"`     // Go heap in use (MiB)
	Goroutines int     `json:"goroutines"` // live goroutines
	NumGC      uint32  `json:"numGC"`      // completed GC cycles
}

// Sampler computes delta-based CPU% between Sample calls. Zero value is ready.
type Sampler struct {
	lastCPU  float64 // process CPU seconds at last sample
	lastWall float64 // wall seconds at last sample
}

// CPURSS returns delta-based CPU% + RSS only - no runtime.ReadMemStats (its brief
// stop-the-world). For callers that source heap stats from runtime/metrics (perfmon).
func (s *Sampler) CPURSS() (cpuPct, rssMB float64, ok bool) {
	cpuSec, wallSec, rss, ok := osSample()
	if !ok {
		return 0, 0, false
	}
	if s.lastWall > 0 {
		if dw := wallSec - s.lastWall; dw > 0 {
			cpuPct = (cpuSec - s.lastCPU) / dw * 100
		}
	}
	s.lastCPU, s.lastWall = cpuSec, wallSec
	return cpuPct, float64(rss) / (1024 * 1024), true
}

// Sample returns the current footprint; CPU% is averaged over the interval since the prior Sample.
func (s *Sampler) Sample() Stats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st := Stats{
		HeapMB:     float64(ms.HeapAlloc) / (1024 * 1024),
		Goroutines: runtime.NumGoroutine(),
		NumGC:      ms.NumGC,
	}
	cpuSec, wallSec, rss, ok := osSample()
	if !ok {
		return st
	}
	st.RSSMB = float64(rss) / (1024 * 1024)
	if s.lastWall > 0 {
		if dw := wallSec - s.lastWall; dw > 0 {
			st.CPUPercent = (cpuSec - s.lastCPU) / dw * 100
		}
	}
	s.lastCPU, s.lastWall = cpuSec, wallSec
	return st
}
