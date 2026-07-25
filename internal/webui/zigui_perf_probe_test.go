package webui

import (
	"strings"
	"testing"
)

// The probe must render on a cold process (zero counters, no division by zero) and in both builds
// - `ctl perf` runs it on the ctl goroutine, and perfmon only panic-guards, it does not skip.
func TestZigPerfProbeRendersCold(t *testing.T) {
	out := zigPerfProbe()
	for _, want := range []string{"renders ", "marshals ", "fallbacks "} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in probe output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "NaN") || strings.Contains(out, "+Inf") {
		t.Errorf("zero-sample division leaked:\n%s", out)
	}
}

func TestZigPerfProbeAvgPlaceholders(t *testing.T) {
	if got := zpAvgDur(0, 0); got != "-" {
		t.Errorf("zpAvgDur(0,0) = %q, want %q", got, "-")
	}
	if got := zpPct(0, 0); got != "-" {
		t.Errorf("zpPct(0,0) = %q, want %q", got, "-")
	}
	if got := zpAvgU(10, 0); got != 0 {
		t.Errorf("zpAvgU(10,0) = %d, want 0", got)
	}
}
