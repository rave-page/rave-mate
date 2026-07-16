// Package recorder is the session-recording sink: it watches the merged now-playing track
// and commits each one to a tracklist after it has been audibly playing long enough to
// count as "played" (mirrors Traktor's history-commit rule). Each recording captures
// per-track start/end times so a set can be exported as a tracklist and a recording's
// precise span is known. Recordings persist to the local store.
package recorder

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/store"
)

const source = "recorder"

// switchDebounce filters brief now-playing flaps (e.g. a quick cue on another deck) so a
// momentary blip doesn't split or duplicate a track.
const switchDebounce = 4 * time.Second

// autoSegmentGap is how long the now-playing slot must stay empty before the always-on
// recorder ends the current set (a long silence = the set is over). The next audible track
// auto-starts a fresh set.
const autoSegmentGap = 30 * time.Minute

// candidate is the track currently occupying the now-playing slot, with its confirm timer.
type candidate struct {
	key       string // identity (lowercased "title|artist")
	track     Track
	firstSeen time.Time
	confirmed bool
	idx       int // index in active.Tracks once confirmed
}

// Recorder is a session.Sink plus a control surface (Start/Stop/List/Export) for the UI.
type Recorder struct {
	log     *logbus.Bus
	st      *store.Store
	lib     *libdb.DB // change-history sink for play events; may be nil
	confirm time.Duration
	clock   func() time.Time

	mu            sync.Mutex
	active        *Recording
	cur           *candidate
	pendingKey    string
	pendingSince  time.Time
	lastAudibleAt time.Time // last time a track was audible (drives auto-segmentation)
	lastKey       string    // identity last observed in the now-playing slot ("" = silence)
	suppressKey   string    // after a manual stop: don't auto-restart while this key still plays
	seq           int       // disambiguates ids minted within the same nanosecond

	subMu   sync.Mutex
	subs    map[int]chan *Recording
	nextSub int

	// reconcile work-set: finished (EndedAt set) but not-yet-reconciled recording IDs. The
	// AutoReconciler reads this instead of Listing+filtering all recordings every 30s sweep -
	// empty set ⇒ the sweep does zero I/O. Seeded once at startup (seedPendingReconcile), added on
	// finish (Stop/autoFinalize/sweepStale), removed on reconcile stamp + delete.
	reconMu   sync.Mutex
	pendingRe map[string]struct{}

	// persist plumbing: store writes (bbolt fsync) run off r.mu on a single flusher
	// goroutine, so render-facing Active()/Get()/Pending() never wait on disk. FIFO
	// order preserved; consecutive same-id puts coalesce (newest wins), bounding the
	// queue by distinct ids in flight, not write rate.
	pmu   sync.Mutex
	pcond *sync.Cond // signaled when the flusher drains (drainPersist)
	pq    []persistOp
	pbusy bool

	// storeMu serializes read-modify-write cycles against BucketRecordings across ALL FOUR DIRECT
	// store writers - Rename's non-active path, ReconcileWithSessions, sweepStale and Delete all
	// Get/Put/Delete a whole Recording outside r.mu and outside the persist queue. Without it, a
	// Rename or Delete landing inside a reconcile's slow per-track resolve (tens-to-hundreds of ms
	// for a 30+ track set) is silently reverted by the reconciler's write-back: the rename undoes
	// itself, or the deleted set REAPPEARS.
	//
	// LOCK ORDER: storeMu → r.mu → r.pmu (→ reconMu, leaf). Never the reverse: nothing holding r.mu
	// takes storeMu (persistLocked only queues, taking r.pmu), so the order is acyclic. The flusher
	// is deliberately NOT a participant - it takes r.pmu only, so drainPersist() under storeMu always
	// makes progress, and every holder drains before reading (see Rename, Delete).
	//
	// The flusher is the fifth writer, reached only via queuePersist. Draining under storeMu is what
	// contains it, and that is final ONLY for a non-active id: persistLocked re-queues r.active's id
	// on every confirm/refresh, so Rename and Delete both branch on active BEFORE the drain.
	storeMu sync.Mutex

	// recVer bumps on every persisted-recordings mutation (put/delete, sweep + flusher). The webui
	// Publish list caches List() keyed by it so a full render doesn't rescan+unmarshal the bucket.
	recVer atomic.Uint64
}

