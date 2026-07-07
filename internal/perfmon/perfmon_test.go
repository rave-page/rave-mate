package perfmon

import (
	"strings"
	"testing"
	"time"
)

// mkSamples builds n 1s-spaced samples ending at end; cpu = index (so newest = n-1).
func mkSamples(n int, end time.Time) []Sample {
	out := make([]Sample, n)
	for i := range out {
		out[i] = Sample{
			T:      end.Add(-time.Duration(n-1-i) * time.Second),
			CPUPct: float64(i),
		}
	}
	return out
}

func TestSummarizeWindows(t *testing.T) {
	end := time.Now()
	ss := mkSamples(600, end) // 10 min of 1 Hz samples, cpu 0..599
	st := summarize(ss, func(s Sample) float64 { return s.CPUPct })
	if st.cur != 599 {
		t.Fatalf("cur=%v want 599", st.cur)
	}
	if st.maxW != 599 {
		t.Fatalf("maxW=%v want 599", st.maxW)
	}
	// 1-min window = samples with T >= end-60s → indices 539..599 (61 samples), avg 569.
	if st.avg1m != 569 {
		t.Fatalf("avg1m=%v want 569", st.avg1m)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	st := summarize(nil, func(s Sample) float64 { return s.CPUPct })
	if st != (seriesStat{}) {
		t.Fatalf("empty summarize=%+v want zero", st)
	}
}

func TestRingWraps(t *testing.T) {
	m := New()
	for i := 0; i < ringCap+50; i++ {
		m.add(Sample{CPUPct: float64(i)})
	}
	ss := m.Snapshot()
	if len(ss) != ringCap {
		t.Fatalf("len=%d want %d", len(ss), ringCap)
	}
	if ss[0].CPUPct != 50 || ss[len(ss)-1].CPUPct != float64(ringCap+49) {
		t.Fatalf("ring window [%v..%v], want [50..%d]", ss[0].CPUPct, ss[len(ss)-1].CPUPct, ringCap+49)
	}
}

// sample() reads real runtime/metrics - smoke: goroutines + heap are nonzero.
func TestSampleSmoke(t *testing.T) {
	m := New()
	s := m.sample()
	if s.Goroutines <= 0 {
		t.Fatalf("goroutines=%d", s.Goroutines)
	}
	if s.HeapMB <= 0 || s.RTTotalMB <= 0 {
		t.Fatalf("heap=%v rt=%v", s.HeapMB, s.RTTotalMB)
	}
}

func TestProbeRegistryReplaceAndPanic(t *testing.T) {
	RegisterProbe("t-ok", func() string { return "first" })
	RegisterProbe("t-ok", func() string { return "second" }) // replaces
	RegisterProbe("t-boom", func() string { panic("kaboom") })
	out := probeSections()
	if strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("replace failed:\n%s", out)
	}
	if !strings.Contains(out, "[t-ok]") || !strings.Contains(out, "probe panicked: kaboom") {
		t.Fatalf("sections/panic guard missing:\n%s", out)
	}
}

func TestReportSmoke(t *testing.T) {
	m := New()
	m.add(m.sample())
	m.SetChildren(func() []ChildProc {
		return []ChildProc{{Name: "obs", PID: 0, Ready: false, Restarts: 3, LastErr: "boom"}}
	})
	// Windows: shorten nothing - Report's 1s system pass is acceptable in tests.
	rep := m.Report()
	for _, want := range []string{"uptime", "cpu%", "goroutines", "gc ", "[system]", "[featurehost children]", "restarts=3"} {
		if !strings.Contains(rep, want) {
			t.Fatalf("report missing %q:\n%s", want, rep)
		}
	}
}

func TestCountList(t *testing.T) {
	if got := countList(nil); got != "-" {
		t.Fatalf("empty=%q", got)
	}
	got := countList(map[string]int{"b": 2, "a": 2, "c": 5})
	if got != "c 5, a 2, b 2" {
		t.Fatalf("got %q", got)
	}
}
