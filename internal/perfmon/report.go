package perfmon

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
)

// ── probe registry ────────────────────────────────────────────────────────────
// Subsystems register named report sections (func() string) so their counters appear
// in `ctl perf` without perfmon importing them. Re-registering a name replaces it.

type probe struct {
	name string
	fn   func() string
}

var (
	probeMu sync.Mutex
	probes  []probe
)

// RegisterProbe adds (or replaces) a named report section.
func RegisterProbe(name string, fn func() string) {
	probeMu.Lock()
	defer probeMu.Unlock()
	for i := range probes {
		if probes[i].name == name {
			probes[i].fn = fn
			return
		}
	}
	probes = append(probes, probe{name, fn})
}

// probeSections renders every registered probe, panic-guarded (a broken probe reports
// its panic instead of killing the ctl handler).
func probeSections() string {
	probeMu.Lock()
	ps := append([]probe(nil), probes...)
	probeMu.Unlock()
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "\n[%s]\n%s\n", p.name, strings.TrimRight(runProbe(p.fn), "\n"))
	}
	return b.String()
}

func runProbe(fn func() string) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = fmt.Sprintf("probe panicked: %v", r)
		}
	}()
	return fn()
}

// ── ring summary ──────────────────────────────────────────────────────────────

// seriesStat is current / 1-min avg / window max for one metric.
type seriesStat struct{ cur, avg1m, maxW float64 }

// summarize reduces the ring: per-metric now/1m-avg/window-max + GC window deltas.
// Times are taken from the samples (newest sample anchors the 1-min window).
func summarize(ss []Sample, metric func(Sample) float64) seriesStat {
	if len(ss) == 0 {
		return seriesStat{}
	}
	newest := ss[len(ss)-1]
	st := seriesStat{cur: metric(newest)}
	cutoff := newest.T.Add(-time.Minute)
	sum, n := 0.0, 0
	for _, s := range ss {
		v := metric(s)
		if v > st.maxW {
			st.maxW = v
		}
		if !s.T.Before(cutoff) {
			sum += v
			n++
		}
	}
	if n > 0 {
		st.avg1m = sum / float64(n)
	}
	return st
}

