// Package gpubench runs bounded GPU micro-benchmarks that populate the gpubudget throughput cache, so
// the planner's `auto` selection can require a device to sustain a route's pixel rate. Benchmarks are
// GATED (BenchAllowed: headroom above reserve, machine idle, no protected OBS/Parsec encode, and -
// for the lazy path - no live route on that adapter), SERIALIZED (one at a time), and WALL-CLOCK
// BOUNDED (the Timer honours ctx's deadline and kills its session). It runs ONLY in the supervised
// media child (the Timer opens an mfenc session there), never the daemon.
//
// OS-neutral: the GPU timing is injected as a Timer, so the orchestration (gating, timeout, persist,
// keying) is table-tested with fakes and a Linux VAAPI timer can plug into the same Runner later.
package gpubench

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/gpubudget"
)

// defaultTimeout bounds one bucket's benchmark; defaultFPS is the synthetic input rate.
const (
	defaultTimeout = 6 * time.Second
	defaultFPS     = 60
)

// Target is one bucket to benchmark.
type Target struct {
	Op          gpubudget.Op // OpEncode | OpDecode
	AdapterLUID string       // "" = engine default / CPU
	Encoder     string       // concrete encoder (diagnostic; the Timer decides the real one)
	Codec       string       // "h264","hevc"
	W, H, FPS   int
}

func (t Target) geom() gpubudget.Geom {
	return gpubudget.Geom{W: t.W, H: t.H, FPS: t.FPS, Codec: t.Codec}
}

// Timer runs a bounded timed loop and returns the measured throughput (pixels/sec). It MUST honour
// ctx's deadline (kill the session/child on timeout). A nil Timer means that op is unsupported here.
type Timer func(ctx context.Context, t Target) (pps float64, err error)

// Options wires a Runner. Budget + Gate are required; the Timers are mfenc-backed (nil = op skipped).
type Options struct {
	Budget    *gpubudget.Budget
	Gate      func() encoderscan.BenchGate  // live gate inputs (headroom/idle/protected/bench cost)
	Encode    Timer                         // encode throughput timer (mfenc ProcSession)
	Decode    Timer                         // decode throughput timer (follow-up: needs a throwaway dest)
	RouteLive func(adapterLUID string) bool // true => skip the LAZY bench on that adapter; nil = never live
	Timeout   time.Duration                 // per-bucket wall clock (default 6s)
	Log       func(msg string, kv map[string]any)
}

// wantedCap bounds the lazy work list (buckets noted but not yet measured). It is a work list, not a
// cache; drop-oldest on overflow keeps it from growing with route churn.
const wantedCap = 32

// Runner orchestrates benchmarks. Safe for concurrent use; at most one benchmark runs at a time.
type Runner struct {
	o      Options
	mu     sync.Mutex
	busy   bool
	wanted []Target // unmeasured buckets noted by Want, drained by Tick when the adapter is idle
}

// New builds a Runner with defaults applied.
func New(o Options) *Runner {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.Log == nil {
		o.Log = func(string, map[string]any) {}
	}
	return &Runner{o: o}
}

