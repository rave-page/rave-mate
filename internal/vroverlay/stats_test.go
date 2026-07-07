package vroverlay

import (
	"math"
	"testing"

	"rave.page/mate/internal/netstats"
	"rave.page/mate/internal/perfmon"
)

func TestIsStatsType(t *testing.T) {
	for _, ty := range []string{typePerf, typeNetwork, typeTiming} {
		if !isStatsType(ty) {
			t.Fatalf("%q should be a stats type", ty)
		}
	}
	for _, ty := range []string{"chat", "alerts", "obs", ""} {
		if isStatsType(ty) {
			t.Fatalf("%q must NOT be a stats type", ty)
		}
	}
}

func TestStatsViewsRenderConstantDims(t *testing.T) {
	r, err := NewRenderer(1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	m := &Manager{rt: NewRuntime()}
	m.statsPerf = func() []perfmon.Sample {
		return []perfmon.Sample{
			{CPUPct: 12, RSSMB: 210, SysOK: true, SysCPUPct: 44, SysMemUsedMB: 8000, SysMemTotalMB: 32000},
			{CPUPct: 15, RSSMB: 215, SysOK: true, SysCPUPct: 51, SysMemUsedMB: 8200, SysMemTotalMB: 32000},
		}
	}
	m.statsNet = func() netstats.Snapshot {
		return netstats.Snapshot{
			PeerIn: []float64{100, 200, 300}, PeerOut: []float64{50, 60, 70},
			APIIn: []float64{10, 20, 30}, APIOut: []float64{5, 6, 7},
			Span: 3,
			RTT:  []netstats.RTTSeries{{Label: "studio", Ms: []float64{math.NaN(), 4.2, 5.1}, LatestMs: 5.1, Has: true}},
		}
	}

	for _, ty := range []string{typePerf, typeNetwork, typeTiming} {
		v := m.statsViewFor(ty)
		if v.title == "" || v.footer == "" {
			t.Fatalf("%s: title/footer must be set", ty)
		}
		if len(v.rows) == 0 {
			t.Fatalf("%s: expected readout rows", ty)
		}
		img := r.RenderStats(v, panelW, panelH, 0.82)
		if img.Bounds().Dx() != panelW || img.Bounds().Dy() != panelH {
			t.Fatalf("%s: dims must stay constant, got %v", ty, img.Bounds())
		}
		if s := m.statsViewFor(ty).sig(); s != v.sig() {
			t.Fatalf("%s: sig not stable across rebuilds", ty)
		}
	}

	// nil providers → placeholder, still renders at constant dims (no panic).
	m2 := &Manager{rt: NewRuntime()}
	for _, ty := range []string{typePerf, typeNetwork, typeTiming} {
		img := r.RenderStats(m2.statsViewFor(ty), panelW, panelH, 0.82)
		if img.Bounds().Dx() != panelW {
			t.Fatalf("%s (nil providers): bad dims", ty)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{0: "0 B", 512: "512 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 1048576: "1.0 MiB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Fatalf("humanBytes(%d)=%q want %q", in, got, want)
		}
	}
}

func TestStatsSigChangesOnData(t *testing.T) {
	m := &Manager{rt: NewRuntime()}
	base := 0.0
	m.statsNet = func() netstats.Snapshot {
		return netstats.Snapshot{PeerIn: []float64{base}, Span: 1}
	}
	a := m.networkView().sig()
	base = 9999
	b := m.networkView().sig()
	if a == b {
		t.Fatal("sig should change when the rate changes")
	}
}
