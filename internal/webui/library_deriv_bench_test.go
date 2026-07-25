package webui

// Phase B4b numbers: HANDLER-LANE occupancy old-vs-new for the Library derivations, plus the
// filesystem sweep cost the 2s/5s TTLs used to pay on a timer.
//
// The "legacy" halves below are the DELETED implementations, transcribed verbatim (the
// liveTickLegacy precedent): the FNV signature over every control, the FNV over every smart rule
// set, the PlaylistVersion compare, and - crucially - LibraryVersion as the SELECT MAX(seq) it was
// before B4b. Without transcribing them there is nothing to compare against.
//
// Run: GOWORK=off go test -count=2 ./internal/webui -run '^$' -bench LibLane -benchtime 1s

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"rave.page/mate/internal/libdb"

	_ "modernc.org/sqlite" // the raw handle the legacy LibraryVersion bench queries through
)

// ── transcribed pre-B4b lane work ──

// legacyLibraryVersion is libdb.LibraryVersion before B4b: one query per call, on the caller's
// goroutine, against a SetMaxOpenConns(1) handle.
func legacyLibraryVersion(db *sql.DB, nodeID string) int64 {
	var max sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(seq) FROM change_log WHERE node_id=?`, nodeID).Scan(&max); err != nil {
		return 0
	}
	return max.Int64
}

// legacyCollViewSig is the deleted collViewSignature.
func legacyCollViewSig(s *libSt, libVer, plVer, compatVer int64) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "g%d|l%d|p%d|c%d|q%q|o%s|d%t|nd%t|di%d",
		s.loadGen, libVer, plVer, compatVer, s.collSearch, s.collSort, s.collDesc, s.collNoDrops, len(s.dropsIdx))
	for _, set := range []struct {
		tag string
		m   map[string]bool
	}{{"k", s.keySel}, {"ge", s.collGenre}, {"la", s.collLabel}} {
		keys := make([]string, 0, len(set.m))
		for k := range set.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "|%s%s", set.tag, k)
		}
	}
	ids := make([]int64, 0, len(s.collPl))
	for id := range s.collPl {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		fmt.Fprintf(h, "|pl%d", id)
	}
	return h.Sum64()
}

// legacySmartCountSig is the deleted libSmartCountSig.
func legacySmartCountSig(loadGen int, libVer, plVer, compatVer int64, rows []libdb.PlaylistRow) uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "g%d|l%d|p%d|c%d", loadGen, libVer, plVer, compatVer)
	for _, p := range rows {
		if p.Kind == libdb.PlaylistSmart {
			fmt.Fprintf(h, "|%d=%s", p.ID, p.Rules)
		}
	}
	return h.Sum64()
}

// legacyCollLane is one steady-state collection render's lane work BEFORE B4b: two epoch reads
// (one of them a query), the control signature, the smart-rule signature and the playlist compare -
// all to conclude "nothing moved".
func legacyCollLane(db *libdb.DB, raw *sql.DB, s *libSt, rows []libdb.PlaylistRow) uint64 {
	libVer := legacyLibraryVersion(raw, "node-test")
	plVer, compatVer := db.PlaylistVersion(), db.CompatVersion()
	sig := legacyCollViewSig(s, libVer, plVer, compatVer)
	libVer2 := legacyLibraryVersion(raw, "node-test") // libSmartCounts read the epochs again
	sig2 := legacySmartCountSig(s.loadGen, libVer2, plVer, compatVer, rows)
	_ = db.PlaylistVersion() // libPlaylists' own compare
	return sig ^ sig2
}

// ── benches ──

// benchLibUI seeds n tracks + m smart playlists and warms every derivation.
func benchLibUI(b *testing.B, n, smart int) (*UI, *libSt, *libdb.DB, *sql.DB, []libdb.PlaylistRow) {
	b.Helper()
	u, s, db, q, path := newLibTestUIAt(b)
	libTestTracks(s, n)
	for i := 0; i < smart; i++ {
		id, err := db.CreatePlaylist(fmt.Sprintf("Smart %d", i), "smart", "")
		if err != nil {
			b.Fatalf("create: %v", err)
		}
		if err := db.SetSmartRules(id, fmt.Sprintf(`{"bpmMin":%d}`, 120+i)); err != nil {
			b.Fatalf("rules: %v", err)
		}
	}
	var rows []libdb.PlaylistRow
	for i := 0; i < 4; i++ { // warm coll + rows + counts (each settle may unlock the next)
		s.mu.Lock()
		_ = u.libCollView(s)
		rows = u.libPlaylists(s)
		_ = u.libSmartCounts(s, rows)
		s.mu.Unlock()
		q.drain()
		_ = u.drainEvals()
	}
	s.mu.Lock()
	if !s.collD.warm || !s.plD.warm || !s.smartD.warm {
		s.mu.Unlock()
		b.Fatal("derivations did not warm - the bench would measure cold fills")
	}
	s.mu.Unlock()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		b.Fatalf("raw handle: %v", err)
	}
	raw.SetMaxOpenConns(1)
	b.Cleanup(func() { _ = raw.Close() })
	return u, s, db, raw, rows
}

// BenchmarkLibLaneSteady: what the handler lane pays for a collection render when NOTHING moved -
// the 1 Hz tick and every selection-only re-render. legacy = two SELECT MAX(seq) + two FNV
// signatures; retained = three comparable-key builds and three struct compares.
func BenchmarkLibLaneSteady(b *testing.B) {
	u, s, db, raw, rows := benchLibUI(b, 23000, 6)
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s.mu.Lock()
			_ = legacyCollLane(db, raw, s, rows)
			s.mu.Unlock()
		}
	})
	b.Run("retained", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s.mu.Lock()
			_ = u.libCollView(s)
			r := u.libPlaylists(s)
			_ = u.libSmartCounts(s, r)
			s.mu.Unlock()
		}
	})
}

// BenchmarkLibLaneChanged: the WORST CASE the TTL/memo design existed to bound - a control moved,
// so the view has to be re-derived. legacy did the ~23k filter+sort ON the lane; retained returns
// last-known and hands the scan to u.bg (the runner is a no-op sink here, so this measures the lane
// only - the scan itself is BenchmarkLibCollViewCompute, unchanged work in a different place).
func BenchmarkLibLaneChanged(b *testing.B) {
	u, s, _, _, _ := benchLibUI(b, 23000, 6)
	b.Run("legacy_inline_scan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s.mu.Lock()
			s.collSort = []string{"Artist", "BPM"}[i%2] // an input moved: the memo misses
			_ = s.collView()
			s.mu.Unlock()
		}
	})
	b.Run("retained_kick", func(b *testing.B) {
		s.mu.Lock()
		s.derivRun = func(func()) {} // sink: the lane must not run the scan
		s.mu.Unlock()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s.mu.Lock()
			s.collSort = []string{"Artist", "BPM"}[i%2]
			s.ctlTouch()
			_ = u.libCollView(s)
			s.mu.Unlock()
		}
	})
}

// BenchmarkLibCollViewCompute is the scan itself - identical code before and after B4b, recorded so
// the lane numbers cannot be read as "the work vanished". It now runs on u.bg.
func BenchmarkLibCollViewCompute(b *testing.B) {
	_, s, _, _, _ := benchLibUI(b, 23000, 0)
	s.mu.Lock()
	in := s.collInputs()
	s.mu.Unlock()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = collViewOf(in)
	}
}

// BenchmarkLibFsSweep: the filesystem half. legacy re-stat'ed EVERY rendered row every 5s (and
// re-ran ReadDir+Info every 2s); the change gate stats the distinct parent dirs and stops there
// when nothing moved.
func BenchmarkLibFsSweep(b *testing.B) {
	dir := b.TempDir()
	paths := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		p := filepath.Join(dir, fmt.Sprintf("t%03d.flac", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			b.Fatalf("write: %v", err)
		}
		paths = append(paths, p)
	}
	b.Run("legacy_stat_every_row", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res := make(map[string]bool, len(paths))
			for _, p := range paths {
				res[p] = pathOnDisk(p)
			}
			_ = res
		}
	})
	b.Run("changegate_stat_dirs", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = dirStamps(parentDirs(paths))
		}
	})
	b.Run("legacy_readdir", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				_, _ = e.Info()
			}
		}
	})
}