// persistOp is one queued store write: rec != nil → put snapshot, else delete id.
type persistOp struct {
	id  string
	rec *Recording
}

// New constructs a recorder. confirmSeconds is how long a track must play before it counts.
// lib (may be nil) receives a play_event in the change history on each confirmed play.
func New(log *logbus.Bus, st *store.Store, lib *libdb.DB, confirmSeconds int) *Recorder {
	if confirmSeconds <= 0 {
		confirmSeconds = 30
	}
	r := &Recorder{
		log:     log,
		st:      st,
		lib:     lib,
		confirm: time.Duration(confirmSeconds) * time.Second,
		clock:   time.Now,
		subs:    map[int]chan *Recording{},
	}
	r.pcond = sync.NewCond(&r.pmu)
	return r
}

// ID implements session.Sink.
func (r *Recorder) ID() string { return source }

// Start drives the confirm/commit state machine off merger updates + a 1s tick (so track
// ends and confirmations fire even when updates pause). Recording itself is gated by
// StartRecording/StopRecording (manual or stream-driven).
func (r *Recorder) Start(ctx context.Context, m *session.Merger) error {
	r.sweepStale() // finalize recordings left open by an unclean exit
	ch, unsub := m.Subscribe()
	defer unsub()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			r.step(r.clock(), m.Snapshot())
		case _, ok := <-ch:
			if !ok {
				return nil
			}
			r.step(r.clock(), m.Snapshot())
		}
	}
}

// sweepStale finalizes persisted recordings with no end time that aren't the active one -
// the app died (or was killed) mid-set, so they'd read as "live" forever. End = the last
// track's end (else its start, else the set start); empty zombie sets are discarded.
func (r *Recorder) sweepStale() {
	r.mu.Lock()
	activeID := ""
	if r.active != nil {
		activeID = r.active.ID
	}
	r.mu.Unlock()
	// Direct BucketRecordings read-modify-write: serialize against Rename + ReconcileWithSessions.
	// r.mu is released above - storeMu is the OUTER lock (see its declaration).
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	r.drainPersist()
	for _, rec := range r.List() {
		if rec.ID == activeID {
			continue
		}
		if !rec.EndedAt.IsZero() { // already finished: seed the work-set if it never reconciled
			if rec.ReconciledAt.IsZero() {
				r.markPendingReconcile(rec.ID)
			}
			continue
		}
		if len(rec.Tracks) == 0 {
			_ = r.st.Delete(store.BucketRecordings, rec.ID)
			r.bumpRec()
			r.log.Info(source, "stale empty recording discarded", map[string]any{"id": rec.ID})
			continue
		}
		last := &rec.Tracks[len(rec.Tracks)-1]
		if last.EndedAt.IsZero() {
			last.EndedAt = last.StartedAt
		}
		rec.EndedAt = last.EndedAt
		if err := r.st.PutJSON(store.BucketRecordings, rec.ID, &rec); err != nil {
			r.log.Warn(source, "finalize stale recording failed", map[string]any{"id": rec.ID, "error": err.Error()})
			continue
		}
		r.bumpRec()
		r.markPendingReconcile(rec.ID) // finished + unreconciled
		r.log.Info(source, "stale recording finalized", map[string]any{"id": rec.ID, "tracks": len(rec.Tracks)})
	}
}

