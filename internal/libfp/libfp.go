// Package libfp runs a paced, idle-time background sweep that proactively computes Chromaprint
// fingerprints for local library tracks that don't have one yet, persisting each into the libdb
// change_log (Store.AppendChanges, field "fingerprint", same shape internal/setfp writes). This
// makes prints EXIST so library sync can carry them - internal/playsync populates
// fingerprint_b64 from FingerprintForTrack - and so the public corpus grows beyond the handful
// of tracks that happened to play inside a captured set.
//
// The sweep is deliberately thin: a small batch per tick, only while the app is otherwise idle
// (governor), and only when the Fingerprint feature is enabled. Full coverage of a large
// (~17k-track) library takes days on purpose - fpcalc must never compete with a live set.
package libfp

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// Store is the libdb subset the sweep needs (*libdb.DB satisfies it).
type Store interface {
	LoadAllTracks() ([]musiclib.Track, error)
	FingerprintedHashes() (map[string]bool, error)
	AppendChanges(events []libdb.ChangeEvent) error
}

// Computer computes a whole-file Chromaprint for path. dur is the fpcalc-reported duration in
// seconds (may be 0). A non-nil error means "skip this file": the sweep logs it and moves on.
type Computer interface {
	Compute(ctx context.Context, path string) (fingerprint string, dur float64, err error)
}

// Logger is the minimal log surface the sweep uses (nil = silent). *logbus.Bus satisfies it.
type Logger interface {
	Info(area, msg string, fields map[string]any)
}

// Built-in pacing defaults, used when the config leaves a knob <= 0.
const (
	DefaultBatch    = 3
	DefaultInterval = 20 * time.Second
)

const logArea = "libfp"

// Options configures a Sweeper. Enabled is the load-bearing gate; the rest have safe fallbacks.
type Options struct {
	Batch    int                       // tracks fingerprinted per tick (<=0 => DefaultBatch)
	Interval time.Duration             // delay between ticks (<=0 => DefaultInterval)
	Enabled  func() bool               // feature gate; nil => never runs
	Allowed  func() bool               // idle gate (governor.BackgroundAllowed); nil => always allowed
	Wait     func(ctx context.Context) // park while busy (governor.WaitWhileBusy); nil => no-op
	Log      Logger
}

// Coverage is a point-in-time snapshot of library fingerprint coverage (set each sweep pass).
type Coverage struct {
	Total   int // real, titled + path'd, deduped tracks
	Covered int // already have a usable local print
	Pending int // queued to fingerprint this pass
}

// Stats counts what the sweep has done this process.
type Stats struct {
	Computed int
	Failed   int
}

// Sweeper is the paced library-fingerprint background worker.
type Sweeper struct {
	store   Store
	compute Computer
	batch   int
	every   time.Duration
	enabled func() bool
	allowed func() bool
	wait    func(ctx context.Context)
	log     Logger

	// queue state - owned solely by the Run goroutine (no locking needed).
	queue []job
	pos   int

	mu       sync.Mutex // guards the exported snapshots below
	stats    Stats
	coverage Coverage
}

type job struct {
	hash string
	path string
}

// New builds a Sweeper, applying defaults for any unset Option.
func New(store Store, compute Computer, o Options) *Sweeper {
	s := &Sweeper{
		store: store, compute: compute,
		batch: o.Batch, every: o.Interval,
		enabled: o.Enabled, allowed: o.Allowed, wait: o.Wait, log: o.Log,
	}
	if s.batch <= 0 {
		s.batch = DefaultBatch
	}
	if s.every <= 0 {
		s.every = DefaultInterval
	}
	if s.enabled == nil {
		s.enabled = func() bool { return false }
	}
	if s.allowed == nil {
		s.allowed = func() bool { return true }
	}
	if s.wait == nil {
		s.wait = func(context.Context) {}
	}
	return s
}

