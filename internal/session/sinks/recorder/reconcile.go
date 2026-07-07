package recorder

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/session/reconcile"
	"rave.page/mate/internal/store"
)

// HistoryMeta is collection metadata for a played file path (resolver output).
type HistoryMeta struct {
	Title, Artist, Album, Key string
	BPM                       float64
}

// HistoryResolver enriches a played file path with collection metadata (ok=false if unknown).
// The caller bridges libdb.TrackByPath → HistoryMeta.
type HistoryResolver func(path string) (HistoryMeta, bool)

// ReconcileWithHistory rebuilds a finished recording's tracklist from the authoritative
// Traktor history session that best overlaps its time window (per-track wall-clock times),
// enriching each played path via resolve. The live capture is best-effort; the history file
// (written when Traktor closed) is ground truth, so this replaces the tracklist and persists.
// Errors if the recording is unknown/in-progress or no history session overlaps.
func (r *Recorder) ReconcileWithHistory(recID, historyDir string, resolve HistoryResolver) (*Recording, error) {
	sessions, err := musiclib.LoadSessions(historyDir)
	if err != nil {
		return nil, fmt.Errorf("load Traktor history: %w", err)
	}
	return r.ReconcileWithSessions(recID, sessions, resolve)
}

// ReconcileWithSessions is ReconcileWithHistory over pre-loaded sessions, so a sweep over
// many recordings loads (and the AutoReconciler's cache parses) the history dir once.
func (r *Recorder) ReconcileWithSessions(recID string, sessions []musiclib.Session, resolve HistoryResolver) (*Recording, error) {
	rec, ok := r.Get(recID)
	if !ok {
		return nil, fmt.Errorf("recording %s not found", recID)
	}
	if rec.EndedAt.IsZero() {
		return nil, fmt.Errorf("recording still in progress - stop it first")
	}
	m, ok := reconcile.MatchSession(rec.StartedAt, rec.EndedAt, sessions)
	if !ok {
		return nil, fmt.Errorf("no Traktor history session overlaps this recording's time window")
	}

	tracks := make([]Track, 0, len(m.Tracks))
	for _, mt := range m.Tracks {
		t := Track{
			StartedAt:   rec.StartedAt.Add(mt.Offset),
			Deck:        deckLetter(mt.Deck),
			TitleSource: "traktor.history",
			// The history file's own embedded metadata - present for every played track, incl. ones a
			// deck loaded from a folder never imported into the library (the "unknown track" case).
			Title:  mt.Title,
			Artist: mt.Artist,
			Album:  mt.Album,
			Key:    mt.Key,
			BPM:    mt.BPM,
		}
		// Library lookup enriches only what the history lacked (extra fields / a richer collection entry);
		// it never overwrites a good history title with an empty resolve.
		if meta, ok := resolve(mt.Path); ok {
			t.Title = orStr(t.Title, meta.Title)
			t.Artist = orStr(t.Artist, meta.Artist)
			t.Album = orStr(t.Album, meta.Album)
			t.Key = orStr(t.Key, meta.Key)
			if t.BPM == 0 {
				t.BPM = meta.BPM
			}
		}
		tracks = append(tracks, t)
	}
	// End each track where the next begins; the last ends with the recording.
	for i := range tracks {
		if i+1 < len(tracks) {
			tracks[i].EndedAt = tracks[i+1].StartedAt
		}
	}
	if len(tracks) > 0 {
		tracks[len(tracks)-1].EndedAt = rec.EndedAt
	}

	rec.Tracks = tracks
	rec.ReconciledAt = r.clock()
	if err := r.st.PutJSON(store.BucketRecordings, rec.ID, &rec); err != nil {
		return nil, fmt.Errorf("persist reconciled recording: %w", err)
	}
	r.log.Info(source, "reconciled with Traktor history", map[string]any{
		"id": rec.ID, "session": m.SessionName, "tracks": len(tracks), "overlap": m.Overlap.String(),
	})
	return &rec, nil
}

// orStr returns a if non-empty, else b (fill an empty field without clobbering a set one).
func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// deckLetter maps a 0-based Traktor history deck index (0..3) to A..D.
func deckLetter(n int) string {
	if n >= 0 && n <= 3 {
		return string(rune('A' + n))
	}
	return ""
}

