// Render-path cost accounting - present in BOTH builds (tagged cgo + untagged stub), same as
// fallback.go, so webui reads it without build-tag branches. Motivation: phase A added a per-render
// state->JSON->parse round trip that phase B intends to remove, and every claim about that cost is
// unfalsifiable without numbers. The benchmarks in internal/webui measure fixtures; these counters
// measure the LIVE app (`ctl perf`, section [zigui]).
package zigui

import (
	"sync/atomic"
	"time"
)

// Perf is a cumulative snapshot of the Zig render path. Counters are monotonic since process
// start; rates/averages come from one snapshot or the delta between two.
type Perf struct {
	Renders    uint64        // Zig renders that returned ok=true
	RenderNS   time.Duration // wall time in the Zig renderer (cgo call + result copy + free)
	StateBytes uint64        // state JSON bytes handed to Zig on those renders
	Marshals   uint64        // state marshals by the webui bridge (Zig-path renders attempted)
	MarshalNS  time.Duration // wall time in encoding/json for those marshals
	MarshalB   uint64        // bytes produced by those marshals (a failed marshal counts 0)
}

// Cumulative counters. Atomics only - the render path runs on the serialized webui act worker
// but ctl/perfmon reads from its own goroutine, and a mutex here would sit on the hot path.
var perf struct {
	renders, renderNS, stateBytes atomic.Uint64
	marshals, marshalNS, marshalB atomic.Uint64
}

// NoteRender records one successful Zig render: bytes = the state JSON handed over, d = wall time
// in the native renderer. Called by the cgo render funnel (and by any future non-JSON wire path);
// exported so the untagged stub build keeps one definition of the counter set.
func NoteRender(bytes int, d time.Duration) {
	perf.renders.Add(1)
	perf.renderNS.Add(uint64(max(d, 0)))
	if bytes > 0 {
		perf.stateBytes.Add(uint64(bytes))
	}
}

// NoteMarshal records one state marshal (webui stateJSON). bytes = 0 when the marshal failed.
func NoteMarshal(bytes int, d time.Duration) {
	perf.marshals.Add(1)
	perf.marshalNS.Add(uint64(max(d, 0)))
	if bytes > 0 {
		perf.marshalB.Add(uint64(bytes))
	}
}

// PerfCounts snapshots the render-path counters (mirrors FallbackCounts: cheap, copy-only, safe
// from any goroutine). All zero in an untagged/stub build - nothing ever reaches Zig.
func PerfCounts() Perf {
	return Perf{
		Renders:    perf.renders.Load(),
		RenderNS:   time.Duration(perf.renderNS.Load()),
		StateBytes: perf.stateBytes.Load(),
		Marshals:   perf.marshals.Load(),
		MarshalNS:  time.Duration(perf.marshalNS.Load()),
		MarshalB:   perf.marshalB.Load(),
	}
}
