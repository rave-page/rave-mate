package webui

// Phase B4b - retained-state derivations for the Library tab.
//
// Replaced: four render-time caches (collViewSig/collViewIdx, plRowsVer/plRows, smartCounts*,
// onDiskCk + its 5s TTL, plus the browse listing's 2s TTL). Each was a Go-runtime workaround -
// the handler lane could not afford the real work, so per render it
//   - hashed every input (fmt.Fprintf + sort.Strings + FNV over three filter maps, the playlist
//     id set and every smart rule set) just to decide whether to recompute,
//   - asked SQLite for LibraryVersion TWICE (SELECT MAX(seq) on a SetMaxOpenConns(1) handle: it
//     queues behind any writer),
//   - recomputed the ~23k-track filtered view INLINE whenever a hash moved, and
//   - bounded the filesystem work with wall-clock TTLs, serving up to 2s/5s-stale data.
//
// Now: retained state + a COMPARABLE key. The lane compares libDerivKey structs (no hash, no
// alloc, no SQL) and reads what is retained; every computation runs off the lane via u.bg.
// Freshness is change-driven, never time-driven:
//   - control mutations (libSetColl) recompute off-lane and patch ONCE, with the fresh view - the
//     mutation path never renders a stale frame,
//   - a libdb epoch move (peer edit, import, another UI) moves the key: the next render serves
//     last-known and kicks the refresh immediately, which patches when it lands,
//   - filesystem-derived state (on-disk existence, browse listing) is gated on DIRECTORY MTIME
//     read off-lane: one stat per distinct dir per render instead of N stats / a full ReadDir
//     every 2-5s, and fresh within one render instead of within the TTL.
//
// HARD CONSTRAINT (memory: never block the webui actWorker): render + handlers run on ONE
// serialized goroutine. Nothing here may do blocking I/O or a per-render scan on that lane. The
// only inline computation left is a COLD first fill (see libDerive): the old path blocked there
// too, and rendering a placeholder instead would be a DOM change - B4 changes timing, not markup.
//
// No Zig-side state: the B3 stateless-export design (ZIG_UI_GUIDE "hash-return, NOT a Zig-side
// cache") holds, so these caches stay Go-side and version-keyed. No state struct changed shape,
// so the wire schema is untouched.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// ── keys ──

// libEpoch is the libdb epoch triple every Library derivation keys on. All three reads are atomic
// loads now (LibraryVersion mirrors the change_log epoch in memory since B4b).
type libEpoch struct{ lib, pl, compat int64 }

func (u *UI) libEpoch() libEpoch {
	if u.svc.Lib == nil {
		return libEpoch{}
	}
	return libEpoch{u.svc.Lib.LibraryVersion(), u.svc.Lib.PlaylistVersion(), u.svc.Lib.CompatVersion()}
}

// libDerivKey identifies one derivation's inputs. Comparable on purpose: struct equality on the
// lane replaces the FNV signatures. Each derivation fills only the fields it actually reads, so a
// keystroke does not invalidate the playlist rows and a playlist write does not re-filter 23k
// tracks for nothing.
type libDerivKey struct {
	lib, pl, compat int64  // libdb epochs
	loadGen         int    // hydrate generation (a reload replaces s.tracks wholesale)
	ctl             uint64 // collection control generation (search/sort/facets/drops)
}

// ── the engine ──

// libDeriv is one retained derivation: the value, the key it was computed for, and whether a
// refresh is in flight. Guarded by libSt.mu.
type libDeriv[T any] struct {
	val  T
	key  libDerivKey
	warm bool
	busy bool
}

// libDerive returns d's retained value for key k, computing OFF the handler lane.
//
//	key unchanged      -> the retained value; zero work on the lane.
//	key moved, warm     -> last-known now, one bg refresh, patch when it settles.
//	key moved, cold     -> inline unless coldAsync (see the file header on DOM parity).
//
// compute() must not touch s: it runs on another goroutine, so callers pass a snapshot. after()
// runs after a bg settle (the DOM patch). Caller holds s.mu.
func libDerive[T any](u *UI, s *libSt, d *libDeriv[T], k libDerivKey, coldAsync bool, compute func() T, after func()) T {
	if d.warm && d.key == k {
		return d.val
	}
	if !d.warm && !coldAsync {
		d.val, d.key, d.warm = compute(), k, true
		return d.val
	}
	if d.busy {
		return d.val // in flight; it stamps the key it was computed for, so the next render re-kicks
	}
	d.busy = true
	u.libRun(s, func() {
		v := compute()
		s.mu.Lock()
		d.val, d.key, d.warm, d.busy = v, k, true, false
		s.derivSettled()
		s.mu.Unlock()
		if after != nil && !u.stopped() {
			after()
		}
	})
	return d.val
}