// step advances the state machine for one observation of the merged state.
func (r *Recorder) step(now time.Time, st session.UnifiedState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Explicit clock (testability) + staleness window: a dead source's leftover
	// isPlaying=true must not keep the recorder "live" forever.
	np, ok := st.DeriveNowPlayingAt(now, session.NowPlayingStaleAfter)
	key := ""
	if ok {
		key = ident(np)
	}

	// Always-on: the first audible track auto-starts a set (no manual "start recording").
	if r.active == nil {
		if key == "" {
			// The deck went quiet: the previously-playing track is no longer showing, so a
			// fresh play is allowed to start a new set again.
			r.suppressKey = ""
			r.lastKey = ""
			return
		}
		// After a manual stop we keep the recorder idle while the *same* track that was
		// playing at stop time is still on a deck (track hasn't changed) - otherwise we'd
		// immediately spin up a new set for a track the user already finished recording.
		if key == r.suppressKey {
			r.lastKey = key
			return
		}
		r.suppressKey = ""
		r.startLocked("", "")
	}
	r.lastKey = key

	// Set-end tracking: while any channel fader is up, advance LastFaderAt. It stops the instant the
	// DJ pulls the final fader down - a more accurate set end than the last track's slot (which lingers
	// / debounces). In-memory only; persisted at the existing persist points (confirm/finalize/stop).
	if faderActive(st) {
		r.active.LastFaderAt = now
	}

	// Track audibility for auto-segmentation; a long silence ends the set (next play starts one).
	if key != "" {
		r.lastAudibleAt = now
	} else if r.cur == nil && !r.lastAudibleAt.IsZero() && now.Sub(r.lastAudibleAt) >= autoSegmentGap {
		r.autoFinalizeLocked(r.lastAudibleAt)
		return
	}

	// Same track continues: cancel any pending switch; confirm once it's old enough.
	if r.cur != nil && key == r.cur.key && key != "" {
		r.pendingKey = ""
		if !r.cur.confirmed {
			r.fillCandidate(np) // late-arriving fields land before confirm commits the track
			if now.Sub(r.cur.firstSeen) >= r.confirm {
				r.confirmCurrent()
			}
		} else {
			r.refreshCurrent(np)
		}
		return
	}

	// A different track (or silence): require it to persist for switchDebounce before
	// committing the switch, so brief flaps are ignored.
	if r.pendingKey != key {
		r.pendingKey = key
		r.pendingSince = now
		return
	}
	if now.Sub(r.pendingSince) < switchDebounce {
		return
	}
	r.finalizeCurrent(r.pendingSince) // the old track ended when the new one became audible
	if key == "" {
		r.cur = nil
	} else {
		r.cur = &candidate{key: key, firstSeen: r.pendingSince, track: trackFrom(np)}
	}
	r.pendingKey = ""
}

// confirmCurrent appends the current candidate to the active tracklist.
func (r *Recorder) confirmCurrent() {
	t := r.cur.track
	t.StartedAt = r.cur.firstSeen
	r.active.Tracks = append(r.active.Tracks, t)
	r.cur.idx = len(r.active.Tracks) - 1
	r.cur.confirmed = true
	r.log.Info(source, "track confirmed", map[string]any{"title": t.Title, "artist": t.Artist, "deck": t.Deck})
	r.persistLocked()
	r.broadcastLocked()
	r.journalPlay(t)
	r.savePlayedLocked(r.cur.idx)
}

// savePlayedLocked upserts the consolidated play-log row for the track at idx in the active
// recording (caller holds r.mu). Keyed by recording id + slot so confirm-time inserts and
// later end-time / metadata updates address the same row. No-op without a DB.
func (r *Recorder) savePlayedLocked(idx int) {
	if r.lib == nil || r.active == nil || idx < 0 || idx >= len(r.active.Tracks) {
		return
	}
	t := r.active.Tracks[idx]
	if err := r.lib.SavePlayedTrack(libdb.PlayedTrack{
		ID:          fmt.Sprintf("%s#%d", r.active.ID, idx),
		RecordingID: r.active.ID,
		Artist:      t.Artist, Title: t.Title, Album: t.Album, Key: t.Key, BPM: t.BPM,
		Deck: t.Deck, TitleSource: t.TitleSource,
		StartedAt: t.StartedAt, EndedAt: t.EndedAt,
	}); err != nil {
		r.log.Warn(source, "persist played track failed", map[string]any{"error": err.Error()})
	}
}

// journalPlay records a confirmed play in the change history (origin recorder). Keyed by
// artist|title only (duration 0) since the now-playing slot carries no reliable duration.
func (r *Recorder) journalPlay(t Track) {
	if r.lib == nil {
		return
	}
	rid := ""
	if r.active != nil {
		rid = r.active.ID
	}
	nv, _ := json.Marshal(map[string]any{
		"deck": t.Deck, "startedAt": t.StartedAt.UTC().Format(time.RFC3339), "recordingId": rid,
	})
	_ = r.lib.AppendChanges([]libdb.ChangeEvent{{
		TrackHash: libdb.TrackHash(t.Artist, t.Title, 0),
		Field:     "play_event", Op: "play", Origin: "recorder", NewValue: string(nv),
	}})
}

