package webui

// Phase B4b gates for the Library retained-state derivations (library_deriv.go).
//
// Three things have to hold, and each has its own test here:
//  1. INVALIDATION IS COMPLETE. The old memos hashed every input, so a forgotten input was a
//     wrong hash; the new ones key on ctlVer, so a forgotten input is a missing ctlTouch. The
//     differential gate drives EVERY collection-control action through the real dispatcher and
//     asserts the retained view still equals a fresh computation - a missed bump fails it.
//  2. THE WORK IS OFF THE HANDLER LANE. The runner seam (libSt.derivRun) queues instead of
//     spawning, so a test can prove the lane returned WITHOUT computing.
//  3. FRESHNESS IS CHANGE-DRIVEN, NOT TIME-DRIVEN. The old caches served up to 2s/5s-stale
//     filesystem data and re-did the full work every TTL; the new ones react to a dir mtime /
//     version move on the next render and do nothing at all when nothing moved.
//
// Plus a DOM-identity gate: a body rendered from retained state must be byte-identical to the same
// body rendered with every derivation dropped (cold = fresh inline compute).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/ui"
)

// ── harness ──

// derivQ is the deterministic replacement for u.bg: work is queued and drained at a known point,
// so "the lane did not compute this" is observable.
type derivQ struct {
	fns []func()
}

func (q *derivQ) push(fn func()) { q.fns = append(q.fns, fn) }

// drain runs queued work until quiet (a settle may queue more, e.g. the patch's own refresh).
func (q *derivQ) drain() int {
	n := 0
	for i := 0; i < 64 && len(q.fns) > 0; i++ {
		batch := q.fns
		q.fns = nil
		for _, fn := range batch {
			fn()
			n++
		}
	}
	return n
}

// newLibTestUI builds a UI with a real libdb + a virtual shell, and a queued deriv runner.
func newLibTestUI(t testing.TB) (*UI, *libSt, *libdb.DB, *derivQ) {
	t.Helper()
	u, s, db, q, _ := newLibTestUIAt(t)
	return u, s, db, q
}