// Run drives the sweep until ctx is cancelled. Ticks are cheap no-ops while the feature is
// disabled or the app is busy, so it is safe to start once at app init and leave running.
func (s *Sweeper) Run(ctx context.Context) {
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick does at most one batch of work; exported-behaviour is unit-tested through it.
func (s *Sweeper) tick(ctx context.Context) {
	if !s.enabled() || !s.allowed() { // feature off, or a stream/drag in progress
		return
	}
	if s.pos >= len(s.queue) {
		if !s.refill() {
			return // nothing to fingerprint (or reload failed) - retry next tick
		}
	}
	s.drain(ctx, s.batch)
}

// refill rebuilds the work queue from the library: every real track that lacks a usable local
// print. Returns false (queue left empty) when there is nothing to do or the reload failed.
func (s *Sweeper) refill() bool {
	tracks, err := s.store.LoadAllTracks()
	if err != nil {
		s.info("reload tracks failed", map[string]any{"err": err.Error()})
		return false
	}
	have, err := s.store.FingerprintedHashes()
	if err != nil {
		s.info("reload prints failed", map[string]any{"err": err.Error()})
		return false
	}
	q := s.queue[:0]
	seen := make(map[string]bool, len(tracks))
	total, covered := 0, 0
	for _, t := range tracks {
		if t.Title == "" || t.Path == "" { // can't identify or fpcalc without both
			continue
		}
		h := libdb.TrackHash(t.Artist, t.Title, 0)
		if seen[h] { // dup identities fingerprint once
			continue
		}
		seen[h] = true
		total++
		if have[h] {
			covered++
			continue
		}
		q = append(q, job{hash: h, path: t.Path})
	}
	s.queue = q
	s.pos = 0
	s.setCoverage(Coverage{Total: total, Covered: covered, Pending: len(q)})
	if len(q) == 0 {
		return false
	}
	s.info("sweep pass", map[string]any{"pending": len(q), "covered": covered, "total": total})
	return true
}

// drain fingerprints up to budget queued tracks, persisting each success. A per-file failure is
// logged and skipped (never stalls the queue). Yields to live use before each file and bails
// out early when the feature is toggled off, the app goes busy, or ctx is cancelled.
func (s *Sweeper) drain(ctx context.Context, budget int) {
	for attempts := 0; s.pos < len(s.queue) && attempts < budget; attempts++ {
		if ctx.Err() != nil {
			return
		}
		s.wait(ctx) // park while a stream is live / window mid drag-resize
		if !s.enabled() || !s.allowed() {
			return
		}
		j := s.queue[s.pos]
		s.pos++
		fp, _, err := s.compute.Compute(ctx, j.path)
		if err != nil || fp == "" {
			s.bumpFailed()
			if err != nil {
				s.info("compute failed", map[string]any{"err": err.Error()})
			}
			continue
		}
		nv, _ := json.Marshal(fp)
		ev := libdb.ChangeEvent{
			TrackHash: j.hash, TrackFP: fp,
			Field: "fingerprint", Op: "set", Origin: "fingerprint", NewValue: string(nv),
		}
		if err := s.store.AppendChanges([]libdb.ChangeEvent{ev}); err != nil {
			s.bumpFailed()
			s.info("persist failed", map[string]any{"err": err.Error()})
			continue
		}
		s.bumpComputed()
	}
}

// Coverage returns the most recent coverage snapshot (zero value before the first pass).
func (s *Sweeper) Coverage() Coverage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coverage
}

// Stats returns cumulative computed/failed counts for this process.
func (s *Sweeper) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Sweeper) setCoverage(c Coverage) {
	s.mu.Lock()
	s.coverage = c
	s.mu.Unlock()
}

func (s *Sweeper) bumpComputed() {
	s.mu.Lock()
	s.stats.Computed++
	s.mu.Unlock()
}

func (s *Sweeper) bumpFailed() {
	s.mu.Lock()
	s.stats.Failed++
	s.mu.Unlock()
}

func (s *Sweeper) info(msg string, fields map[string]any) {
	if s.log != nil {
		s.log.Info(logArea, msg, fields)
	}
}
