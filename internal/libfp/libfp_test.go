package libfp

import (
	"context"
	"fmt"
	"testing"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// fakeStore is an in-memory Store. AppendChanges records events and (like the real libdb) marks
// the hash fingerprinted, so a second sweep pass skips it.
type fakeStore struct {
	tracks    []musiclib.Track
	have      map[string]bool
	appended  []libdb.ChangeEvent
	loadErr   error
	haveErr   error
	appendErr error
}

func (f *fakeStore) LoadAllTracks() ([]musiclib.Track, error) { return f.tracks, f.loadErr }
func (f *fakeStore) FingerprintedHashes() (map[string]bool, error) {
	out := map[string]bool{}
	for h := range f.have {
		out[h] = true
	}
	return out, f.haveErr
}
func (f *fakeStore) AppendChanges(ev []libdb.ChangeEvent) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	if f.have == nil {
		f.have = map[string]bool{}
	}
	for _, e := range ev {
		f.appended = append(f.appended, e)
		if e.TrackFP != "" {
			f.have[e.TrackHash] = true
		}
	}
	return nil
}

type fakeComputer struct {
	calls []string
	fail  map[string]bool // paths that error
	empty map[string]bool // paths that return "" without error
}

func (c *fakeComputer) Compute(_ context.Context, path string) (string, float64, error) {
	c.calls = append(c.calls, path)
	if c.fail[path] {
		return "", 0, fmt.Errorf("compute boom: %s", path)
	}
	if c.empty[path] {
		return "", 0, nil
	}
	return "FP:" + path, 1.0, nil
}

func tk(artist, title, path string) musiclib.Track {
	return musiclib.Track{Artist: artist, Title: title, Path: path}
}

func alwaysOn() bool { return true }

func TestSweepComputesAndPersistsMissingPrints(t *testing.T) {
	st := &fakeStore{tracks: []musiclib.Track{tk("A", "T1", "a.mp3"), tk("B", "T2", "b.mp3")}}
	cp := &fakeComputer{}
	s := New(st, cp, Options{Batch: 10, Enabled: alwaysOn})

	s.tick(context.Background())

	if len(cp.calls) != 2 {
		t.Fatalf("compute calls = %d, want 2 (%v)", len(cp.calls), cp.calls)
	}
	if len(st.appended) != 2 {
		t.Fatalf("persisted events = %d, want 2", len(st.appended))
	}
	byHash := map[string]libdb.ChangeEvent{}
	for _, e := range st.appended {
		if e.Field != "fingerprint" || e.Op != "set" || e.Origin != "fingerprint" {
			t.Fatalf("event shape wrong: %+v", e)
		}
		if e.TrackFP == "" || e.NewValue == "" {
			t.Fatalf("event missing print: %+v", e)
		}
		byHash[e.TrackHash] = e
	}
	// The print must be keyed by the same identity FingerprintForTrack reads.
	hA := libdb.TrackHash("A", "T1", 0)
	if ev, ok := byHash[hA]; !ok || ev.TrackFP != "FP:a.mp3" {
		t.Fatalf("T1 print not keyed/valued correctly: ok=%v %+v", ok, ev)
	}
	if cov := s.Coverage(); cov.Total != 2 || cov.Pending != 2 || cov.Covered != 0 {
		t.Fatalf("coverage = %+v, want total2 pending2 covered0", cov)
	}
	if st := s.Stats(); st.Computed != 2 || st.Failed != 0 {
		t.Fatalf("stats = %+v, want computed2 failed0", st)
	}
}

func TestSweepSkipsTracksThatAlreadyHavePrints(t *testing.T) {
	hA := libdb.TrackHash("A", "T1", 0)
	st := &fakeStore{
		tracks: []musiclib.Track{tk("A", "T1", "a.mp3"), tk("B", "T2", "b.mp3")},
		have:   map[string]bool{hA: true},
	}
	cp := &fakeComputer{}
	s := New(st, cp, Options{Batch: 10, Enabled: alwaysOn})

	s.tick(context.Background())

	if len(cp.calls) != 1 || cp.calls[0] != "b.mp3" {
		t.Fatalf("expected only b.mp3 fingerprinted, got %v", cp.calls)
	}
	if cov := s.Coverage(); cov.Total != 2 || cov.Covered != 1 || cov.Pending != 1 {
		t.Fatalf("coverage = %+v, want total2 covered1 pending1", cov)
	}
}

func TestSweepHonorsFeatureToggle(t *testing.T) {
	st := &fakeStore{tracks: []musiclib.Track{tk("A", "T1", "a.mp3")}}
	cp := &fakeComputer{}
	s := New(st, cp, Options{Batch: 10, Enabled: func() bool { return false }})

	s.tick(context.Background())

	if len(cp.calls) != 0 {
		t.Fatalf("disabled feature must not fingerprint, got %v", cp.calls)
	}
	if len(st.appended) != 0 {
		t.Fatalf("disabled feature must not persist, got %d", len(st.appended))
	}
}