// AutoReconciler watches the Traktor History folder and reconciles finished, not-yet-matched
// recordings the moment Traktor writes the authoritative history (on close) - no button press.
// Driven by filesystem events (prompt) + a periodic sweep (covers the recording being stopped
// after the history file already exists, and a missed/coalesced fs event). Idempotent: a
// recording is matched once (ReconciledAt stamp), so the sweep won't re-overwrite it.
type AutoReconciler struct {
	rec        *Recorder
	historyDir func() string // resolved fresh (config override / newest install)
	resolve    HistoryResolver
	log        *logbus.Bus
	onChange   func()                 // optional UI-refresh nudge after an auto-reconcile
	cache      *musiclib.SessionCache // incremental history parsing across sweeps

	statMu      sync.Mutex
	sweeps      uint64
	lastPending int
}

// SetOnChange registers a callback fired after a recording is auto-reconciled (UI refresh).
func (a *AutoReconciler) SetOnChange(f func()) { a.onChange = f }

// Stats renders sweep + session-cache counters (perfmon probe).
func (a *AutoReconciler) Stats() string {
	a.statMu.Lock()
	sweeps, pending := a.sweeps, a.lastPending
	a.statMu.Unlock()
	return fmt.Sprintf("sweeps=%d lastPending=%d | history %s", sweeps, pending, a.cache.Stats())
}

// NewAutoReconciler wires the watcher to a recorder, a history-dir resolver, and a metadata
// resolver (libdb.TrackByPath bridge).
func NewAutoReconciler(rec *Recorder, historyDir func() string, resolve HistoryResolver, log *logbus.Bus) *AutoReconciler {
	return &AutoReconciler{rec: rec, historyDir: historyDir, resolve: resolve, log: log, cache: musiclib.NewSessionCache()}
}

const (
	reconcileSettle = 3 * time.Second // wait for the history file write to finish before reading
	// Periodic fallback cadence. Cheap enough to keep sub-minute: a sweep with no pending
	// recording does zero I/O, and one with pending is ReadDir + stat - the SessionCache
	// re-parses only new/changed NMLs.
	reconcileSweep = 30 * time.Second
)

// Start blocks watching until ctx is cancelled (run in a goroutine).
func (a *AutoReconciler) Start(ctx context.Context) {
	var events chan fsnotify.Event
	if dir := a.historyDir(); dir != "" {
		if w, err := fsnotify.NewWatcher(); err == nil {
			if w.Add(dir) == nil {
				events = w.Events
				defer func() { _ = w.Close() }()
			} else {
				_ = w.Close()
			}
		}
	}

	settle := time.NewTimer(time.Hour)
	settle.Stop()
	sweep := time.NewTicker(reconcileSweep)
	defer sweep.Stop()

	a.sweep() // catch anything already pending at launch
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			settle.Reset(reconcileSettle) // debounce a burst of write events into one sweep
		case <-settle.C:
			a.sweep()
		case <-sweep.C:
			a.sweep()
		}
	}
}

// sweep reconciles every finished, not-yet-matched recording for which a history session now
// overlaps. A "no overlapping session" error is expected (Traktor still open) → left for a
// later sweep. No pending recordings → no I/O; otherwise sessions load once through the
// mtime/size cache (only new/changed NMLs re-parse).
func (a *AutoReconciler) sweep() {
	dir := a.historyDir()
	if dir == "" {
		return
	}
	var pending []Recording
	for _, rec := range a.rec.List() {
		if rec.EndedAt.IsZero() || !rec.ReconciledAt.IsZero() {
			continue
		}
		pending = append(pending, rec)
	}
	a.statMu.Lock()
	a.sweeps++
	a.lastPending = len(pending)
	a.statMu.Unlock()
	if len(pending) == 0 {
		return
	}
	sessions, err := a.cache.Load(dir)
	if err != nil {
		return
	}
	for _, rec := range pending {
		updated, err := a.rec.ReconcileWithSessions(rec.ID, sessions, a.resolve)
		if err != nil {
			continue
		}
		a.log.Info(source, "auto-reconciled with Traktor history", map[string]any{
			"id": rec.ID, "name": rec.Name, "tracks": len(updated.Tracks),
		})
		if a.onChange != nil {
			a.onChange() // nudge the UI to rebuild the recordings list
		}
	}
}