// libRun runs fn off the handler lane. s.derivRun is a test seam (a queue drained at a known
// point); production always spawns.
func (u *UI) libRun(s *libSt, fn func()) {
	if s.derivRun != nil {
		s.derivRun(fn)
		return
	}
	u.bg(fn)
}

// derivSettled records a settled derivation and wakes derivWait. Caller holds s.mu.
func (s *libSt) derivSettled() {
	s.derivN++
	if s.derivCh != nil {
		close(s.derivCh)
		s.derivCh = nil
	}
}

// derivWait returns a channel closed on the next settle (tests; nil-safe under s.mu).
func (s *libSt) derivWait() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.derivCh == nil {
		s.derivCh = make(chan struct{})
	}
	return s.derivCh
}

// ── collection controls: copy-on-write + one generation stamp ──

// ctlTouch stamps "a collection control moved". Every mutation of a collView input goes through
// libSetColl / cowToggle / this, which is what makes invalidation STRUCTURAL instead of a hash of
// the inputs that has to be kept in sync by hand. Caller holds s.mu.
func (s *libSt) ctlTouch() { s.ctlVer++ }

// cowToggle returns m with key set/cleared, as a NEW map. The filter maps are read off the lane by
// collViewOf, so they are copy-on-write: an in-place write would race the derivation (and the lane
// would have to copy them per render to avoid it).
func cowToggle(m map[string]bool, key string, on bool) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	for k := range m {
		out[k] = true
	}
	if on {
		out[key] = true
	} else {
		delete(out, key)
	}
	return out
}

// cowTogglePl is cowToggle for the playlist-facet id set.
func cowTogglePl(m map[int64]bool, id int64, on bool) map[int64]bool {
	out := make(map[int64]bool, len(m)+1)
	for k := range m {
		out[k] = true
	}
	if on {
		out[id] = true
	} else {
		delete(out, id)
	}
	return out
}

// cowDrops returns s.dropsIdx with path's markers replaced (empty = removed), as a NEW map - same
// copy-on-write rule as the filter maps (collViewOf reads it off the lane). Caller holds s.mu.
func (s *libSt) cowDrops(path string, drops []float64) {
	out := make(map[string][]float64, len(s.dropsIdx)+1)
	for k, v := range s.dropsIdx {
		out[k] = v
	}
	if len(drops) == 0 {
		delete(out, path)
	} else {
		out[path] = append([]float64(nil), drops...)
	}
	s.dropsIdx = out
	s.ctlTouch() // collNoDrops filters on it
}

// ── collection view ──

// libCollInputs is the snapshot collViewOf runs on. References only: every map in it is
// copy-on-write and s.tracks is replaced wholesale, so building this is O(1) on the lane and the
// off-lane read can never observe a torn write.
type libCollInputs struct {
	tracks  []musiclib.Track
	search  string
	sortBy  string
	desc    bool
	noDrops bool
	keySel  map[string]bool
	genre   map[string]bool
	label   map[string]bool
	plIDs   map[int64]bool
	plSet   map[string]bool
	drops   map[string][]float64
}

// collInputs snapshots everything collViewOf reads. Caller holds s.mu.
func (s *libSt) collInputs() libCollInputs {
	return libCollInputs{
		tracks: s.tracks, search: s.collSearch, sortBy: s.collSort, desc: s.collDesc,
		noDrops: s.collNoDrops, keySel: s.keySel, genre: s.collGenre, label: s.collLabel,
		plIDs: s.collPl, plSet: s.collPlSet, drops: s.dropsIdx,
	}
}

// libCollKey is the collection view's key: the three epochs (collPlSet is a pure function of the
// playlist + compat state), the hydrate generation and the control generation. Caller holds s.mu.
func (u *UI) libCollKey(s *libSt) libDerivKey {
	ep := u.libEpoch()
	return libDerivKey{lib: ep.lib, pl: ep.pl, compat: ep.compat, loadGen: s.loadGen, ctl: s.ctlVer}
}

// libPrimeCollSync recomputes the collection view for the CURRENT inputs. Must NOT run on the
// handler lane - callers are already off it (libSetColl, the hydrate completion).
func (u *UI) libPrimeCollSync(s *libSt) {
	s.mu.Lock()
	k, in := u.libCollKey(s), s.collInputs()
	warm := s.collD.warm && s.collD.key == k
	s.mu.Unlock()
	if warm {
		return
	}
	idx := collViewOf(in)
	s.mu.Lock()
	if !s.collD.busy { // a bg refresh in flight owns the slot; it stamps its own key
		s.collD.val, s.collD.key, s.collD.warm = idx, k, true
		s.derivSettled()
	}
	s.mu.Unlock()
}