// fillCandidate fills empty candidate fields from metadata arriving during the confirm
// window (e.g. NML album/key a beat behind the deck ingest), so the confirmed track
// carries the best-known fields from the start. Caller holds r.mu.
func (r *Recorder) fillCandidate(np session.NowPlaying) {
	t := &r.cur.track
	if t.Album == "" {
		t.Album = session.StringField(np.Fields, session.FieldAlbum)
	}
	if t.Key == "" {
		t.Key = session.StringField(np.Fields, session.FieldKey)
	}
	if t.BPM == 0 {
		t.BPM, _ = floatField(np.Fields, session.FieldBPM)
	}
	if t.Path == "" {
		t.Path = session.StringField(np.Fields, session.FieldPath)
	}
	if t.TitleSource == "" {
		if fv, ok := np.Fields[session.FieldTitle]; ok {
			t.TitleSource = fv.Source
		}
	}
}

// refreshCurrent fills in fields that arrived after a track was confirmed (e.g. NML album).
func (r *Recorder) refreshCurrent(np session.NowPlaying) {
	if !r.cur.confirmed {
		return
	}
	t := &r.active.Tracks[r.cur.idx]
	changed := false
	if t.Album == "" {
		if v := session.StringField(np.Fields, session.FieldAlbum); v != "" {
			t.Album, changed = v, true
		}
	}
	if t.Key == "" {
		if v := session.StringField(np.Fields, session.FieldKey); v != "" {
			t.Key, changed = v, true
		}
	}
	if t.BPM == 0 {
		if v, ok := floatField(np.Fields, session.FieldBPM); ok {
			t.BPM, changed = v, true
		}
	}
	if t.Path == "" {
		if v := session.StringField(np.Fields, session.FieldPath); v != "" {
			t.Path, changed = v, true
		}
	}
	if changed {
		r.persistLocked()
		r.broadcastLocked()
		r.savePlayedLocked(r.cur.idx)
	}
}

// finalizeCurrent stamps the end time on the confirmed current track.
func (r *Recorder) finalizeCurrent(endAt time.Time) {
	if r.cur != nil && r.cur.confirmed {
		r.active.Tracks[r.cur.idx].EndedAt = endAt
		r.persistLocked()
		r.broadcastLocked()
		r.savePlayedLocked(r.cur.idx)
	}
}

// ── control surface ──────────────────────────────────────────────────────────

// StartRecording begins a recording (idempotent - a second call returns the active one).
// streamID links it to a live stream when started from the stream lifecycle. Recording is
// always-on (auto-started by the play state machine); this is for the stream link + naming.
func (r *Recorder) StartRecording(name, streamID string) *Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startLocked(name, streamID)
}

// startLocked creates the active recording (caller holds r.mu). Idempotent.
func (r *Recorder) startLocked(name, streamID string) *Recording {
	if r.active != nil {
		if streamID != "" && r.active.StreamID == "" {
			r.active.StreamID = streamID
		}
		return r.active.clone()
	}
	now := r.clock()
	r.seq++
	if name == "" {
		name = "Set " + now.Local().Format("2006-01-02 15:04")
	}
	r.active = &Recording{
		ID:        fmt.Sprintf("rec_%d_%d", now.UnixNano(), r.seq),
		Name:      name,
		StreamID:  streamID,
		StartedAt: now,
	}
	r.cur = nil
	r.pendingKey = ""
	r.suppressKey = "" // an explicit start clears any post-stop hold-off
	r.lastAudibleAt = now
	r.log.Info(source, "recording started", map[string]any{"id": r.active.ID, "name": name, "streamId": streamID})
	r.persistLocked()
	r.broadcastLocked()
	return r.active.clone()
}

// autoFinalizeLocked ends the active set after a long silence (caller holds r.mu). A set with
// no confirmed tracks (only previews) is discarded so the list isn't cluttered with empties.
func (r *Recorder) autoFinalizeLocked(endAt time.Time) {
	r.finalizeCurrent(endAt)
	r.active.EndedAt = endAt
	done := r.active
	if len(done.Tracks) == 0 {
		r.queuePersist(done.ID, nil) // via queue: ordered after the start-time put, so the empty set can't resurrect
		r.log.Info(source, "empty set discarded", map[string]any{"id": done.ID})
	} else {
		r.persistLocked()
		r.markPendingReconcile(done.ID) // finished + unreconciled → auto-reconcile picks it up
		r.log.Info(source, "set auto-finalized (silence)", map[string]any{"id": done.ID, "tracks": len(done.Tracks)})
	}
	r.active = nil
	r.cur = nil
	r.pendingKey = ""
	r.broadcastLocked()
}

