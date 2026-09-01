package gpumem

import (
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

type fakeSampler struct {
	adapters    []AdapterUsage
	procs       []AdapterProcs
	sampleCalls int
	procCalls   int
	lastTopN    int
}

func (f *fakeSampler) Sample() ([]AdapterUsage, error) { f.sampleCalls++; return f.adapters, nil }

func (f *fakeSampler) SampleProcesses(topN int) ([]AdapterProcs, error) {
	f.procCalls++
	f.lastTopN = topN
	return f.procs, nil
}

func TestThreshold(t *testing.T) {
	cases := []struct {
		warnFreeMB, budgetMB, want uint64
	}{
		{500, 12000, 500},   // explicit override wins
		{0, 12000, 1024},    // 8% = 960 < floor -> floor
		{0, 24000, 1920},    // 8% = 1920 > floor
		{0, 2000, 1024},     // 8% = 160 < floor
		{0, 0, 1024},        // no budget -> floor
		{2048, 24000, 2048}, // explicit above auto
	}
	for _, c := range cases {
		if got := threshold(c.warnFreeMB, c.budgetMB); got != c.want {
			t.Errorf("threshold(%d,%d)=%d want %d", c.warnFreeMB, c.budgetMB, got, c.want)
		}
	}
}

func TestDecideHysteresis(t *testing.T) {
	const th, rearm = 1024, 256
	const gap = 10 * time.Minute
	base := time.Unix(1_700_000_000, 0)
	steps := []struct {
		dtMin int
		free  uint64
		want  bool
		note  string
	}{
		{0, 2000, false, "healthy"},
		{1, 1000, true, "first crossing fires"},
		{2, 900, false, "still low: disarmed"},
		{3, 1000, false, "not recovered past th+rearm"},
		{4, 1279, false, "just under re-arm band"},
		{5, 1300, false, "recovers past th+rearm -> re-arm (no fire)"},
		{7, 1000, false, "drop again but <10min since last warn -> suppressed"},
		{12, 1000, true, "10min gap elapsed -> fires again"},
		{13, 1000, false, "disarmed after 2nd fire"},
	}
	var w watchState
	for _, s := range steps {
		now := base.Add(time.Duration(s.dtMin) * time.Minute)
		if got := w.decide(now, s.free, th, rearm, gap); got != s.want {
			t.Errorf("%s: decide(free=%d)=%v want %v", s.note, s.free, got, s.want)
		}
	}
}

// TestDecideRearmRequiredBeforeRefire proves a sustained low state warns exactly once even
// past the gap: without recovery above th+rearm, it never re-fires.
func TestDecideRearmRequiredBeforeRefire(t *testing.T) {
	const th, rearm = 1024, 256
	const gap = 10 * time.Minute
	base := time.Unix(1_700_000_000, 0)
	var w watchState
	if !w.decide(base, 500, th, rearm, gap) {
		t.Fatal("first low reading should fire")
	}
	// stays low for a long time, well past the gap - must NOT re-fire (never re-armed)
	for i := 1; i <= 30; i++ {
		if w.decide(base.Add(time.Duration(i)*time.Minute), 500, th, rearm, gap) {
			t.Fatalf("re-fired at minute %d without recovery", i)
		}
	}
}

func newTestMonitor(f *fakeSampler, notifyEnabled bool) (*Monitor, *logbus.Bus, *[]string, *time.Time) {
	bus := logbus.New(256)
	var toasts []string
	clk := time.Unix(1_700_000_000, 0)
	m := New(Options{
		Log:           bus,
		Sampler:       f,
		NotifyEnabled: notifyEnabled,
		Notify:        func(title, body string) { toasts = append(toasts, title+"|"+body) },
		now:           func() time.Time { return clk },
	})
	return m, bus, &toasts, &clk
}

func countMsg(bus *logbus.Bus, level logbus.Level, msg string) int {
	n := 0
	for _, e := range bus.Snapshot() {
		if e.Level == level && e.Msg == msg {
			n++
		}
	}
	return n
}

func TestSampleAdaptersWarnsAndSweepsOnCrossing(t *testing.T) {
	f := &fakeSampler{
		adapters: []AdapterUsage{{Name: "RTX", LUID: "0:1", BudgetMB: 12000, UsedMB: 11500, FreeMB: 500}},
		procs:    []AdapterProcs{{Adapter: "RTX", Procs: []ProcUsage{{Name: "Arena.exe", PID: 1, MB: 7000}, {Name: "obs64.exe", PID: 2, MB: 300}}}},
	}
	m, bus, toasts, _ := newTestMonitor(f, true)
	m.sampleAdapters()

	if got := countMsg(bus, logbus.Info, "vram"); got != 1 {
		t.Errorf("adapter growth-curve line count=%d want 1", got)
	}
	if got := countMsg(bus, logbus.Warn, "GPU memory nearly exhausted - OpenGL/DirectX interop creation will start failing (Spout senders/receivers in any app: Resolume, OBS, VRChat)"); got != 1 {
		t.Errorf("warn line count=%d want 1", got)
	}
	if f.procCalls != 1 {
		t.Errorf("immediate process sweep on crossing: procCalls=%d want 1", f.procCalls)
	}
	if f.lastTopN != sweepTopN {
		t.Errorf("sweep topN=%d want %d", f.lastTopN, sweepTopN)
	}
	if got := countMsg(bus, logbus.Info, "vram by process"); got != 1 {
		t.Errorf("process attribution line count=%d want 1", got)
	}
	if len(*toasts) != 1 {
		t.Fatalf("toast count=%d want 1", len(*toasts))
	}
	if want := "GPU memory nearly full|GPU memory nearly full (11.2/11.7 GB) - Spout/video interop may start failing. Close GPU-heavy apps (browser, extra sources)."; (*toasts)[0] != want {
		t.Errorf("toast=%q want %q", (*toasts)[0], want)
	}
}

func TestSampleAdaptersHealthyNoWarn(t *testing.T) {
	f := &fakeSampler{
		adapters: []AdapterUsage{{Name: "RTX", LUID: "0:1", BudgetMB: 12000, UsedMB: 4000, FreeMB: 8000}},
	}
	m, bus, toasts, _ := newTestMonitor(f, true)
	m.sampleAdapters()
	if got := countMsg(bus, logbus.Warn, "GPU memory nearly exhausted - OpenGL/DirectX interop creation will start failing (Spout senders/receivers in any app: Resolume, OBS, VRChat)"); got != 0 {
		t.Errorf("healthy adapter warned %d times", got)
	}
	if f.procCalls != 0 {
		t.Errorf("healthy adapter triggered %d process sweeps", f.procCalls)
	}
	if len(*toasts) != 0 {
		t.Errorf("healthy adapter raised %d toasts", len(*toasts))
	}
}

func TestNotifyGate(t *testing.T) {
	f := &fakeSampler{
		adapters: []AdapterUsage{{Name: "RTX", LUID: "0:1", BudgetMB: 12000, UsedMB: 11500, FreeMB: 500}},
	}
	m, bus, toasts, _ := newTestMonitor(f, false) // notify disabled
	m.sampleAdapters()
	if got := countMsg(bus, logbus.Warn, "GPU memory nearly exhausted - OpenGL/DirectX interop creation will start failing (Spout senders/receivers in any app: Resolume, OBS, VRChat)"); got != 1 {
		t.Errorf("warn log should still emit with notify off: got %d", got)
	}
	if len(*toasts) != 0 {
		t.Errorf("notify disabled but %d toasts raised", len(*toasts))
	}
}

// TestSampleAdaptersTenMinuteSuppression drives sampleAdapters over the loop with a moving
// clock: a sustained-then-recovered-then-dropped adapter warns twice, 10 min apart.
func TestSampleAdaptersTenMinuteSuppression(t *testing.T) {
	f := &fakeSampler{}
	m, bus, _, clk := newTestMonitor(f, true)
	warnMsg := "GPU memory nearly exhausted - OpenGL/DirectX interop creation will start failing (Spout senders/receivers in any app: Resolume, OBS, VRChat)"

	low := AdapterUsage{Name: "RTX", LUID: "0:1", BudgetMB: 12000, UsedMB: 11500, FreeMB: 500}
	high := AdapterUsage{Name: "RTX", LUID: "0:1", BudgetMB: 12000, UsedMB: 4000, FreeMB: 8000}

	set := func(a AdapterUsage) { f.adapters = []AdapterUsage{a} }
	adv := func(d time.Duration) { *clk = clk.Add(d) }

	set(low)
	m.sampleAdapters() // fire #1
	adv(2 * time.Minute)
	set(low)
	m.sampleAdapters() // suppressed (disarmed)
	set(high)
	m.sampleAdapters() // recover -> re-arm
	adv(1 * time.Minute)
	set(low)
	m.sampleAdapters() // <10min since fire#1 -> suppressed
	adv(10 * time.Minute)
	set(low)
	m.sampleAdapters() // gap elapsed -> fire #2

	if got := countMsg(bus, logbus.Warn, warnMsg); got != 2 {
		t.Errorf("warn count=%d want 2", got)
	}
}

func TestTopString(t *testing.T) {
	got := topString([]ProcUsage{{Name: "Arena.exe", MB: 7786}, {Name: "obs64.exe", MB: 313}})
	if want := "Arena.exe=7786 obs64.exe=313"; got != want {
		t.Errorf("topString=%q want %q", got, want)
	}
	if got := topString(nil); got != "" {
		t.Errorf("empty topString=%q want empty", got)
	}
}
