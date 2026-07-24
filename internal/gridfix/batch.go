package gridfix

import (
	"context"
	"time"

	"rave.page/mate/internal/musiclib"
)

// BatchTrack is one candidate track for a batch run (grid state as scanned from
// the collection; marker in ms to match collection storage).
type BatchTrack struct {
	Path        string
	Title       string   // display: "Artist - Title" when known
	OldBPM      float64  // stored BPM, 0 = none
	OldStartMs  *float64 // existing single grid marker (ms); nil = none
	MultiMarker bool     // >1 grid markers: manually gridded, never touch
	Locked      bool     // grid lock flag: never touch, never analyze
	Verified    bool     // user-confirmed grid: always skip (never re-touch), even in force mode
	RangeLo     float64  // target tempo band (0 = none): prior + octave choice fold into it
	RangeHi     float64
}

// analyzer is the beat-detection seam (Engine satisfies it; tests stub it).
type analyzer interface {
	Analyze(ctx context.Context, path string) (*Detection, error)
}

// BatchOptions tune per-track planning for a run.
type BatchOptions struct {
	MinQuality  float64     // min grid coverage to auto-fix (0 = default 0.85)
	ThresholdMS float64     // ignore corrections smaller than this (0 = default 12)
	BiasS       float64     // manual detector offset (s); Bias wins when non-empty
	Bias        Calibration // per-extension measured detector bias (nil = use BiasS)
	Checkpoint  string      // model checkpoint id recorded on cache entries + matched on read
	Force       bool        // re-analyze past the Locked/MultiMarker skips AND the detection cache
	// (verified tracks stay protected); for "force re-analyze" after a model change or a bad grid
}

// Batch is the READ-ONLY run orchestrator: detect → fit → plan per track,
// serially (the engine is serial by design). A separate writeback layer applies.
type Batch struct {
	a     analyzer
	cache *DetectionCache // nil = no caching
	opts  BatchOptions
}

// NewBatch wires a run; zero MinQuality/ThresholdMS take the plan defaults.
func NewBatch(a analyzer, cache *DetectionCache, opts BatchOptions) *Batch {
	if opts.MinQuality == 0 {
		opts.MinQuality = 0.85
	}
	if opts.ThresholdMS == 0 {
		opts.ThresholdMS = 12
	}
	return &Batch{a: a, cache: cache, opts: opts}
}

// etaWindow bounds the rolling analyze-duration sample for ETA.
const etaWindow = 20

// Run processes tracks serially, emitting progress after every track. Ctx cancel
// stops cleanly (phase=cancelled) and returns the results completed so far.
// Per-track analyze errors count as Failed and never abort the run.
func (b *Batch) Run(ctx context.Context, tracks []BatchTrack, onProgress func(BatchProgress)) []TrackResult {
	start := time.Now()
	results := make([]TrackResult, 0, len(tracks))
	p := BatchProgress{Phase: PhaseScanning, Total: len(tracks), StartedAt: start}
	emit := func() {
		p.Elapsed = time.Since(start)
		if onProgress != nil {
			onProgress(p)
		}
	}
	emit()
	p.Phase = PhaseAnalyzing
	if len(tracks) > 0 {
		p.Current = tracks[0].Title
	}
	emit()
	finish := func(phase BatchPhase) []TrackResult {
		p.Phase = phase
		p.Current = ""
		p.ETA = 0
		emit()
		return results
	}
	var durs []time.Duration // last etaWindow non-cached analyze durations
	misses := 0              // successful non-cached analyzes
	for i, t := range tracks {
		if ctx.Err() != nil {
			return finish(PhaseCancelled)
		}
		t0 := time.Now()
		res := TrackResult{Path: t.Path, Title: t.Title, OldBPM: t.OldBPM}
		switch {
		case t.Verified:
			// user confirmed this grid - never re-touch, not even in force mode
			res.Plan = Plan{Status: StatusSkip, Detail: "verified grid - protected", OldBPM: t.OldBPM}
		case !b.opts.Force && t.Locked:
			res.Plan = Plan{Status: StatusSkip, Detail: "grid locked - not touching", OldBPM: t.OldBPM}
		case !b.opts.Force && t.MultiMarker:
			// PlanFix short-circuits before touching fit; reuse its exact detail
			res.Plan = PlanFix(GridFit{}, nil, PlanInput{MultiMarker: true})
		default:
			var det *Detection
			// force bypasses the cache (fresh detection); else a checkpoint-matched cache hit
			if b.cache != nil && !b.opts.Force {
				det, res.FromCache = b.cache.Get(t.Path, b.opts.Checkpoint)
			}
			if res.FromCache {
				p.Cached++
			} else {
				d, err := b.a.Analyze(ctx, t.Path)
				if err != nil {
					if ctx.Err() != nil {
						return finish(PhaseCancelled) // in-flight track not counted
					}
					res.Err = err.Error()
					break
				}
				det = d
				if len(durs) == etaWindow {
					durs = durs[1:]
				}
				durs = append(durs, time.Since(t0))
				misses++
				if b.cache != nil {
					_ = b.cache.Put(t.Path, det, b.opts.Checkpoint) // cache persist failure is non-fatal
				}
			}
			res.Beats = len(det.Beats)
			// candidate seeding + tie-breaks use the band-folded prior so the
			// in-range octave wins even when the stored BPM is octave-wrong
			prior := t.OldBPM
			if p, ok := musiclib.FoldBPM(t.OldBPM, musiclib.BPMRange{Min: t.RangeLo, Max: t.RangeHi}); ok {
				prior = p
			}
			fit := FitConstantGrid(det.Beats, det.Downbeats, prior)
			if fit == nil {
				res.Plan = Plan{Status: StatusSkip,
					Detail: "no stable constant grid found - fix manually", OldBPM: t.OldBPM}
				break
			}
			bias := b.opts.BiasS
			if len(b.opts.Bias) > 0 {
				bias = b.opts.Bias.ForPath(t.Path) // calibrated per-ext bias wins
			}
			in := PlanInput{OldBPM: t.OldBPM, BiasS: bias,
				MinQuality: b.opts.MinQuality, ThresholdMS: b.opts.ThresholdMS,
				RangeLo: t.RangeLo, RangeHi: t.RangeHi}
			if t.OldStartMs != nil {
				s := *t.OldStartMs / 1000.0
				in.OldStartS = &s
			}
			res.Plan = PlanFix(*fit, det.Downbeats, in)
		}
		res.ElapsedMS = time.Since(t0).Milliseconds()
		results = append(results, res)
		p.Done++
		switch {
		case res.Err != "":
			p.Failed++
		case res.Plan.Status == StatusFix:
			p.Fixed++
		case res.Plan.Status == StatusOK:
			p.OK++
		default:
			p.Skipped++
		}
		p.Current = ""
		if i+1 < len(tracks) {
			p.Current = tracks[i+1].Title
		}
		// ETA = mean of last etaWindow non-cached analyze durations × estimated
		// remaining analyzes; estimate = remaining × observed miss rate
		// (misses/done), so cached + short-circuited tracks (~0 cost) scale it down
		p.ETA = 0
		if len(durs) > 0 {
			var sum time.Duration
			for _, d := range durs {
				sum += d
			}
			mean := sum / time.Duration(len(durs))
			remaining := float64(len(tracks) - p.Done)
			p.ETA = time.Duration(float64(mean) * remaining * float64(misses) / float64(p.Done))
		}
		emit()
	}
	return finish(PhaseDone)
}
