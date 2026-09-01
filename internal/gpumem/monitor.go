package gpumem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rave.page/mate/internal/logbus"
)

// Watchdog tuning.
const (
	rearmMarginMB = 256              // free must recover to threshold+this before re-arming
	minWarnGap    = 10 * time.Minute // at most one warning per adapter per this window
	autoFloorMB   = 1024             // auto-threshold floor
	autoPctNum    = 8                // auto-threshold = max(floor, budget * 8%)
	autoPctDen    = 100
	sweepTopN     = 8 // processes logged per adapter
)

// Notify raises a user-facing toast (title, body). Matches the app frontend.Notify seam.
type Notify func(title, body string)

// Options configures the sampler + watchdog. Zero durations take the defaults.
type Options struct {
	Log     *logbus.Bus
	Notify  Notify  // nil-safe user toast on low-VRAM
	Sampler Sampler // nil => NewSampler() (the platform D3DKMT sampler / no-op stub)

	SampleInterval  time.Duration // adapter sample cadence (default 60s)
	ProcessInterval time.Duration // process sweep cadence (default 5m)
	WarnFreeMB      uint64        // warn threshold; 0 => auto = max(1024, 8% of budget)
	NotifyEnabled   bool          // gate the toast (log lines always emit)

	now func() time.Time // test clock hook; nil => time.Now
}

func (o *Options) applyDefaults() {
	if o.SampleInterval <= 0 {
		o.SampleInterval = 60 * time.Second
	}
	if o.ProcessInterval <= 0 {
		o.ProcessInterval = 5 * time.Minute
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.Log == nil {
		o.Log = logbus.New(64)
	}
	if o.Sampler == nil {
		o.Sampler = NewSampler()
	}
}

// Monitor samples VRAM on a cadence, logs the growth curve, and warns before exhaustion.
type Monitor struct {
	opt    Options
	states map[string]*watchState // per-adapter (keyed by LUID) hysteresis state
}

// New builds a Monitor. Cheap; no syscalls until Run.
func New(opt Options) *Monitor {
	opt.applyDefaults()
	return &Monitor{opt: opt, states: map[string]*watchState{}}
}

// Run drives the sampler until ctx is cancelled. Adapter sample every SampleInterval;
// process sweep every ProcessInterval and immediately on any warn-threshold crossing.
func (m *Monitor) Run(ctx context.Context) {
	m.sampleAdapters() // t0 baseline for the growth curve
	sTick := time.NewTicker(m.opt.SampleInterval)
	pTick := time.NewTicker(m.opt.ProcessInterval)
	defer sTick.Stop()
	defer pTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sTick.C:
			m.sampleAdapters()
		case <-pTick.C:
			m.processSweep()
		}
	}
}

// sampleAdapters logs one line per adapter + folds each into its watchdog state.
func (m *Monitor) sampleAdapters() {
	ads, err := m.opt.Sampler.Sample()
	if err != nil {
		m.opt.Log.Debug("gpumem", "sample failed", map[string]any{"err": err.Error()})
		return
	}
	now := m.opt.now()
	for _, a := range ads {
		m.opt.Log.Info("gpumem", "vram", map[string]any{
			"adapter": a.Name, "usedMB": a.UsedMB, "freeMB": a.FreeMB, "budgetMB": a.BudgetMB,
		})
		st := m.states[a.LUID]
		if st == nil {
			st = &watchState{}
			m.states[a.LUID] = st
		}
		if st.decide(now, a.FreeMB, threshold(m.opt.WarnFreeMB, a.BudgetMB), rearmMarginMB, minWarnGap) {
			m.warn(a)
		}
	}
}

// warn logs the exhaustion warning, triggers an immediate process sweep (attribution at the
// moment of crossing), and raises the toast.
func (m *Monitor) warn(a AdapterUsage) {
	m.opt.Log.Warn("gpumem",
		"GPU memory nearly exhausted - OpenGL/DirectX interop creation will start failing (Spout senders/receivers in any app: Resolume, OBS, VRChat)",
		map[string]any{"adapter": a.Name, "usedMB": a.UsedMB, "budgetMB": a.BudgetMB, "freeMB": a.FreeMB})
	m.processSweep()
	if m.opt.NotifyEnabled && m.opt.Notify != nil {
		m.opt.Notify("GPU memory nearly full", notifyBody(a))
	}
}

// processSweep logs the top-N VRAM consumers per adapter (the leaker attribution line).
func (m *Monitor) processSweep() {
	aps, err := m.opt.Sampler.SampleProcesses(sweepTopN)
	if err != nil {
		m.opt.Log.Debug("gpumem", "process sweep failed", map[string]any{"err": err.Error()})
		return
	}
	for _, ap := range aps {
		m.opt.Log.Info("gpumem", "vram by process", map[string]any{
			"adapter": ap.Adapter, "top": topString(ap.Procs),
		})
	}
}

func notifyBody(a AdapterUsage) string {
	return fmt.Sprintf("GPU memory nearly full (%.1f/%.1f GB) - Spout/video interop may start failing. Close GPU-heavy apps (browser, extra sources).",
		float64(a.UsedMB)/1024, float64(a.BudgetMB)/1024)
}

func topString(ps []ProcUsage) string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%d", p.Name, p.MB)
	}
	return b.String()
}

// threshold resolves the warn free-MB threshold: explicit override, else max(floor, 8% budget).
func threshold(warnFreeMB, budgetMB uint64) uint64 {
	if warnFreeMB > 0 {
		return warnFreeMB
	}
	if p := budgetMB * autoPctNum / autoPctDen; p > autoFloorMB {
		return p
	}
	return autoFloorMB
}

// watchState is one adapter's warn hysteresis. A reading below threshold fires at most once,
// then stays disarmed until free recovers above threshold+rearm; a fired warning also holds
// off the next for minWarnGap (covers fast recover/drop oscillation).
type watchState struct {
	armed    bool
	inited   bool
	lastWarn time.Time
}

func (w *watchState) decide(now time.Time, freeMB, threshMB, rearmMB uint64, minGap time.Duration) bool {
	if !w.inited {
		w.armed, w.inited = true, true
	}
	if freeMB < threshMB {
		if w.armed && (w.lastWarn.IsZero() || now.Sub(w.lastWarn) >= minGap) {
			w.armed = false
			w.lastWarn = now
			return true
		}
		return false
	}
	if freeMB >= threshMB+rearmMB {
		w.armed = true
	}
	return false
}