// Want notes an unmeasured bucket for the lazy background sweep (idempotent; bounded). Called on an
// encode/decode open with the route's geometry; the actual benchmark runs later via Tick, only when
// no route is live on that adapter (a route never waits on a benchmark).
func (r *Runner) Want(t Target) {
	if r.o.Budget == nil || r.timerFor(t.Op) == nil {
		return
	}
	t.FPS = orFPS(t.FPS)
	if _, ok := r.o.Budget.Throughput(t.Op, t.geom()); ok {
		return // already measured
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.wanted {
		if w == t {
			return
		}
	}
	if len(r.wanted) >= wantedCap {
		r.wanted = r.wanted[1:] // drop-oldest
	}
	r.wanted = append(r.wanted, t)
}

// Tick drains ONE wanted bucket if nothing is running, the adapter is idle (no live route), and the
// gate allows. Non-blocking: the benchmark runs in the background. Returns true if one was kicked.
// Call it periodically (e.g. the media child's telemetry ticker); cheap when nothing is wanted.
func (r *Runner) Tick(ctx context.Context) bool {
	r.mu.Lock()
	if r.busy || len(r.wanted) == 0 {
		r.mu.Unlock()
		return false
	}
	idx := -1
	for i, w := range r.wanted {
		if r.o.RouteLive == nil || !r.o.RouteLive(w.AdapterLUID) {
			idx = i
			break
		}
	}
	if idx < 0 {
		r.mu.Unlock()
		return false // every wanted bucket's adapter has a live route - try later
	}
	t := r.wanted[idx]
	r.wanted = append(r.wanted[:idx], r.wanted[idx+1:]...)
	r.busy = true
	r.mu.Unlock()
	go func() {
		defer func() { r.mu.Lock(); r.busy = false; r.mu.Unlock() }()
		cctx, cancel := context.WithTimeout(ctx, r.o.Timeout)
		defer cancel()
		if _, err := r.runOne(cctx, t); err != nil {
			r.o.Log("lazy benchmark skipped/failed", map[string]any{"op": string(t.Op),
				"adapter": t.AdapterLUID, "geom": fmt.Sprintf("%dx%d", t.W, t.H), "err": err.Error()})
		}
	}()
	return true
}

func (r *Runner) timerFor(op gpubudget.Op) Timer {
	if op == gpubudget.OpDecode {
		return r.o.Decode
	}
	return r.o.Encode
}

// WantEncode notes an unmeasured encode bucket for the lazy sweep (convenience over Want).
func (r *Runner) WantEncode(adapterLUID, encoder, codec string, w, h, fps int) {
	r.Want(Target{Op: gpubudget.OpEncode, AdapterLUID: adapterLUID, Encoder: encoder, Codec: codec, W: w, H: h, FPS: fps})
}

// runOne gates, times (bounded), and persists one bucket. The gate is checked HERE so the explicit
// and lazy paths share one policy. Returns the measured pps (and persists it) or an error.
func (r *Runner) runOne(ctx context.Context, t Target) (float64, error) {
	timer := r.timerFor(t.Op)
	if timer == nil {
		return 0, fmt.Errorf("gpubench: no %s timer", t.Op)
	}
	if r.o.Gate != nil {
		if ok, reason := encoderscan.BenchAllowed(r.o.Gate()); !ok {
			return 0, fmt.Errorf("gate denied: %s", reason)
		}
	}
	pps, err := timer(ctx, t)
	if err != nil {
		return 0, err
	}
	if pps <= 0 {
		return 0, fmt.Errorf("gpubench: non-positive throughput")
	}
	r.o.Budget.ObserveThroughput(t.Op, t.geom(), pps)
	r.o.Log("benchmark measured", map[string]any{"op": string(t.Op), "adapter": t.AdapterLUID,
		"geom": fmt.Sprintf("%dx%d@%d", t.W, t.H, t.FPS), "codec": t.Codec, "Mpps": pps / 1e6})
	return pps, nil
}

// RunAll runs targets explicitly (ctl encoder-bench): still gated on headroom/idle/protected, but NOT
// on RouteLive (the user asked). Serialized. Returns a printable table. Safe to call off any goroutine.
func (r *Runner) RunAll(ctx context.Context, targets []Target) string {
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return "gpubench: a benchmark is already running\n"
	}
	r.busy = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.busy = false; r.mu.Unlock() }()

	var b strings.Builder
	b.WriteString("op      adapter              codec  geom         result\n")
	for _, t := range targets {
		t.FPS = orFPS(t.FPS)
		cctx, cancel := context.WithTimeout(ctx, r.o.Timeout)
		pps, err := r.runOne(cctx, t)
		cancel()
		res := fmt.Sprintf("%.0f Mpps", pps/1e6)
		if err != nil {
			res = "skip/fail: " + err.Error()
		}
		fmt.Fprintf(&b, "%-7s %-20s %-6s %-12s %s\n", t.Op, short(t.AdapterLUID), t.Codec, fmt.Sprintf("%dx%d@%d", t.W, t.H, t.FPS), res)
	}
	return b.String()
}

// EncodeTargets expands the standard encode buckets (1080p/1440p/2160p) for one adapter+codec.
func EncodeTargets(adapterLUID, encoder, codec string, fps int) []Target {
	buckets := [][2]int{{1920, 1080}, {2560, 1440}, {3840, 2160}}
	out := make([]Target, 0, len(buckets))
	for _, wh := range buckets {
		out = append(out, Target{Op: gpubudget.OpEncode, AdapterLUID: adapterLUID, Encoder: encoder,
			Codec: codec, W: wh[0], H: wh[1], FPS: orFPS(fps)})
	}
	return out
}

func orFPS(fps int) int {
	if fps <= 0 {
		return defaultFPS
	}
	return fps
}

func short(luid string) string {
	if luid == "" {
		return "(engine/cpu)"
	}
	return luid
}

// SortTargets orders targets deterministically (op, adapter, codec, pixels) for stable ctl output.
func SortTargets(ts []Target) {
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].Op != ts[j].Op {
			return ts[i].Op < ts[j].Op
		}
		if ts[i].AdapterLUID != ts[j].AdapterLUID {
			return ts[i].AdapterLUID < ts[j].AdapterLUID
		}
		return ts[i].W*ts[i].H < ts[j].W*ts[j].H
	})
}
