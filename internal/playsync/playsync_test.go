package playsync

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
)

// fakeAPI is an in-memory trackAPI for testing the sync logic without a live backend.
type fakeAPI struct {
	candidates []api.FpCandidate
	provN      int
	submitted  int
	observed   int
	streams    int
	ingested   int
	endedN     int
	lastReq    api.CreateStreamReq
	lastEvents []api.IngestEvent
	libCalls   int
	libTracks  []api.LibraryTrack
	libCanon   map[string]string // title → canonical id returned by the bulk upload
	libErr     error             // forced bulk-upload failure

	// media sync (methods in mediasync_test.go)
	listRows   []api.LibraryTrackOut
	listCalls  int
	wfUploads  map[string]string // libID → peaks b64
	wfDurMs    int
	wfCalls    int
	wfErr      error
	artUploads map[string][]byte // libID → uploaded bytes
	artCT      map[string]string // libID → content type
	artCalls   int

	pls *fakePlaylists // playlist sync (methods in playlistsync_test.go); lazily init
}

func (f *fakeAPI) IdentifyFingerprint(_ context.Context, _, _ string, _ int) ([]api.FpCandidate, error) {
	return f.candidates, nil
}
func (f *fakeAPI) SubmitFingerprint(_ context.Context, _, _, _ string, _, _ int) error {
	f.submitted++
	return nil
}
func (f *fakeAPI) CreateProvisionalTrack(_ context.Context, _ string, _ api.NewTrack) (string, error) {
	f.provN++
	return "prov_track", nil
}
func (f *fakeAPI) ReportObservation(_ context.Context, _, _ string, _ api.Observation) error {
	f.observed++
	return nil
}
func (f *fakeAPI) CreateStream(_ context.Context, _ string, req api.CreateStreamReq) (api.CreateStreamResp, error) {
	f.streams++
	f.lastReq = req
	return api.CreateStreamResp{StreamID: "strm_x", PublishToken: "pub"}, nil
}
func (f *fakeAPI) Ingest(_ context.Context, _, _ string, ev []api.IngestEvent) error {
	f.ingested += len(ev)
	f.lastEvents = append(f.lastEvents, ev...)
	return nil
}
func (f *fakeAPI) EndStream(_ context.Context, _, _ string) error { f.endedN++; return nil }

func openDB(t *testing.T) *libdb.DB {
	t.Helper()
	d, err := libdb.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func tokenFn(tok string) func() string { return func() string { return tok } }

func TestIdentifyTrackFingerprintMatch(t *testing.T) {
	d := openDB(t)
	track := Track{Artist: "A", Title: "T", DurationSec: 200}
	// Seed a fingerprint in the change_log so identify runs.
	if err := d.AppendChanges([]libdb.ChangeEvent{{
		TrackHash: track.hash(), TrackFP: "FP", Field: "fingerprint", Op: "set", Origin: "fingerprint", NewValue: `"FP"`,
	}}); err != nil {
		t.Fatalf("seed fp: %v", err)
	}
	f := &fakeAPI{candidates: []api.FpCandidate{{TrackID: "trk_canon", Confidence: 0.88}}}
	s := New(f, d, nil, tokenFn("tok"))

	link, err := s.IdentifyTrack(context.Background(), track)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if link.TrackID != "trk_canon" || link.Provisional {
		t.Fatalf("want canonical link, got %+v", link)
	}
	if f.submitted != 1 || f.observed != 1 {
		t.Fatalf("expected fingerprint submit + observation, got submit=%d obs=%d", f.submitted, f.observed)
	}
	// Second call is cached - no new API hits.
	if _, err := s.IdentifyTrack(context.Background(), track); err != nil {
		t.Fatalf("identify cached: %v", err)
	}
	if f.submitted != 1 {
		t.Fatalf("cached call hit the API again (submit=%d)", f.submitted)
	}
}

func TestIdentifyTrackProvisionalOnLowConfidence(t *testing.T) {
	d := openDB(t)
	track := Track{Artist: "A", Title: "Unknown"}
	if err := d.AppendChanges([]libdb.ChangeEvent{{
		TrackHash: track.hash(), TrackFP: "FP", Field: "fingerprint", Op: "set", Origin: "fingerprint", NewValue: `"FP"`,
	}}); err != nil {
		t.Fatalf("seed fp: %v", err)
	}
	f := &fakeAPI{candidates: []api.FpCandidate{{TrackID: "trk_weak", Confidence: 0.2}}} // below floor
	s := New(f, d, nil, tokenFn("tok"))

	link, err := s.IdentifyTrack(context.Background(), track)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if !link.Provisional || link.TrackID != "prov_track" || f.provN != 1 {
		t.Fatalf("expected provisional seed, got %+v provN=%d", link, f.provN)
	}
}

func TestDrainUnauthedDefers(t *testing.T) {
	d := openDB(t)
	if err := d.SavePlayedTrack(libdb.PlayedTrack{ID: "rec#0", RecordingID: "rec", Title: "T", StartedAt: time.Now()}); err != nil {
		t.Fatalf("save played: %v", err)
	}
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("")) // no token
	res, err := s.DrainRecording(context.Background(), "rec")
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if res.Linked+res.Provisional+res.Failed != 0 {
		t.Fatalf("unauthed drain should defer, got %+v", res)
	}
}

func TestUploadRecordedSetIdempotent(t *testing.T) {
	d := openDB(t)
	now := time.Now()
	set := RecordedSet{
		RecordingID: "rec_1", Title: "My Set", StartedAt: now,
		Tracks: []Track{
			{Artist: "A1", Title: "T1", StartedAt: now, Deck: "A", BPM: 128},
			{Artist: "A2", Title: "T2", StartedAt: now.Add(5 * time.Minute), Deck: "B"},
		},
	}
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))

	id, err := s.UploadRecordedSet(context.Background(), set)
	if err != nil || id != "strm_x" {
		t.Fatalf("upload: id=%q err=%v", id, err)
	}
	if f.streams != 1 || f.ingested != 2 || f.endedN != 1 {
		t.Fatalf("expected 1 stream / 2 events / 1 end, got streams=%d ingested=%d end=%d", f.streams, f.ingested, f.endedN)
	}
	if f.lastReq.Metadata["recorded"] != true {
		t.Fatalf("recorded flag not set in metadata: %+v", f.lastReq.Metadata)
	}
	if f.lastEvents[0].Type != "deck.loaded" || f.lastEvents[0].TS == 0 {
		t.Fatalf("event not backdated deck.loaded: %+v", f.lastEvents[0])
	}
	// Idempotent: a second upload returns the same stream id without re-creating.
	id2, err := s.UploadRecordedSet(context.Background(), set)
	if err != nil || id2 != "strm_x" {
		t.Fatalf("re-upload: id=%q err=%v", id2, err)
	}
	if f.streams != 1 {
		t.Fatalf("idempotent upload re-created the stream (streams=%d)", f.streams)
	}
}