// StopRecording ends the active recording and returns it (nil if none was active).
func (r *Recorder) StopRecording() *Recording {
	r.mu.Lock()
	if r.active == nil {
		r.mu.Unlock()
		return nil
	}
	now := r.clock()
	r.finalizeCurrent(now)
	r.active.EndedAt = now
	done := r.active
	r.persistLocked()
	r.log.Info(source, "recording stopped", map[string]any{"id": done.ID, "tracks": len(done.Tracks)})
	// Stopping only closes the in/out window of *this* set. The recorder stays always-on, but
	// we hold off auto-starting a new set while the track that was playing at stop is still on
	// a deck (it hasn't changed) - a genuinely new track (or silence then a new track) resumes
	// auto-recording. This prevents an immediate duplicate set for the same in-flight track.
	r.suppressKey = r.lastKey
	r.active = nil
	r.cur = nil
	r.pendingKey = ""
	r.broadcastLocked()
	r.mu.Unlock()
	r.markPendingReconcile(done.ID) // finished + unreconciled → auto-reconcile picks it up
	r.drainPersist()                // stopped set durable + visible to List() before return; fsync waits off r.mu
	return done.clone()
}

// Pending describes the unconfirmed track occupying the now-playing slot: what's playing
// right now but hasn't met the confirm threshold yet.
type Pending struct {
	Track     Track
	FirstSeen time.Time // when it became audible
	ConfirmAt time.Time // when it commits to the tracklist (FirstSeen + confirm)
}

// Pending returns the candidate awaiting confirmation (ok=false when silent or the
// current track is already confirmed). Drives the cockpit's "confirming…" strip.
func (r *Recorder) Pending() (Pending, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil || r.cur.confirmed {
		return Pending{}, false
	}
	return Pending{Track: r.cur.track, FirstSeen: r.cur.firstSeen, ConfirmAt: r.cur.firstSeen.Add(r.confirm)}, true
}

// Active returns a copy of the in-progress recording (nil if not recording).
func (r *Recorder) Active() *Recording {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active.clone()
}

