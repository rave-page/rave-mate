package webui

// `ctl perf` section [zigui]: the live cost of the phase-A render bridge - how many renders went
// to Zig, how long the native renderer took, and how much of that time is the state->JSON->parse
// round trip phase B removes. Registered from New() (webview renderer only) and additive: no
// existing perfmon section changes. Companion to logZigFallbacks, which logs the SILENT
// Zig->Go fallbacks; this shows the successful path's price.

import (
	"fmt"
	"sort"
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
	// --- phaseb-retain ---
	b.WriteString(zigPatchProbe())
	// --- end phaseb-retain ---
	return b.String()
}

// zigPatchProbe reports the retained-doc delta channel (B7 ii): per-surface seed/delta counts and
// EVERY way it declined, kept apart on purpose - a cap breach is the only decline that changes
// behaviour (three make a surface sticky-stateless), so lumping it into a generic "fallback" would
// hide the one number worth watching. Silent when the channel never ran.
func zigPatchProbe() string {
	pc := zigui.PatchCounts()
	if len(pc) == 0 {
		return ""
	}
	live, seeded, bytes := zigui.RetainStats()
	names := make([]string, 0, len(pc))
	for n := range pc {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "\nretained slots %d live · %d seeded · %s held", live, seeded, humanBytes(bytes))
	for _, n := range names {
		s := pc[n]
		fmt.Fprintf(&b, "\n  %s %d seeds + %d deltas · %s sent", n, s.Seeds, s.Deltas, humanBytes(s.DocBytes))
		if d := s.Desync + s.CapBreach + s.Malformed + s.Errors; d > 0 {
			fmt.Fprintf(&b, " · declines desync %d cap %d malformed %d err %d", s.Desync, s.CapBreach, s.Malformed, s.Errors)
		}
		if s.Sticky != 0 {
			b.WriteString(" · STICKY (stateless for this session)")
		}
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
