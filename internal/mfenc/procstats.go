package mfenc

// ProcStats is one encode session's live telemetry (per SESSION, not per child - the
// Phase-2 load governor consumes rising p99 as an early saturation signal).
type ProcStats struct {
	Name        string  // encoder MFT friendly name
	LatP50Ms    float64 // submit→AU latency percentile (sliding window)
	LatP99Ms    float64
	QueueDepth  int     // frames submitted minus AUs received (encoder pipeline depth)
	ChildCPUPct float64 // encoder child process CPU (all sessions)
	DroppedAUs  uint64  // ring-full drops (child side)
	Restarts    int     // encoder child restarts observed by this session
	// Zero-copy capture (src:"spout"): read straight out of the SHM header, so the frame path
	// stays JSON-free. Zero on a readback session.
	ZeroCopy    bool
	CapFrames   uint64  // shared textures captured
	CapFPS      float64 // derived from CapFrames
	CapSkips    uint64  // pacing ticks skipped (previous encode still running)
	MtxTimeouts uint64  // shared-texture mutex acquire timeouts (sender contention)
	SrcErrors   uint64  // capture hard failures
	CapStaleMs  float64 // age of the last successful capture (frozen-source oracle)
	CapFmt      uint32  // DXGI format actually consumed
	CapFlags    uint32  // bit0 zero-copy live, bit1 keyed mutex, bit2 named mutex, bit3 unsynchronized
	Downgrades  int     // zero-copy recycles/downgrades on this session
}