// List returns all persisted recordings, newest first.
func (r *Recorder) List() []Recording {
	raws, err := r.st.ListJSON(store.BucketRecordings)
	if err != nil {
		r.log.Warn(source, "list recordings failed", map[string]any{"error": err.Error()})
		return nil
	}
	out := make([]Recording, 0, len(raws))
	for _, raw := range raws {
		var rec Recording
		if err := json.Unmarshal(raw, &rec); err == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// RecordingsVersion returns a monotonic epoch bumped on every persisted-recordings mutation - a
// change-aware key for callers that cache List() (the webui Publish tab) so a full render reads the
// cache instead of rescanning+unmarshaling the whole recordings bucket.
func (r *Recorder) RecordingsVersion() uint64 { return r.recVer.Load() }

func (r *Recorder) bumpRec() { r.recVer.Add(1) }

// Get returns one recording (active, queued-for-persist, or persisted) by id.
func (r *Recorder) Get(id string) (Recording, bool) {
	r.mu.Lock()
	if r.active != nil && r.active.ID == id {
		c := r.active.clone()
		r.mu.Unlock()
		return *c, true
	}
	r.mu.Unlock()
	// Pending store writes: newest queued snapshot beats the (stale) on-disk copy.
	r.pmu.Lock()
	for i := len(r.pq) - 1; i >= 0; i-- {
		if r.pq[i].id == id {
			op := r.pq[i]
			r.pmu.Unlock()
			if op.rec == nil {
				return Recording{}, false // queued delete
			}
			return *op.rec.clone(), true
		}
	}
	r.pmu.Unlock()
	var rec Recording
	ok, err := r.st.GetJSON(store.BucketRecordings, id, &rec)
	if err != nil || !ok {
		return Recording{}, false
	}
	return rec, true
}

// maxRecordingName bounds a user-supplied set name, in runes (a name is free text: emoji and
// non-Latin scripts must count as one char each, not as their UTF-8 byte length).
const maxRecordingName = 200

// Rename sets a recording's display name (active or persisted). Resolves across active /
// pending-persist-queue / store like Get, so a queued snapshot flushing afterwards can't revert
// the new name.
func (r *Recorder) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("recording name empty")
	}
	if n := utf8.RuneCountInString(name); n > maxRecordingName {
		return fmt.Errorf("recording name too long (%d chars, max %d)", n, maxRecordingName)
	}

	// Active: rename in memory + queue a fresh snapshot. persistLocked appends/coalesces at the
	// queue TAIL, so it flushes after any older queued snapshot for this id (FIFO ⇒ newest wins).
	r.mu.Lock()
	if r.active != nil && r.active.ID == id {
		if r.active.Name == name {
			r.mu.Unlock()
			return nil
		}
		r.active.Name = name
		r.persistLocked()
		r.broadcastLocked()
		r.mu.Unlock()
		r.bumpRec()
		r.log.Info(source, "recording renamed", map[string]any{"id": id, "name": name})
		return nil
	}
	r.mu.Unlock()

	// Not active. Two different writers can clobber this read-modify-write; both are handled here.
	//
	// 1. The persist QUEUE. A snapshot for this id may be queued OR in flight (the flusher pops an op
	//    before writing, so an in-flight one is invisible to a pq scan) - it carries the old name and
	//    fresher tracks than the store. Draining lands it before we read, so we neither lose its
	//    tracks nor let it revert our name. Nothing can re-queue this id afterwards: only
	//    persistLocked queues puts and only for the ACTIVE recording, and ids are never reused.
	// 2. The other DIRECT store writers - ReconcileWithSessions and sweepStale Get/PutJSON the whole
	//    object, bypassing both the queue and r.mu, so draining does nothing about them. A reconcile
	//    interleaving here loses the rename (it writes back the name it read) or loses its own
	//    reconciled tracklist (we write back the object we read pre-reconcile). storeMu is what
	//    serializes them; take it BEFORE draining so the drain in 1. can't be undone by a reconcile
	//    that was already mid-cycle. Lock order storeMu → r.mu (released above) → r.pmu.
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	r.drainPersist()
	var rec Recording
	ok, err := r.st.GetJSON(store.BucketRecordings, id, &rec)
	if err != nil {
		return fmt.Errorf("read recording %s: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("recording %s not found", id)
	}
	if rec.Name == name {
		return nil
	}
	rec.Name = name
	if err := r.st.PutJSON(store.BucketRecordings, id, &rec); err != nil {
		return fmt.Errorf("persist renamed recording %s: %w", id, err)
	}
	r.bumpRec()
	r.log.Info(source, "recording renamed", map[string]any{"id": id, "name": name})
	return nil
}

// Delete removes a persisted recording. Refuses the in-progress one (stop it first).
//
// A fourth DIRECT BucketRecordings writer, so it takes storeMu like the other three. Unserialized,
// a delete landing inside ReconcileWithSessions' slow Get→resolve→Put window is undone by the
// reconciler's write-back of the object it read pre-delete - and the AutoReconciler fires that off a
// background fsnotify watcher over the Traktor history dir, so the user deletes a set and it just
// reappears. Same shape against Rename's read-modify-write.
func (r *Recorder) Delete(id string) error {
	// The ACTIVE recording is refused, not deleted: draining below lands only the writes queued SO
	// FAR, while persistLocked re-queues a put for r.active on every confirm/refresh and the flusher
	// (not a storeMu participant) would write the row straight back - a "successful" delete that
	// silently resurrects. Both renderers already gate their Delete button on EndedAt != 0; this
	// closes remotectl's RecDelete, which has no such gate. Wording mirrors ReconcileWithSessions.
	//
	// Not a TOCTOU: a recording only ever goes active → finished (startLocked mints a fresh
	// rec_<unixnano>_<seq> off a per-instance monotonic seq, so ids are never reused and a finished
	// one can never become active again). "Not active" here is therefore stable, which is exactly
	// what makes drain-then-delete below final.
	r.mu.Lock()
	active := r.active != nil && r.active.ID == id
	r.mu.Unlock()
	if active {
		return fmt.Errorf("recording still in progress - stop it first")
	}

	// Lock order storeMu → r.mu (released above) → r.pmu (drainPersist) → reconMu.
	r.storeMu.Lock()
	defer r.storeMu.Unlock()
	// Drain BEFORE deleting: a queued-but-unflushed snapshot (StopRecording queues then drains;
	// autoFinalizeLocked and confirmCurrent queue without draining) would otherwise land AFTER the
	// store delete and resurrect the row. Post-drain nothing can re-queue this id (see above).
	r.drainPersist()
	if err := r.st.Delete(store.BucketRecordings, id); err != nil {
		return err
	}
	r.clearPendingReconcile(id) // drop from the auto-reconcile work-set only once it's really gone
	r.bumpRec()                 // publish cache is epoch-keyed - without this the row lingers on screen
	r.log.Info(source, "recording deleted", map[string]any{"id": id})
	return nil
}

// markPendingReconcile adds a finished-unreconciled recording to the auto-reconcile work-set.
func (r *Recorder) markPendingReconcile(id string) {
	r.reconMu.Lock()
	if r.pendingRe == nil {
		r.pendingRe = map[string]struct{}{}
	}
	r.pendingRe[id] = struct{}{}
	r.reconMu.Unlock()
}

// clearPendingReconcile drops a recording from the work-set (reconciled or deleted).
func (r *Recorder) clearPendingReconcile(id string) {
	r.reconMu.Lock()
	delete(r.pendingRe, id)
	r.reconMu.Unlock()
}

// pendingReconcileIDs snapshots the current work-set (empty ⇒ the sweep is a no-op).
func (r *Recorder) pendingReconcileIDs() []string {
	r.reconMu.Lock()
	defer r.reconMu.Unlock()
	if len(r.pendingRe) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.pendingRe))
	for id := range r.pendingRe {
		out = append(out, id)
	}
	return out
}

