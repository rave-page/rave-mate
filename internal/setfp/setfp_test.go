package setfp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/worker"
)

// fakeWorker returns a deterministic fingerprint derived from the requested offset, and
// records each call so the test can assert the spans were dispatched correctly.
type fakeWorker struct {
	calls []map[string]any
	fail  map[float64]bool // offsets that should error (simulate an undecodable span)
}

func (f *fakeWorker) RunStream(_ context.Context, typ, method string, params any, _ worker.ProgressFunc) (json.RawMessage, error) {
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, m)
	off, _ := m["offsetSeconds"].(float64)
	if f.fail[off] {
		return nil, context.DeadlineExceeded
	}
	return json.Marshal(map[string]any{"fingerprint": "FP@" + method + jitter(off), "durationSeconds": 30.0})
}

func jitter(off float64) string { return "_" + string(rune('A'+int(off)%26)) }

func openDB(t *testing.T) *libdb.DB {
	t.Helper()
	d, err := libdb.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	d.SetNodeID("node-test")
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestFingerprintSetWritesChangeLog(t *testing.T) {
	db := openDB(t)
	fw := &fakeWorker{}
	f := New(fw, db)

	spans := []TrackSpan{
		{Artist: "A", Title: "One", OffsetSeconds: 0, LengthSeconds: 120},
		{Artist: "B", Title: "Two", OffsetSeconds: 60, LengthSeconds: 90},
	}
	var progress int
	n, err := f.FingerprintSet(context.Background(), "/sets/x.ogg", spans, func(done, total int) { progress = done })
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if n != 2 || progress != 2 {
		t.Fatalf("n=%d progress=%d want 2/2", n, progress)
	}
	if len(fw.calls) != 2 || fw.calls[0]["path"] != "/sets/x.ogg" || fw.calls[1]["offsetSeconds"] != 60.0 {
		t.Fatalf("worker calls wrong: %+v", fw.calls)
	}

	// Each track must have a fingerprint change_log row with track_fp populated.
	for _, sp := range spans {
		hash := libdb.TrackHash(sp.Artist, sp.Title, 0)
		evs, err := db.ChangesForTrack(hash)
		if err != nil {
			t.Fatalf("changes: %v", err)
		}
		var fpRow *libdb.ChangeEvent
		for i := range evs {
			if evs[i].Field == "fingerprint" {
				fpRow = &evs[i]
				break
			}
		}
		if fpRow == nil {
			t.Fatalf("no fingerprint row for %s/%s", sp.Artist, sp.Title)
		}
		if fpRow.TrackFP == "" || fpRow.Origin != "fingerprint" || fpRow.Op != "set" {
			t.Fatalf("fingerprint row wrong: %+v", *fpRow)
		}
	}
}

func TestFingerprintSetSkipsFailures(t *testing.T) {
	db := openDB(t)
	fw := &fakeWorker{fail: map[float64]bool{0: true}} // first track's span fails to decode
	f := New(fw, db)
	spans := []TrackSpan{
		{Artist: "A", Title: "One", OffsetSeconds: 0},
		{Artist: "B", Title: "Two", OffsetSeconds: 30},
	}
	n, err := f.FingerprintSet(context.Background(), "/sets/x.ogg", spans, nil)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if n != 1 { // only the second track succeeds
		t.Fatalf("n=%d want 1", n)
	}
	if evs, _ := db.ChangesForTrack(libdb.TrackHash("A", "One", 0)); len(evs) != 0 {
		t.Fatalf("failed track must not be recorded: %+v", evs)
	}
}

func TestFingerprintSetUnavailable(t *testing.T) {
	f := New(nil, nil)
	if _, err := f.FingerprintSet(context.Background(), "x", nil, nil); err == nil {
		t.Fatal("nil worker should error")
	}
}
