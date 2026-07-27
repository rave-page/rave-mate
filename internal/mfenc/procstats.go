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
	EncBusyMs   float64 // mean child capture+encode time per frame (the zero-copy path's
	// saturation signal: the parent submits nothing, so submitAt/p99 stay empty here)
	// AdapterMoved: this session's sender lives on a different GPU than the encode device asked
	// for, and affinity resolution re-placed it there instead of downgrading to the readback path
	// (zigmedia inc 3, risk R7). Visible so a rig whose device policy fights its sender layout is
	// obvious rather than mysteriously slow.
	AdapterMoved bool
	CapFmt       uint32 // DXGI format actually consumed
	CapFlags     uint32 // bit0 zero-copy live, bit1 keyed mutex, bit2 named mutex, bit3 unsynchronized
	Downgrades   int    // zero-copy recycles/downgrades on this session
	// Vendor portability. Drive is the mode resolved from the MFT's own MF_TRANSFORM_ASYNC
	// attribute ("async"/"sync"); Software = the software MF encoder tier is serving this
	// session; DegradeReason is the one-sentence "why is this route not on its best path".
	// EMPTY DegradeReason is the only healthy value - a degraded route that reports nothing is
	// the failure shape this telemetry exists to kill.
	Drive         string
	Software      bool
	DegradeReason string
	EncFails      uint32 // attributed mid-route encode failures reported by the child
	// AsyncAttr is the RAW MF_TRANSFORM_ASYNC value Drive was derived from. DevShared/DevPolicy
	// say which device path is in effect. All three exist so a rig with no toolchain and no
	// remote-exec can prove from telemetry alone WHICH code path a passing run exercised.
	AsyncAttr bool
	DevShared bool
	DevPolicy string
	// Ledger view for this (adapter, encoder): streak length, and why it is poisoned if it is.
	LedgerFails  int
	Poisoned     bool
	PoisonReason string
	AdapterLUID  int64
	AdapterName  string
	// BusyDrops: frames the encoder had no credit for inside the child's per-frame budget. This
	// is what encoder SATURATION looks like now - previously the same condition blocked long
	// enough for the parent's deadline to end the route.
	BusyDrops uint32
}
