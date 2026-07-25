package webui

// `ctl perf` section [zigui]: the live cost of the phase-A render bridge - how many renders went
// to Zig, how long the native renderer took, and how much of that time is the state->JSON->parse
// round trip phase B removes. Registered from New() (webview renderer only) and additive: no
// existing perfmon section changes. Companion to logZigFallbacks, which logs the SILENT
// Zig->Go fallbacks; this shows the successful path's price.

import (
	"fmt"
	"strings"
	"time"

	"rave.page/mate/internal/zigui"
)

// zigPerfProbe renders the bridge counters (all cumulative since process start).
func zigPerfProbe() string {
	p := zigui.PerfCounts()
	var b strings.Builder
	if !zigui.Available() {
		b.WriteString("zig lib not linked (stub build) - Go renderers only\n")
	}
	fmt.Fprintf(&b, "renders %d · zig %s total · avg %s · state %s (avg %s)\n",
		p.Renders, zpDur(p.RenderNS), zpAvgDur(p.RenderNS, p.Renders),
		humanBytes(p.StateBytes), humanBytes(zpAvgU(p.StateBytes, p.Renders)))
	fmt.Fprintf(&b, "marshals %d · json %s total · avg %s · %s (avg %s) · %s of bridge time\n",
		p.Marshals, zpDur(p.MarshalNS), zpAvgDur(p.MarshalNS, p.Marshals),
		humanBytes(p.MarshalB), humanBytes(zpAvgU(p.MarshalB, p.Marshals)),
		zpPct(p.MarshalNS, p.MarshalNS+p.RenderNS))
	counts := zigui.FallbackCounts()
	if len(counts) == 0 {
		b.WriteString("fallbacks none")
	} else {
		fmt.Fprintf(&b, "fallbacks %s", zigFbKey(counts))
	}
	return b.String()
}

// zpDur formats a cumulative duration at µs resolution (ns noise is meaningless over 1000s of renders).
func zpDur(d time.Duration) string { return d.Round(time.Microsecond).String() }

func zpAvgDur(total time.Duration, n uint64) string {
	if n == 0 {
		return "-"
	}
	return zpDur(total / time.Duration(n))
}

func zpAvgU(total, n uint64) uint64 {
	if n == 0 {
		return 0
	}
	return total / n
}

// zpPct renders part/whole as a percentage ("-" until the first sample).
func zpPct(part, whole time.Duration) string {
	if whole <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(part)/float64(whole))
}