// seedPendingReconcile scans persisted recordings once and marks every finished, not-yet-reconciled
// one - startup catch-up so a set stopped before Traktor wrote its history still auto-reconciles.
func (r *Recorder) seedPendingReconcile() {
	for _, rec := range r.List() {
		if !rec.EndedAt.IsZero() && rec.ReconciledAt.IsZero() {
			r.markPendingReconcile(rec.ID)
		}
	}
}

// FindByWindow returns the recording with the most temporal overlap with [start,end] across
// the in-progress + persisted recordings (ok=false if none overlaps). Used to link a captured
// set-audio file to the tracklist that was recorded over the same span.
func (r *Recorder) FindByWindow(start, end time.Time) (Recording, bool) {
	cands := r.List()
	if a := r.Active(); a != nil {
		cands = append(cands, *a)
	}
	var best Recording
	var bestOv time.Duration
	for _, rec := range cands {
		recEnd := rec.EndedAt
		if recEnd.IsZero() { // in-progress: treat as ongoing through the capture end
			recEnd = end
		}
		if ov := overlap(start, end, rec.StartedAt, recEnd); ov > bestOv {
			bestOv, best = ov, rec
		}
	}
	return best, bestOv > 0
}

// overlap is the duration two time intervals share (0 if disjoint).
func overlap(aStart, aEnd, bStart, bEnd time.Time) time.Duration {
	start := aStart
	if bStart.After(start) {
		start = bStart
	}
	end := aEnd
	if bEnd.Before(end) {
		end = bEnd
	}
	if d := end.Sub(start); d > 0 {
		return d
	}
	return 0
}

// Export renders a recording's tracklist in the given format.
func (r *Recorder) Export(id, format string) (string, error) {
	rec, ok := r.Get(id)
	if !ok {
		return "", fmt.Errorf("recording %s not found", id)
	}
	return rec.Export(format)
}

// Subscribe streams the active recording on every change (nil when recording stops).
func (r *Recorder) Subscribe() (<-chan *Recording, func()) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	id := r.nextSub
	r.nextSub++
	ch := make(chan *Recording, 8)
	r.subs[id] = ch
	return ch, func() {
		r.subMu.Lock()
		defer r.subMu.Unlock()
		if c, ok := r.subs[id]; ok {
			delete(r.subs, id)
			close(c)
		}
	}
}