// newLibTestUIAt also returns the DB file path (the bench opens a second handle to it to time the
// pre-B4b LibraryVersion query).
func newLibTestUIAt(t testing.TB) (*UI, *libSt, *libdb.DB, *derivQ, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lib.db")
	db, err := libdb.Open(path)
	if err != nil {
		t.Fatalf("libdb open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetNodeID("node-test")
	u := &UI{svc: ui.Services{Cfg: &config.Config{}, Lib: db}, active: "library", libSection: "collection",
		stop: make(chan struct{}), evalKick: make(chan struct{}, 1)}
	u.shell = newVirtualShell(nil, func(string) {}, func(string) {})
	t.Cleanup(func() { u.shell.terminate(); releaseUIState(u) })
	q := &derivQ{}
	s := u.lib()
	s.mu.Lock()
	s.derivRun = q.push
	s.mu.Unlock()
	return u, s, db, q, path
}

// libTestTracks seeds s.tracks the way the hydrate completion does (wholesale replace + ctlTouch).
func libTestTracks(s *libSt, n int) {
	tr := make([]musiclib.Track, 0, n)
	genres := []string{"Techno", "House", "Drum & Bass"}
	labels := []string{"Ostgut", "Hessle", "Metalheadz"}
	// 5 keys x 3 genres x 2 labels: every facet combination the gates drive stays non-empty
	keys := []string{"8A", "1B", "12A", "5A", "7B"}
	for i := 0; i < n; i++ {
		tr = append(tr, musiclib.Track{
			Path:   fmt.Sprintf("C:\\m\\t%03d.flac", i),
			Artist: fmt.Sprintf("Artist %02d", i%17),
			Title:  fmt.Sprintf("Track %03d", i),
			Genre:  genres[i%3], Label: labels[i%2], Key: keys[i%5],
			BPM: float64(120 + i%20), DurationSec: float64(200 + i),
		})
	}
	s.mu.Lock()
	s.tracks, s.byPath = tr, map[string]musiclib.Track{}
	for _, t := range tr {
		s.byPath[t.Path] = t
	}
	s.loaded = true
	s.ctlTouch()
	s.mu.Unlock()
}

// collViewFresh computes the view from scratch, ignoring everything retained.
func collViewFresh(s *libSt) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collView()
}

func sameIdx(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func retainedView(u *UI, s *libSt) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return u.libCollView(s)
}

// ── 1. invalidation completeness ──

// TestLibCollViewRetainedMatchesFreshAfterEveryControlAction is the missed-invalidation gate. Every
// act that can move a collView input is driven through the REAL dispatcher; after each the retained
// view must equal a from-scratch computation. Dropping a ctlTouch (or adding a control that does not
// route through libSetColl) fails this - verified by execution: removing the ctlTouch from
// libSearchDebounced makes the "coll-search" step fail.
func TestLibCollViewRetainedMatchesFreshAfterEveryControlAction(t *testing.T) {
	u, s, db, q := newLibTestUI(t)
	libTestTracks(s, 400)
	if _, err := db.CreatePlaylist("Warmup", "manual", ""); err != nil {
		t.Fatalf("playlist: %v", err)
	}
	rows, err := db.ListPlaylists()
	if err != nil || len(rows) != 1 {
		t.Fatalf("list playlists: %v %+v", err, rows)
	}
	plID := rows[0].ID
	if _, err := db.AddToPlaylist(plID, "C:\\m\\t000.flac", "C:\\m\\t003.flac"); err != nil {
		t.Fatalf("add tracks: %v", err)
	}

	steps := []struct {
		name string
		m    actMsg
	}{
		{"coll-search", actMsg{Act: "lib-coll-search", Val: "track 01"}},
		{"coll-search-clear", actMsg{Act: "lib-coll-search", Val: ""}},
		{"coll-sort", actMsg{Act: "lib-coll-sort:BPM"}},
		{"coll-dir", actMsg{Act: "lib-coll-dir"}},
		{"coll-hsort-same", actMsg{Act: "lib-coll-hsort:BPM"}},
		{"coll-hsort-other", actMsg{Act: "lib-coll-hsort:Key"}},
		{"nodrops-on", actMsg{Act: "lib-nodrops"}},
		{"nodrops-off", actMsg{Act: "lib-nodrops"}},
		{"genre-on", actMsg{Act: "lib-genre:Techno"}},
		{"genre-second", actMsg{Act: "lib-genre:House"}},
		{"genre-off", actMsg{Act: "lib-genre:Techno"}},
		{"label-on", actMsg{Act: "lib-label:Ostgut"}},
		{"key-on", actMsg{Act: "lib-key:8A"}},
		{"key-clear", actMsg{Act: "lib-key-clear"}},
		{"clear-1", actMsg{Act: "lib-clearfilters"}},
		{"plfilter-on", actMsg{Act: fmt.Sprintf("lib-plfilter:%d", plID)}},
		{"plfilter-off", actMsg{Act: fmt.Sprintf("lib-plfilter:%d", plID)}},
		{"plgoto", actMsg{Act: fmt.Sprintf("lib-plgoto:%d", plID)}},
		{"clear-2", actMsg{Act: "lib-clearfilters"}},
		{"key-harmonic", actMsg{Act: "lib-key-harmonic:8A"}},
		{"clear-3", actMsg{Act: "lib-clearfilters"}},
		{"select-row", actMsg{Act: "lib-collsel:C:\\m\\t001.flac", Val: "true"}}, // NOT a view input
		{"sort-again", actMsg{Act: "lib-coll-sort:Artist"}},
	}
	for _, st := range steps {
		if !u.dispatch(st.m) {
			t.Fatalf("%s: no handler for %q", st.name, st.m.Act)
		}
		// the debounced search timer fires on wall time; prime the same way it does
		if st.m.Act == "lib-coll-search" {
			u.libPrimeColl(nil)
		}
		q.drain()
		got, want := retainedView(u, s), collViewFresh(s)
		if !sameIdx(got, want) {
			t.Fatalf("%s: retained view (%d rows) != fresh view (%d rows) - a ctlTouch is missing",
				st.name, len(got), len(want))
		}
		if len(want) == 0 {
			t.Fatalf("%s: fixture filtered everything away - the step proves nothing", st.name)
		}
		_ = u.drainEvals()
	}
}

// TestLibCollViewKeyIsPreciseNotBlanket: a selection-only action must NOT invalidate the view (the
// memo existed because selection re-renders re-filtered 23k tracks), and a control action must.
func TestLibCollViewKeyIsPreciseNotBlanket(t *testing.T) {
	u, s, _, q := newLibTestUI(t)
	libTestTracks(s, 200)
	_ = retainedView(u, s)
	q.drain()
	before := s.derivN

	u.dispatch(actMsg{Act: "lib-collsel:C:\\m\\t001.flac", Val: "true"})
	q.drain()
	_ = retainedView(u, s)
	q.drain()
	if s.derivN != before {
		t.Fatalf("selection recomputed the view (%d -> %d settles)", before, s.derivN)
	}
	u.dispatch(actMsg{Act: "lib-coll-sort:BPM"})
	q.drain()
	if s.derivN == before {
		t.Fatal("sort did not recompute the view")
	}
}

// ── 2. off the handler lane ──

// TestLibCollViewComputesOffTheLane: with the runner queued (nothing executed), a control action
// must leave the lane with the OLD retained view and the recompute merely enqueued. This is the
// actWorker constraint expressed as a test: the ~23k filter+sort may not run on the caller.
func TestLibCollViewComputesOffTheLane(t *testing.T) {
	u, s, _, q := newLibTestUI(t)
	libTestTracks(s, 300)
	_ = retainedView(u, s) // cold fill (inline by design: DOM parity)
	q.drain()
	all := retainedView(u, s)
	if len(all) != 300 {
		t.Fatalf("cold view = %d rows, want 300", len(all))
	}

	// a facet toggle: one third of the fixture is Techno
	u.dispatch(actMsg{Act: "lib-genre:Techno"})
	if len(q.fns) == 0 {
		t.Fatal("control action did not enqueue an off-lane recompute")
	}
	if got := retainedView(u, s); len(got) != 300 {
		t.Fatalf("the lane recomputed: %d rows before the queue was drained", len(got))
	}
	q.drain()
	got := retainedView(u, s)
	if len(got) == 0 || len(got) == 300 {
		t.Fatalf("after draining: %d rows, want the Techno subset", len(got))
	}
	if want := collViewFresh(s); !sameIdx(got, want) {
		t.Fatalf("off-lane result %d rows != fresh %d rows", len(got), len(want))
	}
}

// TestLibPlaylistsRefreshesOffTheLane: a version move must not put ListPlaylists (a per-row COUNT
// subquery) back on the lane - the lane serves last-known and the refresh is queued.
func TestLibPlaylistsRefreshesOffTheLane(t *testing.T) {
	u, s, db, q := newLibTestUI(t)
	if _, err := db.CreatePlaylist("One", "manual", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	s.mu.Lock()
	rows := u.libPlaylists(s) // cold: inline, as the old path was
	s.mu.Unlock()
	if len(rows) != 1 {
		t.Fatalf("cold rows = %d, want 1", len(rows))
	}
	if _, err := db.CreatePlaylist("Two", "manual", ""); err != nil {
		t.Fatalf("create 2: %v", err)
	}
	s.mu.Lock()
	rows = u.libPlaylists(s)
	s.mu.Unlock()
	if len(rows) != 1 {
		t.Fatalf("lane re-queried the DB: %d rows before the refresh ran", len(rows))
	}
	if len(q.fns) == 0 {
		t.Fatal("version move did not enqueue a refresh")
	}
	q.drain()
	s.mu.Lock()
	rows = u.libPlaylists(s)
	s.mu.Unlock()
	if len(rows) != 2 {
		t.Fatalf("after the refresh: %d rows, want 2", len(rows))
	}
}

// ── 3. freshness is change-driven, not time-driven ──

// TestOnDiskFreshnessIsChangeDrivenNotTTL: the old cache served an existence verdict for up to
// collOnDiskFresh (5s) and then re-stat'ed EVERY row unconditionally. The new sweep re-stats only
// when a parent dir moved (or a row is unknown) - and it reacts on the very next render, with no
// wall-clock wait anywhere in the path.
func TestOnDiskFreshnessIsChangeDrivenNotTTL(t *testing.T) {
	u, s, _, q := newLibTestUI(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.flac")
	if err := os.WriteFile(a, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	paths := []string{a, filepath.Join(dir, "gone.flac")}

	sweep := func() {
		s.mu.Lock()
		u.libEnsureOnDisk(s, paths)
		s.mu.Unlock()
		q.drain()
	}
	onDisk := func(p string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.onDiskOr(p)
	}

	sweep()
	if !onDisk(a) || onDisk(paths[1]) {
		t.Fatalf("first sweep wrong: a=%v gone=%v", onDisk(a), onDisk(paths[1]))
	}
	sweeps := s.onDiskSweeps
	if sweeps == 0 {
		t.Fatal("first sweep did not stat anything")
	}
	// nothing moved: the sweep must do NO file stats at all (the TTL used to redo all of them)
	sweep()
	sweep()
	if s.onDiskSweeps != sweeps {
		t.Fatalf("unchanged dir re-stat'ed the rows (%d -> %d sweeps)", sweeps, s.onDiskSweeps)
	}
	// delete the file: the dir mtime moves, so the very next render's sweep sees it - immediately,
	// not after a 5s TTL.
	if err := os.Remove(a); err != nil {
		t.Fatalf("remove: %v", err)
	}
	touchDir(t, dir)
	t0 := time.Now()
	sweep()
	if onDisk(a) {
		t.Fatal("deleted file still reads as present - the change gate missed the dir mtime move")
	}
	if el := time.Since(t0); el > time.Second {
		t.Fatalf("freshness took %v - that is a wall-clock wait, not a change gate", el)
	}
	if s.onDiskSweeps == sweeps {
		t.Fatal("dir moved but no re-stat happened")
	}
}

// TestBrowseFreshnessIsChangeDrivenNotTTL: same for the browse listing, whose 2s TTL re-ran
// os.ReadDir + one Info per entry on a timer. Now: one dir stat per render, a full read only when
// the dir moved.
func TestBrowseFreshnessIsChangeDrivenNotTTL(t *testing.T) {
	u, s, _, q := newLibTestUI(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "one.flac"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	read := func() (int, bool) {
		s.mu.Lock()
		fes, _, ok := u.libBrowseEntries(s, dir)
		n := len(fes)
		s.mu.Unlock()
		q.drain()
		return n, ok
	}
	if _, ok := read(); ok {
		t.Fatal("first call must report not-cached (loading placeholder)")
	}
	n, ok := read()
	if !ok || n != 1 {
		t.Fatalf("cached listing = %d entries ok=%v, want 1 true", n, ok)
	}
	reads := s.browseReads
	read()
	read()
	if s.browseReads != reads {
		t.Fatalf("unchanged dir re-read the listing (%d -> %d reads)", reads, s.browseReads)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.flac"), []byte("y"), 0o600); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	touchDir(t, dir)
	t0 := time.Now()
	read()                     // this render's bg job sees the moved mtime and re-reads
	if n, _ = read(); n != 2 { // the next render serves it - no wall-clock wait in between
		t.Fatalf("new file: %d entries, want 2 (the dir-mtime gate missed it)", n)
	}
	if el := time.Since(t0); el > time.Second {
		t.Fatalf("freshness took %v - that is the TTL, not a change gate", el)
	}
	if s.browseReads == reads {
		t.Fatal("dir moved but no re-read happened")
	}
}

// touchDir forces an observable mtime move on dir (filesystem mtime granularity is coarse enough
// that a create inside a fresh TempDir can land in the same tick as the previous stat).
func touchDir(t testing.TB, dir string) {
	t.Helper()
	now := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(dir, now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// TestLibSmartCountsFreshAfterRulesEdit: a smart-playlist rules edit bumps PlaylistVersion, which
// must move the counts key. The old path hashed every rule set per render to notice; the new one
// keys on the epoch. Cold stays ASYNC (the "…" placeholder is the established DOM).
func TestLibSmartCountsFreshAfterRulesEdit(t *testing.T) {
	u, s, db, q := newLibTestUI(t)
	libTestTracks(s, 60) // 20 Techno / 20 House / 20 D&B
	id, err := db.CreatePlaylist("Smart", "smart", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.SetSmartRules(id, `{"bpmMin":130}`); err != nil {
		t.Fatalf("rules: %v", err)
	}
	counts := func() map[int64]int {
		s.mu.Lock()
		rows := u.libPlaylists(s)
		c := u.libSmartCounts(s, rows)
		s.mu.Unlock()
		q.drain()
		return c
	}
	if got := counts(); len(got) != 0 {
		t.Fatalf("cold counts = %v, want the async placeholder (empty)", got)
	}
	first := counts()
	if first[id] == 0 {
		t.Fatalf("counts after settle = %v, want a non-zero BPM-rule count", first)
	}
	if err := db.SetSmartRules(id, `{"bpmMin":125}`); err != nil {
		t.Fatalf("rules 2: %v", err)
	}
	counts() // serves last-known + kicks the refresh
	second := counts()
	if second[id] <= first[id] {
		t.Fatalf("widened rules: %d not > %d - the epoch key missed the edit", second[id], first[id])
	}
}

// TestLibDerivFreshAfterLibraryVersionBump: an external library write (peer edit, import) moves
// LibraryVersion, and the very next render must refresh - no TTL, no polling interval.
func TestLibDerivFreshAfterLibraryVersionBump(t *testing.T) {
	u, s, db, q := newLibTestUI(t)
	libTestTracks(s, 100)
	_ = retainedView(u, s)
	q.drain()
	settles := s.derivN

	if err := db.AppendChanges([]libdb.ChangeEvent{
		{TrackHash: "h1", Field: "rating", Op: "set", NewValue: "5", Origin: "peer"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = retainedView(u, s) // serves last-known, kicks the refresh
	if len(q.fns) == 0 {
		t.Fatal("LibraryVersion moved but no refresh was kicked")
	}
	q.drain()
	if s.derivN <= settles {
		t.Fatal("refresh did not settle")
	}
	if got, want := retainedView(u, s), collViewFresh(s); !sameIdx(got, want) {
		t.Fatalf("post-bump view %d rows != fresh %d rows", len(got), len(want))
	}
}

// ── DOM identity ──

// TestLibBodyFromRetainedStateIsByteIdentical: the whole #lib-body rendered off retained state must
// equal the same body rendered with every derivation dropped (cold path = fresh inline compute).
// Retained state can therefore never serve markup a from-scratch render would not produce.
func TestLibBodyFromRetainedStateIsByteIdentical(t *testing.T) {
	u, s, db, q := newLibTestUI(t)
	libTestTracks(s, 250)
	if _, err := db.CreatePlaylist("Warmup", "manual", ""); err != nil {
		t.Fatalf("playlist: %v", err)
	}
	rows, _ := db.ListPlaylists()
	plID := rows[0].ID
	if _, err := db.AddToPlaylist(plID, "C:\\m\\t002.flac", "C:\\m\\t005.flac"); err != nil {
		t.Fatalf("add: %v", err)
	}

	script := []actMsg{
		{Act: "lib-coll-sort:BPM"},
		{Act: "lib-coll-dir"},
		{Act: "lib-genre:Techno"},
		{Act: "lib-label:Ostgut"},
		{Act: "lib-key:8A"},
		{Act: "lib-key-clear"},
		{Act: fmt.Sprintf("lib-plfilter:%d", plID)},
		{Act: "lib-collsel:C:\\m\\t002.flac", Val: "true"},
		{Act: "lib-nodrops"},
		{Act: "lib-clearfilters"},
		{Act: "lib-coll-hsort:Key"},
	}
	for i, m := range script {
		if !u.dispatch(m) {
			t.Fatalf("step %d: no handler for %q", i, m.Act)
		}
		q.drain()
		_ = u.drainEvals()

		retained := u.libBody()
		s.mu.Lock() // drop everything retained: the next render must compute from scratch
		s.collD, s.plD, s.smartD = libDeriv[[]int]{}, libDeriv[[]libdb.PlaylistRow]{}, libDeriv[map[int64]int]{}
		s.mu.Unlock()
		q.drain()
		cold := u.libBody()
		if retained != cold {
			t.Fatalf("step %d (%s): retained body != cold body\nretained %d B\ncold     %d B\nfirst diff at %d",
				i, m.Act, len(retained), len(cold), firstDiff(retained, cold))
		}
		if !strings.Contains(retained, "trk-row") && !strings.Contains(retained, "empty") {
			t.Fatalf("step %d: body rendered neither rows nor an empty state - the gate is vacuous", i)
		}
		q.drain()
		_ = u.drainEvals()
	}
}

func firstDiff(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
