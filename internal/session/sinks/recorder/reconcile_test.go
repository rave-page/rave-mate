package recorder

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/store"
)

// resolveWindow stands in for the real per-track library resolve over a 30+ track set
// (tens-to-hundreds of ms). Long enough that an UNSERIALIZED Rename provably lands inside the
// reconciler's read-modify-write window rather than racing the test's own scheduling.
const resolveWindow = 250 * time.Millisecond

// seedFinished persists a finished recording (a reconcile target) straight to the store.
func seedFinished(t *testing.T, r *Recorder, id, name string, start, end time.Time) {
	t.Helper()
	rec := Recording{ID: id, Name: name, StartedAt: start, EndedAt: end}
	if err := r.st.PutJSON(store.BucketRecordings, id, &rec); err != nil {
		t.Fatal(err)
	}
}

// historySessions builds a Traktor history session whose plays sit inside [start, end].
func historySessions(start time.Time, paths ...string) []musiclib.Session {
	played := make([]musiclib.PlayedTrack, 0, len(paths))
	for i, p := range paths {
		played = append(played, musiclib.PlayedTrack{
			Path:      p,
			Deck:      i % 2,
			StartedAt: start.Add(time.Duration(i+1) * time.Minute),
			Title:     "History " + p,
			Artist:    "History Artist",
		})
	}
	return []musiclib.Session{{Name: "hist", Played: played, StartedAt: played[0].StartedAt}}
}