// libPrimeColl primes the view off the handler lane, then runs then() (also off-lane).
func (u *UI) libPrimeColl(then func()) {
	s := u.lib()
	u.libRun(s, func() {
		u.libPrimeCollSync(s)
		if then != nil && !u.stopped() {
			then()
		}
	})
}

// ── filesystem-derived state: dir-mtime gated, no TTL ──

// libDirStamp is one directory's mtime as last observed off the lane.
type libDirStamp struct {
	mod time.Time
	ok  bool
}

// dirStamps stats dirs and reports them. Off-lane only.
func dirStamps(dirs []string) map[string]libDirStamp {
	out := make(map[string]libDirStamp, len(dirs))
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil {
			out[d] = libDirStamp{mod: fi.ModTime(), ok: true}
		} else {
			out[d] = libDirStamp{}
		}
	}
	return out
}

// parentDirs returns the distinct parent directories of paths, in first-seen order.
func parentDirs(paths []string) []string {
	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)
	for _, p := range paths {
		d := filepath.Dir(p)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// libEnsureOnDisk keeps the on-disk existence map for the rendered rows warm, entirely off the
// handler lane and WITHOUT a TTL: the sweep first stats the DISTINCT PARENT DIRS of the rows and
// re-stats the files only when a dir mtime moved (any create/delete/rename inside it) or a row is
// unknown. That is the change the old 5s TTL was sampling for - now caught within one render, for
// k dir stats instead of N file stats every 5s. Render reads s.onDiskOr; an unknown path reads as
// present until the sweep lands (unchanged). Caller holds s.mu.
func (u *UI) libEnsureOnDisk(s *libSt, paths []string) {
	if s.onDiskGen != s.loadGen { // library reloaded: prior results may be stale (files moved)
		s.onDiskGen = s.loadGen
		s.onDiskCk, s.dirMod = nil, nil
	}
	if s.onDiskBusy || len(paths) == 0 {
		return
	}
	unknown := false
	for _, p := range paths {
		if _, ok := s.onDiskCk[p]; !ok {
			unknown = true
			break
		}
	}
	s.onDiskBusy = true
	want := append([]string(nil), paths...)
	prevDirs := s.dirMod
	u.libRun(s, func() {
		dirs := parentDirs(want)
		stamps := dirStamps(dirs)
		moved := unknown
		for d, st := range stamps {
			if prev, had := prevDirs[d]; !had || prev != st {
				moved = true
			}
		}
		var res map[string]bool
		if moved {
			res = make(map[string]bool, len(want))
			for _, p := range want {
				res[p] = pathOnDisk(p)
			}
		}
		s.mu.Lock()
		s.onDiskBusy = false
		if res != nil {
			s.onDiskSweeps++
		}
		if s.dirMod == nil {
			s.dirMod = make(map[string]libDirStamp, len(stamps))
		}
		for d, st := range stamps {
			s.dirMod[d] = st
		}
		changed := false
		if res != nil {
			if s.onDiskCk == nil {
				s.onDiskCk = make(map[string]bool, len(res))
			}
			for p, ex := range res {
				if old, ok := s.onDiskCk[p]; !ok || old != ex {
					changed = true
				}
				s.onDiskCk[p] = ex
			}
		}
		s.mu.Unlock()
		if changed && !u.stopped() {
			u.libPatchBody()
		}
	})
}

// libFsChanged forgets the on-disk verdicts for paths this UI just changed on disk (delete/move),
// so the next render re-sweeps them instead of waiting for a dir-mtime observation.
func (u *UI) libFsChanged(paths ...string) {
	s := u.lib()
	s.mu.Lock()
	for _, p := range paths {
		delete(s.onDiskCk, p)
		delete(s.dirMod, filepath.Dir(p))
	}
	s.mu.Unlock()
}

// libBrowseEntries returns the cached listing for dir, refreshed off the handler lane and gated on
// the DIRECTORY MTIME instead of the old 2s TTL: the bg job stats dir first and only re-runs
// os.ReadDir + per-entry Info when the dir actually moved (or the cache belongs to another dir).
// ok=false -> nothing cached for this dir yet (render a loading placeholder; the read re-patches).
// Caller holds s.mu.
func (u *UI) libBrowseEntries(s *libSt, dir string) (fes []libFe, errRead, ok bool) {
	cached := s.browseDir == dir
	if !s.browseBusy {
		s.browseBusy = true
		prev := s.browseMod
		u.libRun(s, func() {
			stamp := dirStamps([]string{dir})[dir]
			if cached && stamp == prev && stamp.ok {
				s.mu.Lock()
				s.browseBusy = false
				s.mu.Unlock()
				return // dir untouched since the cached listing was read
			}
			entries, err := os.ReadDir(dir)
			var out []libFe
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".") {
					continue
				}
				fi, serr := e.Info()
				if serr != nil {
					continue
				}
				out = append(out, libFe{name, filepath.Join(dir, name), libKind(name, fi.IsDir()), fi.IsDir(), fi.Size(), fi.ModTime()})
			}
			s.mu.Lock()
			changed := s.browseDir != dir || s.browseErr != (err != nil) || !slices.Equal(s.browseFes, out)
			s.browseBusy, s.browseReads = false, s.browseReads+1
			s.browseDir, s.browseMod = dir, stamp
			s.browseErr = err != nil
			s.browseFes = out
			s.mu.Unlock()
			if changed && !u.stopped() { // replace the placeholder / refresh a listing that moved
				u.libPatchBody()
			}
		})
	}
	if !cached {
		return nil, false, false
	}
	return s.browseFes, s.browseErr, true
}

