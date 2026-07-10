package gridfix

import (
	"context"
	"time"
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
}

// analyzer is the beat-detection seam (Engine satisfies it; tests stub it).
type analyzer interface {
	Analyze(ctx context.Context, path string) (*Detection, error)
}

// BatchOptions tune per-track planning for a run.
type BatchOptions struct {
	MinQuality  float64 // min grid coverage to auto-fix (0 = default 0.85)
	ThresholdMS float64 // ignore corrections smaller than this (0 = default 12)
	BiasS       float64 // calibrated systematic detector offset (s)
	Checkpoint  string  // model checkpoint id recorded on cache entries
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
		case t.Locked:
			res.Plan = Plan{Status: StatusSkip, Detail: "grid locked - not touching", OldBPM: t.OldBPM}
		case t.MultiMarker:
			// PlanFix short-circuits before touching fit; reuse its exact detail
			res.Plan = PlanFix(GridFit{}, nil, PlanInput{MultiMarker: true})
		default:
			var det *Detection
			if b.cache != nil {
				det, res.FromCache = b.cache.Get(t.Path)
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
			fit := FitConstantGrid(det.Beats, det.Downbeats, t.OldBPM)
			if fit == nil {
				res.Plan = Plan{Status: StatusSkip,
					Detail: "no stable constant grid found - fix manually", OldBPM: t.OldBPM}
				break
			}
			in := PlanInput{OldBPM: t.OldBPM, BiasS: b.opts.BiasS,
				MinQuality: b.opts.MinQuality, ThresholdMS: b.opts.ThresholdMS}
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
