package zigui

import (
	"testing"
	"time"
)

// Counters are cumulative + process-global, so assert on DELTAS (other tests in this package
// render too, and the tagged build's render funnel bumps them).
func TestPerfCountsAccumulate(t *testing.T) {
	before := PerfCounts()
	NoteRender(1234, 2*time.Millisecond)
	NoteMarshal(567, time.Millisecond)
	NoteMarshal(0, 3*time.Millisecond) // failed marshal: timed, zero bytes
	got := PerfCounts()

	if d := got.Renders - before.Renders; d != 1 {
		t.Fatalf("renders delta = %d, want 1", d)
	}
	if d := got.StateBytes - before.StateBytes; d != 1234 {
		t.Fatalf("stateBytes delta = %d, want 1234", d)
	}
	if d := got.RenderNS - before.RenderNS; d != 2*time.Millisecond {
		t.Fatalf("renderNS delta = %s, want 2ms", d)
	}
	if d := got.Marshals - before.Marshals; d != 2 {
		t.Fatalf("marshals delta = %d, want 2", d)
	}
	if d := got.MarshalB - before.MarshalB; d != 567 {
		t.Fatalf("marshalB delta = %d, want 567 (a failed marshal adds no bytes)", d)
	}
	if d := got.MarshalNS - before.MarshalNS; d != 4*time.Millisecond {
		t.Fatalf("marshalNS delta = %s, want 4ms", d)
	}
}

// A negative duration (clock stepped backwards) must not wrap the unsigned counter.
func TestPerfNegativeDurationClamped(t *testing.T) {
	before := PerfCounts()
	NoteRender(0, -time.Second)
	NoteMarshal(0, -time.Second)
	got := PerfCounts()
	if got.RenderNS != before.RenderNS || got.MarshalNS != before.MarshalNS {
		t.Fatalf("negative duration leaked: render %s→%s, marshal %s→%s",
			before.RenderNS, got.RenderNS, before.MarshalNS, got.MarshalNS)
	}
}