// persistLocked queues a snapshot of the active recording for persistence (caller holds
// r.mu). The bbolt write (fsync) happens on the flusher goroutine outside r.mu.
func (r *Recorder) persistLocked() {
	if r.active == nil {
		return
	}
	r.queuePersist(r.active.ID, r.active.clone())
}

// queuePersist appends a store write (put when rec != nil, delete otherwise) and starts
// the flusher if idle. Consecutive puts for the same id coalesce (newest wins).
func (r *Recorder) queuePersist(id string, rec *Recording) {
	r.pmu.Lock()
	if n := len(r.pq); n > 0 && rec != nil && r.pq[n-1].rec != nil && r.pq[n-1].id == id {
		r.pq[n-1].rec = rec
	} else {
		r.pq = append(r.pq, persistOp{id: id, rec: rec})
	}
	spawn := !r.pbusy
	r.pbusy = true
	r.pmu.Unlock()
	if spawn {
		debuglog.Go(r.log, source, r.flushPersist)
	}
}

// flushPersist drains the persist queue serially (single flusher at a time preserves
// put/delete ordering per id).
func (r *Recorder) flushPersist() {
	defer func() { // on panic: release drainers before debuglog.Recover logs it
		if p := recover(); p != nil {
			r.pmu.Lock()
			r.pbusy = false
			r.pcond.Broadcast()
			r.pmu.Unlock()
			panic(p)
		}
	}()
	for {
		r.pmu.Lock()
		if len(r.pq) == 0 {
			r.pbusy = false
			r.pcond.Broadcast()
			r.pmu.Unlock()
			return
		}
		op := r.pq[0]
		r.pq = r.pq[1:]
		r.pmu.Unlock()
		var err error
		msg := "persist recording failed"
		if op.rec != nil {
			err = r.st.PutJSON(store.BucketRecordings, op.id, op.rec)
		} else {
			err = r.st.Delete(store.BucketRecordings, op.id)
			msg = "delete recording failed"
		}
		if err != nil {
			r.log.Warn(source, msg, map[string]any{"id": op.id, "error": err.Error()})
		} else {
			r.bumpRec()
		}
	}
}

// drainPersist blocks until every queued store write has flushed. Caller must NOT hold r.mu.
func (r *Recorder) drainPersist() {
	r.pmu.Lock()
	for r.pbusy || len(r.pq) > 0 {
		r.pcond.Wait()
	}
	r.pmu.Unlock()
}

func (r *Recorder) broadcastLocked() {
	snap := r.active.clone()
	r.subMu.Lock()
	chans := make([]chan *Recording, 0, len(r.subs))
	for _, ch := range r.subs {
		chans = append(chans, ch)
	}
	r.subMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- snap:
		default:
		}
	}
}

// ident is a track's stable identity for now-playing comparison.
func ident(np session.NowPlaying) string {
	title := session.StringField(np.Fields, session.FieldTitle)
	artist := session.StringField(np.Fields, session.FieldArtist)
	return strings.ToLower(strings.TrimSpace(title + "|" + artist))
}

func trackFrom(np session.NowPlaying) Track {
	bpm, _ := floatField(np.Fields, session.FieldBPM)
	t := Track{
		Title:  session.StringField(np.Fields, session.FieldTitle),
		Artist: session.StringField(np.Fields, session.FieldArtist),
		Album:  session.StringField(np.Fields, session.FieldAlbum),
		Key:    session.StringField(np.Fields, session.FieldKey),
		BPM:    bpm,
		Deck:   np.Deck,
		Path:   session.StringField(np.Fields, session.FieldPath),
	}
	if fv, ok := np.Fields[session.FieldTitle]; ok {
		t.TitleSource = fv.Source
	}
	return t
}

// faderActive reports whether any channel's fader sits above the on-air threshold (the mix is live).
func faderActive(st session.UnifiedState) bool {
	for _, ch := range st.Channels {
		if f, ok := floatField(ch, session.FieldFader); ok && f > session.OnAirFaderThreshold {
			return true
		}
	}
	return false
}

func floatField(m map[string]session.FieldValue, f string) (float64, bool) {
	if fv, ok := m[f]; ok {
		switch n := fv.Value.(type) {
		case float64:
			return n, true
		case int:
			return float64(n), true
		case int64:
			return float64(n), true
		}
	}
	return 0, false
}