// Report renders the full perf-diagnosis text: uptime + ring summary + GC + system +
// supervised children + every registered probe section. Blocks ~1s for the system /
// per-process CPU sampling pass.
func (m *Monitor) Report() string {
	ss := m.Snapshot()
	m.mu.Lock()
	children := m.children
	m.mu.Unlock()

	var b strings.Builder
	window := time.Duration(0)
	if len(ss) > 1 {
		window = ss[len(ss)-1].T.Sub(ss[0].T).Round(time.Second)
	}
	fmt.Fprintf(&b, "uptime %s | %d samples (window %s)\n",
		time.Since(m.startAt).Round(time.Second), len(ss), window)
	if len(ss) == 0 {
		b.WriteString("(no samples yet - collector just started)\n")
	} else {
		rows := []struct {
			label  string
			metric func(Sample) float64
			fmtStr string
		}{
			{"cpu%", func(s Sample) float64 { return s.CPUPct }, "%.1f"},
			{"rss MB", func(s Sample) float64 { return s.RSSMB }, "%.1f"},
			{"goroutines", func(s Sample) float64 { return float64(s.Goroutines) }, "%.0f"},
			{"heap MB", func(s Sample) float64 { return s.HeapMB }, "%.1f"},
		}
		fmt.Fprintf(&b, "%-12s %10s %10s %10s\n", "", "now", "1m-avg", "10m-max")
		for _, r := range rows {
			st := summarize(ss, r.metric)
			fmt.Fprintf(&b, "%-12s %10s %10s %10s\n", r.label,
				fmt.Sprintf(r.fmtStr, st.cur), fmt.Sprintf(r.fmtStr, st.avg1m), fmt.Sprintf(r.fmtStr, st.maxW))
		}
		newest, oldest := ss[len(ss)-1], ss[0]
		fmt.Fprintf(&b, "runtime mem %.1f MB | heap objects %d\n", newest.RTTotalMB, newest.HeapObjects)
		cycles := newest.GCCycles - oldest.GCCycles
		avgPause, maxPause := 0.0, 0.0
		if cycles > 0 {
			avgPause = (newest.GCPauseMs - oldest.GCPauseMs) / float64(cycles)
		}
		for _, s := range ss {
			if s.GCMaxPauseMs > maxPause {
				maxPause = s.GCMaxPauseMs
			}
		}
		fmt.Fprintf(&b, "gc %d cycles (window, %d total) | avg pause %.2fms | max pause ≤%.2fms\n",
			cycles, newest.GCCycles, avgPause, maxPause)
	}

	// System + per-process CPU: one shared ~1s sampling pass; children reuse its pid stats.
	sys := sysSnapshot(time.Second)
	b.WriteString("\n[system]\n")
	if !sys.OK {
		b.WriteString(sys.Err + "\n")
	} else {
		fmt.Fprintf(&b, "cpu %.1f%% (all cores) | mem %.1f/%.1f GB used (%.0f%%)\n",
			sys.CPUPct, sys.MemUsedMB/1024, sys.MemTotalMB/1024, sys.MemUsedMB/sys.MemTotalMB*100)
		fmt.Fprintf(&b, "top processes by cpu (%% of one core, over 1s):\n")
		for i, p := range sys.Procs {
			if i >= 8 {
				break
			}
			fmt.Fprintf(&b, "  %-28s pid %-7d cpu %6.1f%%  ws %8.1f MB\n", p.Name, p.PID, p.CPUPct, p.WSMB)
		}
	}

	if children != nil {
		b.WriteString("\n[featurehost children]\n")
		kids := children()
		if len(kids) == 0 {
			b.WriteString("(none)\n")
		}
		byPID := map[int]ProcStat{}
		for _, p := range sys.Procs {
			byPID[p.PID] = p
		}
		for _, c := range kids {
			line := fmt.Sprintf("  %-10s pid %-7d ready=%-5v restarts=%d", c.Name, c.PID, c.Ready, c.Restarts)
			if p, ok := byPID[c.PID]; ok && c.PID > 0 {
				line += fmt.Sprintf("  cpu %5.1f%%  ws %7.1f MB", p.CPUPct, p.WSMB)
			}
			if c.LastErr != "" {
				line += "  lastErr: " + c.LastErr
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString(probeSections())
	return b.String()
}

// LogCounts is a ready-made probe body: WARN/ERROR entry counts from a logbus over the
// trailing window, grouped by source (register via RegisterProbe in app wiring).
func LogCounts(bus *logbus.Bus, window time.Duration) string {
	if bus == nil {
		return "(no log bus)"
	}
	cutoff := time.Now().Add(-window)
	warns, errs := map[string]int{}, map[string]int{}
	var nw, ne int
	for _, e := range bus.Snapshot() {
		if e.Time.Before(cutoff) {
			continue
		}
		switch e.Level {
		case logbus.Warn:
			warns[e.Source]++
			nw++
		case logbus.Error:
			errs[e.Source]++
			ne++
		}
	}
	return fmt.Sprintf("last %s: WARN %d (%s) | ERROR %d (%s)",
		window, nw, countList(warns), ne, countList(errs))
}

// countList renders "src 3, other 1" sorted by count desc then name; "-" when empty.
func countList(m map[string]int) string {
	if len(m) == 0 {
		return "-"
	}
	type kv struct {
		k string
		v int
	}
	kvs := make([]kv, 0, len(m))
	for k, v := range m {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool {
		if kvs[i].v != kvs[j].v {
			return kvs[i].v > kvs[j].v
		}
		return kvs[i].k < kvs[j].k
	})
	parts := make([]string, len(kvs))
	for i, e := range kvs {
		parts[i] = fmt.Sprintf("%s %d", e.k, e.v)
	}
	return strings.Join(parts, ", ")
}