func TestSweepHonorsIdleGate(t *testing.T) {
	st := &fakeStore{tracks: []musiclib.Track{tk("A", "T1", "a.mp3")}}
	cp := &fakeComputer{}
	s := New(st, cp, Options{Batch: 10, Enabled: alwaysOn, Allowed: func() bool { return false }})

	s.tick(context.Background())

	if len(cp.calls) != 0 {
		t.Fatalf("busy app must not fingerprint, got %v", cp.calls)
	}
}

func TestSweepHonorsBudgetAcrossTicks(t *testing.T) {
	var tracks []musiclib.Track
	for i := 0; i < 5; i++ {
		tracks = append(tracks, tk(fmt.Sprintf("A%d", i), fmt.Sprintf("T%d", i), fmt.Sprintf("p%d.mp3", i)))
	}
	st := &fakeStore{tracks: tracks}
	cp := &fakeComputer{}
	s := New(st, cp, Options{Batch: 2, Enabled: alwaysOn})

	ctx := context.Background()
	s.tick(ctx)
	if len(cp.calls) != 2 {
		t.Fatalf("tick1 calls = %d, want 2 (budget)", len(cp.calls))
	}
	s.tick(ctx)
	if len(cp.calls) != 4 {
		t.Fatalf("tick2 calls = %d, want 4", len(cp.calls))
	}
	s.tick(ctx)
	if len(cp.calls) != 5 {
		t.Fatalf("tick3 calls = %d, want 5 (queue drained)", len(cp.calls))
	}
	// A fresh pass: every track now has a print (fakeStore mirrors the real skip), so nothing runs.
	s.tick(ctx)
	if len(cp.calls) != 5 {
		t.Fatalf("tick4 recomputed already-fingerprinted tracks: calls=%d", len(cp.calls))
	}
	if cov := s.Coverage(); cov.Pending != 0 || cov.Covered != 5 {
		t.Fatalf("after full coverage: %+v, want pending0 covered5", cov)
	}
}

func TestSweepErrorOnOneFileDoesNotStallQueue(t *testing.T) {
	st := &fakeStore{tracks: []musiclib.Track{
		tk("A", "T1", "a.mp3"), tk("B", "T2", "bad.mp3"), tk("C", "T3", "c.mp3"), tk("D", "T4", "empty.mp3"),
	}}
	cp := &fakeComputer{fail: map[string]bool{"bad.mp3": true}, empty: map[string]bool{"empty.mp3": true}}
	s := New(st, cp, Options{Batch: 10, Enabled: alwaysOn})

	s.tick(context.Background())

	if len(cp.calls) != 4 {
		t.Fatalf("all four should be attempted despite failures, got %v", cp.calls)
	}
	// Only the two good files persist.
	if len(st.appended) != 2 {
		t.Fatalf("persisted = %d, want 2 (a.mp3 + c.mp3)", len(st.appended))
	}
	persisted := map[string]bool{}
	for _, e := range st.appended {
		persisted[e.TrackFP] = true
	}
	if !persisted["FP:a.mp3"] || !persisted["FP:c.mp3"] {
		t.Fatalf("wrong tracks persisted: %v", persisted)
	}
	if stt := s.Stats(); stt.Computed != 2 || stt.Failed != 2 {
		t.Fatalf("stats = %+v, want computed2 failed2 (error + empty)", stt)
	}
}

func TestSweepSkipsUntitledAndPathlessTracks(t *testing.T) {
	st := &fakeStore{tracks: []musiclib.Track{
		tk("A", "T1", "a.mp3"),
		tk("B", "", "b.mp3"),   // no title
		tk("C", "T3", ""),      // no path
		tk("A", "T1", "a.mp3"), // duplicate identity
	}}
	cp := &fakeComputer{}
	s := New(st, cp, Options{Batch: 10, Enabled: alwaysOn})

	s.tick(context.Background())

	if len(cp.calls) != 1 || cp.calls[0] != "a.mp3" {
		t.Fatalf("only the one valid, unique track should fingerprint, got %v", cp.calls)
	}
}

func TestSweepReloadErrorIsSafe(t *testing.T) {
	st := &fakeStore{loadErr: fmt.Errorf("db down")}
	cp := &fakeComputer{}
	s := New(st, cp, Options{Batch: 10, Enabled: alwaysOn})

	s.tick(context.Background()) // must not panic; simply does nothing

	if len(cp.calls) != 0 {
		t.Fatalf("reload error should yield no work, got %v", cp.calls)
	}
}