// TestRenameRacesSlowReconcile interleaves a Rename against a ReconcileWithSessions that is inside
// its Get→resolve→Put window. Both are DIRECT BucketRecordings read-modify-writes outside r.mu and
// outside the persist queue (drainPersist is irrelevant to the reconciler), so without storeMu
// serializing them the rename is silently reverted by the reconciler's write-back of the object it
// read pre-rename - the AutoReconciler fires this off a background fsnotify watcher over the
// Traktor dir, so the user just sees their rename undo itself.
func TestRenameRacesSlowReconcile(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	seedFinished(t, r, "rec_1", "Old name", base, base.Add(30*time.Minute))
	sessions := historySessions(base, "a.wav", "b.wav")

	// Signals the reconcile is mid-cycle (it has read the recording, not yet written it back).
	inWindow := make(chan struct{})
	var once sync.Once
	resolve := func(string) (HistoryMeta, bool) {
		once.Do(func() {
			close(inWindow)
			time.Sleep(resolveWindow)
		})
		return HistoryMeta{}, false // history metadata stands alone; a resolve miss is normal
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.ReconcileWithSessions("rec_1", sessions, resolve)
		done <- err
	}()

	<-inWindow
	if err := r.Rename("rec_1", "Closing Set"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got, ok := r.Get("rec_1")
	if !ok {
		t.Fatal("recording gone")
	}
	if got.Name != "Closing Set" {
		t.Fatalf("the rename was silently reverted by the reconciler: name = %q, want %q", got.Name, "Closing Set")
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("the reconciled tracklist was lost to the rename: %d tracks, want 2", len(got.Tracks))
	}
	if got.Tracks[0].Title != "History a.wav" {
		t.Fatalf("tracks are not the reconciled ones: %+v", got.Tracks)
	}
	if got.ReconciledAt.IsZero() {
		t.Fatal("the reconcile stamp was lost to the rename")
	}
	if got.EndedAt.IsZero() {
		t.Fatal("EndedAt was lost")
	}
}

// TestDeleteRacesSlowReconcile is TestRenameRacesSlowReconcile's sibling for the fourth direct
// BucketRecordings writer. The user deletes a set in the Publish tab while the AutoReconciler (fired
// off its fsnotify watcher over the Traktor history dir) is inside its Get→resolve→Put window for
// that same set. Unserialized, the store delete lands mid-window and the reconciler then writes the
// whole object it read pre-delete straight back: the deleted set REAPPEARS.
func TestDeleteRacesSlowReconcile(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	seedFinished(t, r, "rec_1", "Closing Set", base, base.Add(30*time.Minute))
	sessions := historySessions(base, "a.wav", "b.wav")

	inWindow := make(chan struct{})
	var once sync.Once
	resolve := func(string) (HistoryMeta, bool) {
		once.Do(func() {
			close(inWindow)
			time.Sleep(resolveWindow)
		})
		return HistoryMeta{}, false
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.ReconcileWithSessions("rec_1", sessions, resolve)
		done <- err
	}()

	<-inWindow // the reconcile now holds storeMu across its resolve
	v0 := r.RecordingsVersion()
	if err := r.Delete("rec_1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Delete queued behind the reconcile rather than corrupting it, so the reconcile still succeeds.
	if err := <-done; err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, ok := r.Get("rec_1"); ok {
		t.Fatalf("the deleted set was resurrected by the reconciler's write-back: %+v", got)
	}
	if r.RecordingsVersion() == v0 {
		t.Fatal("Delete must bump RecordingsVersion (the Publish list caches List() by it - the row would linger on screen)")
	}
}

// TestDeleteOfActiveRecordingIsRefused pins the guard against the FIFTH writer, the persist flusher.
// Draining under storeMu lands only what is queued so far; persistLocked re-queues r.active's id on
// every confirm/refresh, and the flusher is deliberately not a storeMu participant - so a delete of
// the LIVE set would report success and then be written straight back by the next tick. Both
// renderers gate their Delete button on EndedAt != 0; remotectl's RecDelete does not, which is the
// path this closes. Revert-check: drop the active branch in Delete and the refusal assert fails.
func TestDeleteOfActiveRecordingIsRefused(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	r.clock = func() time.Time { return base }
	live := r.StartRecording("Live", "")

	if err := r.Delete(live.ID); err == nil {
		t.Fatal("Delete of the ACTIVE recording must be refused - the persist queue would resurrect it")
	}
	// Drive the state machine past confirm so persistLocked re-queues this id: the write-back that
	// would have resurrected a "successful" delete.
	ds := deckState("A", "Track A", "Artist A", true)
	for i := range 60 {
		r.step(base.Add(time.Duration(i)*time.Second), ds)
	}
	r.drainPersist()
	if _, ok := r.Get(live.ID); !ok {
		t.Fatal("the active recording must survive a refused delete")
	}
}

// TestDeleteAfterStopStaysDeleted covers Delete vs active-stop: StopRecording queues its final
// snapshot and only then drains, and autoFinalizeLocked/confirmCurrent queue without draining at all,
// so a finished id can still have a put in flight (the flusher pops an op before writing it, so it is
// invisible to a pq scan). A delete that removed the key BEFORE that put landed would see it written
// back. Delete drains first, so the snapshot lands and is then deleted - and nothing re-queues a
// non-active id.
func TestDeleteAfterStopStaysDeleted(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	r.clock = func() time.Time { return base }
	r.StartRecording("Live", "")
	ds := deckState("A", "Track A", "Artist A", true)
	for i := range 60 {
		r.step(base.Add(time.Duration(i)*time.Second), ds)
	}
	done := r.StopRecording()
	if done == nil {
		t.Fatal("no active recording to stop")
	}
	if err := r.Delete(done.ID); err != nil {
		t.Fatalf("delete after stop: %v", err)
	}
	r.drainPersist() // let any straggling queued write land - it must not resurrect the row
	if got, ok := r.Get(done.ID); ok {
		t.Fatalf("a queued snapshot resurrected the deleted set: %+v", got)
	}
	for _, rec := range r.List() {
		if rec.ID == done.ID {
			t.Fatal("the deleted set is still in List() - it would render in the Publish tab")
		}
	}
}

// TestStoreWritersConcurrentNoDeadlock is a LOCK-ORDER guard, not a data-loss repro (it passes with
// storeMu removed - see below). It runs every storeMu holder concurrently with r.mu/persist-queue
// traffic from the state machine, so the declared order (storeMu → r.mu → r.pmu) is exercised in
// both directions: anyone later taking storeMu under r.mu, or having the flusher take storeMu,
// hangs this test instead of deadlocking the daemon. drainPersist() under storeMu is the sharp edge.
//
// Why sweepStale has no data-loss repro: it writes ONLY open recordings (EndedAt zero), while
// ReconcileWithSessions rejects in-progress ones, so those two can never contend on one id. Its one
// real race is against Rename on a crash-orphaned open recording, and sweepStale is a tight
// List→Put loop at startup with no injectable window to make that deterministic. It takes storeMu
// because it IS a direct read-modify-write writer, not because a test pins it.
func TestStoreWritersConcurrentNoDeadlock(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "rec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := New(logbus.New(64), st, nil, 10)

	base := time.Unix(1_700_000_000, 0)
	seedFinished(t, r, "rec_1", "One", base, base.Add(30*time.Minute))
	seedFinished(t, r, "rec_2", "Two", base, base.Add(30*time.Minute))
	seedFinished(t, r, "rec_4", "Four", base, base.Add(30*time.Minute)) // deleted concurrently below
	// Open (EndedAt zero) + non-active ⇒ sweepStale finalizes it.
	open := Recording{ID: "rec_3", Name: "Three", StartedAt: base, Tracks: []Track{{Title: "T", StartedAt: base}}}
	if err := st.PutJSON(store.BucketRecordings, "rec_3", &open); err != nil {
		t.Fatal(err)
	}
	sessions := historySessions(base, "a.wav", "b.wav")
	// Hold storeMu across a slow window so the r.mu traffic below genuinely overlaps it.
	resolve := func(string) (HistoryMeta, bool) {
		time.Sleep(time.Millisecond)
		return HistoryMeta{}, false
	}

	var wg sync.WaitGroup
	// r.mu + persist-queue traffic: step() takes r.mu and queues store writes, so the flusher runs
	// under the storeMu holders' drainPersist(). An inverted order surfaces here as a hang.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ds := deckState("A", "Track A", "Artist A", true)
		for i := range 200 {
			r.step(base.Add(time.Duration(i)*time.Second), ds)
			r.Active()
			r.Pending()
		}
	}()
	for _, id := range []string{"rec_1", "rec_2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.ReconcileWithSessions(id, sessions, resolve); err != nil {
				t.Errorf("reconcile %s: %v", id, err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.sweepStale()
		if err := r.Rename("rec_1", "One renamed"); err != nil {
			t.Errorf("rename: %v", err)
		}
	}()
	// Delete is the fourth storeMu holder + the only one that drains then deletes: an inverted order
	// (or the flusher ever needing storeMu) hangs here rather than in the daemon.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := r.Delete("rec_4"); err != nil {
			t.Errorf("delete: %v", err)
		}
	}()
	wg.Wait()

	if _, ok := r.Get("rec_4"); ok {
		t.Fatal("rec_4 came back: a concurrent storeMu writer resurrected the deleted set")
	}

	for _, id := range []string{"rec_1", "rec_2"} {
		got, ok := r.Get(id)
		if !ok || len(got.Tracks) != 2 || got.ReconciledAt.IsZero() {
			t.Fatalf("%s lost its reconcile: %+v ok=%v", id, got, ok)
		}
	}
	got, ok := r.Get("rec_3")
	if !ok || got.EndedAt.IsZero() {
		t.Fatalf("rec_3 lost its sweepStale finalize: %+v ok=%v", got, ok)
	}
}