// ── playlist rows + smart counts ──

// libPlaylists returns the playlist rows for this render, retained and keyed by PlaylistVersion +
// the hydrate generation. ListPlaylists issues a per-row COUNT subquery and was called 2-3x per
// render on the handler lane; now only a cold first fill runs there (the old path blocked too) and
// every refresh runs on u.bg. Caller holds s.mu.
func (u *UI) libPlaylists(s *libSt) []libdb.PlaylistRow {
	if u.svc.Lib == nil {
		return nil
	}
	lib := u.svc.Lib
	return libDerive(u, s, &s.plD, u.libPlKey(s), false, func() []libdb.PlaylistRow {
		rows, _ := lib.ListPlaylists()
		return rows
	}, u.libPatchBody)
}

// libPlKey is the playlist-rows key. Caller holds s.mu.
func (u *UI) libPlKey(s *libSt) libDerivKey {
	if u.svc.Lib == nil {
		return libDerivKey{loadGen: s.loadGen}
	}
	return libDerivKey{pl: u.svc.Lib.PlaylistVersion(), loadGen: s.loadGen}
}

// libSmartCounts returns the smart-playlist match counts (id->count), computed off the lane: each
// count is len(filterSmartDB(...)) = a full ~23k scan + a compat DB read, and it ran per smart list
// per render. Keyed by the three epochs it depends on + the hydrate generation (a rules edit bumps
// PlaylistVersion), so the old per-render FNV over every rule set is gone. Cold is ASYNC: render
// shows the established "…" placeholder until the first compute lands. Caller holds s.mu.
func (u *UI) libSmartCounts(s *libSt, rows []libdb.PlaylistRow) map[int64]int {
	if u.svc.Lib == nil {
		return nil
	}
	ep := u.libEpoch()
	k := libDerivKey{lib: ep.lib, pl: ep.pl, compat: ep.compat, loadGen: s.loadGen}
	if s.smartD.warm && s.smartD.key == k {
		return s.smartD.val
	}
	// The counts are computed FROM rows, so they may only be stamped with an epoch the rows
	// themselves are current for - otherwise a rules edit settles the OLD rules' counts under the
	// NEW key and sticks. (The deleted implementation hashed the rule TEXT, which hid this; keying
	// on the epoch makes the ordering explicit. plD's settle re-patches, so this converges.)
	if pk := u.libPlKey(s); !s.plD.warm || s.plD.key != pk {
		return s.smartD.val
	}
	smart := make([]libdb.PlaylistRow, 0, len(rows))
	for _, p := range rows {
		if p.Kind == libdb.PlaylistSmart {
			smart = append(smart, p)
		}
	}
	if len(smart) == 0 { // nothing to eval: settle this key empty, no goroutine
		s.smartD.val, s.smartD.key, s.smartD.warm = map[int64]int{}, k, true
		return s.smartD.val
	}
	trk := s.tracks // replaced wholesale on reload/edit - safe to read off-thread
	return libDerive(u, s, &s.smartD, k, true, func() map[int64]int {
		counts := make(map[int64]int, len(smart))
		for _, p := range smart {
			if r, ok := libParseRules(p.Rules); ok {
				counts[p.ID] = len(u.filterSmartDB(trk, r))
			}
		}
		return counts
	}, u.libPatchBody)
}
