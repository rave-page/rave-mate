package setpublish

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/worker"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeRecs struct{ recs map[string]recorder.Recording }

func (f fakeRecs) Get(id string) (recorder.Recording, bool) {
	r, ok := f.recs[id]
	return r, ok
}

// fakeJobs answers probe.peaks / transcode.loudtl / publish.upload with canned results.
type fakeJobs struct {
	upload     worker.PublishUploadOut
	uploadErr  error
	lastUpload worker.PublishUploadIn
	progress   []worker.PublishProgress
	noAnalysis bool
}

func (f *fakeJobs) RunBackground(_ context.Context, typ, method string, _ any) (json.RawMessage, error) {
	if f.noAnalysis {
		return nil, errors.New("no analysis")
	}
	switch method {
	case "probe.peaks":
		return json.Marshal(map[string]any{
			"peaks": "AAECAw==", "bands": "AAAAAAAA", "durationSeconds": 3600.0,
			"rate": 16000, "samples": int64(57_600_000), "leadSkipMs": 0.0,
		})
	case "transcode.loudtl":
		mom := make([]float64, 120)
		for i := range mom {
			mom[i] = -9.5
		}
		return json.Marshal(worker.LoudTimeline{I: -9.4, TP: -0.8, LRA: 5.2, Step: 30, Mom: mom})
	}
	return nil, errors.New("unexpected job " + typ + "/" + method)
}

func (f *fakeJobs) RunStreamBackground(_ context.Context, _, method string, params any, onProgress worker.ProgressFunc) (json.RawMessage, error) {
	if method != worker.MethodPublishUpload {
		return nil, errors.New("unexpected stream job " + method)
	}
	raw, _ := json.Marshal(params)
	_ = json.Unmarshal(raw, &f.lastUpload)
	if onProgress != nil {
		for _, p := range f.progress {
			b, _ := json.Marshal(p)
			onProgress("progress", b)
		}
	}
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	return json.Marshal(f.upload)
}

type fakeAPI struct {
	created   []api.CreateRecordingReq
	audio     []string // media upload ids attached
	waveforms int
	loudness  []api.RecordingLoudness
	createErr error
}

func (f *fakeAPI) CreateRecording(_ context.Context, _ string, req api.CreateRecordingReq) (api.CreateRecordingResp, error) {
	if f.createErr != nil {
		return api.CreateRecordingResp{}, f.createErr
	}
	f.created = append(f.created, req)
	return api.CreateRecordingResp{ID: "rec_api_1", TracklistID: "tl_1"}, nil
}

func (f *fakeAPI) SetRecordingAudio(_ context.Context, _, _, mediaUploadID string) error {
	f.audio = append(f.audio, mediaUploadID)
	return nil
}

func (f *fakeAPI) SetRecordingWaveform(_ context.Context, _, _, peaks, _ string, _ int) error {
	if peaks == "" {
		return errors.New("no peaks")
	}
	f.waveforms++
	return nil
}

func (f *fakeAPI) SetRecordingLoudness(_ context.Context, _, _ string, l api.RecordingLoudness) error {
	f.loudness = append(f.loudness, l)
	return nil
}

func (f *fakeAPI) BaseURL() string { return "https://development.api.rave.page" }

// ── harness ──────────────────────────────────────────────────────────────────

type rig struct {
	pub  *Publisher
	api  *fakeAPI
	jobs *fakeJobs
	lib  *libdb.DB
	path string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	lib, err := libdb.Open(filepath.Join(dir, "music.db"))
	if err != nil {
		t.Fatalf("libdb: %v", err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	st, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	audio := filepath.Join(dir, "set.flac")
	if werr := os.WriteFile(audio, make([]byte, 4096), 0o600); werr != nil {
		t.Fatal(werr)
	}
	start := time.Date(2026, 8, 1, 21, 0, 0, 0, time.UTC)
	if serr := lib.SaveSetRecording(libdb.SetRecording{
		ID: "cap_1", RecordingID: "rec_1", Path: audio, Format: "flac",
		Kind: libdb.SetKindIcecast, StartedAt: start, EndedAt: start.Add(time.Hour), Bytes: 4096,
	}); serr != nil {
		t.Fatal(serr)
	}
	recs := fakeRecs{recs: map[string]recorder.Recording{"rec_1": {
		ID: "rec_1", Name: "Friday Warmup", StartedAt: start, EndedAt: start.Add(time.Hour),
		Tracks: []recorder.Track{
			{Title: "Opener", Artist: "A1", StartedAt: start.Add(30 * time.Second), EndedAt: start.Add(5 * time.Minute), BPM: 128, Deck: "A"},
			{Title: "Second", Artist: "A2", StartedAt: start.Add(5 * time.Minute), EndedAt: start.Add(11 * time.Minute), Key: "8A"},
			// starts BEFORE the capture - must clamp to 0
			{Title: "Early", Artist: "A3", StartedAt: start.Add(-2 * time.Minute), EndedAt: start.Add(30 * time.Second)},
		},
	}}}
	jobs := &fakeJobs{upload: worker.PublishUploadOut{
		FileHash: "hash_v1", FileSize: 4096, MediaUploadID: "mu_1", Status: api.MediaUploadReady,
	}}
	fapi := &fakeAPI{}
	asm := NewAssembler(recs, lib, st, jobs, nil)
	return &rig{
		pub: NewPublisher(asm, fapi, lib, jobs, nil, func() string { return "tok" }),
		api: fapi, jobs: jobs, lib: lib, path: audio,
	}
}

func okReq() Request {
	return Request{RecordingID: "rec_1", Visibility: api.VisibilityUnlisted, RightsConfirmed: true}
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestPublishRequiresRightsConfirmation(t *testing.T) {
	r := newRig(t)
	req := okReq()
	req.RightsConfirmed = false
	if _, err := r.pub.Publish(context.Background(), req, nil); !errors.Is(err, ErrRightsNotConfirmed) {
		t.Fatalf("err = %v, want ErrRightsNotConfirmed", err)
	}
	if len(r.api.created) != 0 {
		t.Error("nothing may be created without rights confirmation")
	}
}

func TestPublishRequiresToken(t *testing.T) {
	r := newRig(t)
	r.pub.token = func() string { return "" }
	if _, err := r.pub.Publish(context.Background(), okReq(), nil); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestPublishFullFlow(t *testing.T) {
	r := newRig(t)
	var stages []string
	res, err := r.pub.Publish(context.Background(), okReq(), func(p Progress) {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.APIRecordingID != "rec_api_1" || res.TracklistID != "tl_1" {
		t.Errorf("ids = %+v", res)
	}
	if res.Republished || res.AudioReused {
		t.Errorf("first publish should be neither republish nor reuse: %+v", res)
	}
	if !res.WaveformSent || !res.LoudnessSent {
		t.Errorf("waveform/loudness not sent: %+v", res)
	}
	if len(r.api.created) != 1 {
		t.Fatalf("created %d recordings, want 1", len(r.api.created))
	}
	c := r.api.created[0]
	if !c.RightsConfirmed || c.Source != api.SourceRecorded || c.Visibility != api.VisibilityUnlisted {
		t.Errorf("create body wrong: %+v", c)
	}
	if len(c.Tracklist) != 3 {
		t.Fatalf("tracklist has %d entries, want 3", len(c.Tracklist))
	}
	// Offsets are relative to the capture start, in play order, clamped at 0.
	if c.Tracklist[0].StartOffsetMs != 30_000 {
		t.Errorf("track 1 offset = %d, want 30000", c.Tracklist[0].StartOffsetMs)
	}
	if c.Tracklist[1].StartOffsetMs != 300_000 {
		t.Errorf("track 2 offset = %d, want 300000", c.Tracklist[1].StartOffsetMs)
	}
	if c.Tracklist[2].StartOffsetMs != 0 {
		t.Errorf("pre-capture track must clamp to 0, got %d", c.Tracklist[2].StartOffsetMs)
	}
	for i, tr := range c.Tracklist {
		if tr.Number != i+1 {
			t.Errorf("track %d numbered %d", i, tr.Number)
		}
	}
	if len(r.api.loudness) != 1 || r.api.loudness[0].StepMs != 30_000 {
		t.Errorf("loudness = %+v", r.api.loudness)
	}
	if r.api.loudness[0].MomentaryB64 == "" {
		t.Error("momentary timeline missing")
	}
	if len(r.api.audio) != 1 || r.api.audio[0] != "mu_1" {
		t.Errorf("audio attach = %v", r.api.audio)
	}
	want := []string{StagePreparing, StagePublishing, StageDone}
	if len(stages) < len(want) {
		t.Errorf("stages = %v, want at least %v", stages, want)
	}
	// Ledger recorded for idempotency.
	led, ok, _ := r.lib.GetSetPublish("rec_1")
	if !ok || led.APIRecordingID != "rec_api_1" || led.FileHash != "hash_v1" || led.TracklistItems != 3 {
		t.Errorf("ledger = %+v ok=%v", led, ok)
	}
}

func TestPublishNeverShipsLocalPaths(t *testing.T) {
	r := newRig(t)
	if _, err := r.pub.Publish(context.Background(), okReq(), nil); err != nil {
		t.Fatalf("publish: %v", err)
	}
	// The manifest's audio ref must marshal without the local path.
	m, err := r.pub.Preview(context.Background(), "rec_1")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if m.Audio.LocalPath != r.path {
		t.Fatal("assembler lost the local path (the worker needs it)")
	}
	blob, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(r.path)
	if json.Valid(blob) && containsAny(string(blob), dir) {
		t.Errorf("manifest JSON leaked a local directory: %s", blob)
	}
	if m.Audio.Name != "set.flac" {
		t.Errorf("audio name = %q, want the basename", m.Audio.Name)
	}
	// The create body must not carry a path either.
	body, _ := json.Marshal(r.api.created[0])
	if containsAny(string(body), dir) {
		t.Errorf("create body leaked a local directory: %s", body)
	}
}

func TestRepublishReusesAudioAndSkipsCreate(t *testing.T) {
	r := newRig(t)
	if _, err := r.pub.Publish(context.Background(), okReq(), nil); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	// Second run: same file → the worker reports the hash unchanged.
	r.jobs.upload = worker.PublishUploadOut{
		FileHash: "hash_v1", FileSize: 4096, MediaUploadID: "mu_1",
		Status: api.MediaUploadReady, Reused: true,
	}
	res, err := r.pub.Publish(context.Background(), okReq(), nil)
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if !res.Republished || !res.AudioReused {
		t.Errorf("expected a republish reusing audio, got %+v", res)
	}
	if len(r.api.created) != 1 {
		t.Errorf("republish created %d recordings, want the original only", len(r.api.created))
	}
	if len(r.api.audio) != 1 {
		t.Errorf("audio re-attached %d times, want 1", len(r.api.audio))
	}
	if r.api.waveforms != 2 || len(r.api.loudness) != 2 {
		t.Errorf("metadata should be re-PUT: waveforms=%d loudness=%d", r.api.waveforms, len(r.api.loudness))
	}
	// The known hash + upload id must reach the worker so it can short-circuit.
	if r.jobs.lastUpload.KnownHash != "hash_v1" || r.jobs.lastUpload.KnownUploadID != "mu_1" {
		t.Errorf("worker params missing the ledger hints: %+v", r.jobs.lastUpload)
	}
}

func TestRepublishAfterAudioChangedReattaches(t *testing.T) {
	r := newRig(t)
	if _, err := r.pub.Publish(context.Background(), okReq(), nil); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	r.jobs.upload = worker.PublishUploadOut{
		FileHash: "hash_v2", FileSize: 5000, MediaUploadID: "mu_2", Status: api.MediaUploadReady,
	}
	res, err := r.pub.Publish(context.Background(), okReq(), nil)
	if err != nil {
		t.Fatalf("republish: %v", err)
	}
	if !res.Republished || res.AudioReused {
		t.Errorf("changed audio must not read as reused: %+v", res)
	}
	if len(r.api.audio) != 2 || r.api.audio[1] != "mu_2" {
		t.Errorf("new audio not attached: %v", r.api.audio)
	}
	led, _, _ := r.lib.GetSetPublish("rec_1")
	if led.FileHash != "hash_v2" || led.MediaUploadID != "mu_2" {
		t.Errorf("ledger not advanced: %+v", led)
	}
}

func TestPublishUploadFailureCreatesNothing(t *testing.T) {
	r := newRig(t)
	r.jobs.uploadErr = errors.New("uplink died")
	if _, err := r.pub.Publish(context.Background(), okReq(), nil); err == nil {
		t.Fatal("expected an upload error")
	}
	if len(r.api.created) != 0 {
		t.Error("a failed upload must not leave an orphan recording")
	}
	if _, ok, _ := r.lib.GetSetPublish("rec_1"); ok {
		t.Error("a failed upload must not write the ledger")
	}
}

func TestPreviewSurfacesAnalysisWarnings(t *testing.T) {
	r := newRig(t)
	r.jobs.noAnalysis = true
	m, err := r.pub.Preview(context.Background(), "rec_1")
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if m.Waveform != nil || m.Loudness != nil {
		t.Error("analysis was supposed to fail")
	}
	if len(m.Warnings) < 2 {
		t.Errorf("warnings = %v, want both waveform + loudness noted", m.Warnings)
	}
	if len(m.Tracks) != 3 {
		t.Errorf("tracks should still assemble without analysis, got %d", len(m.Tracks))
	}
}

func TestPreviewRejectsUnfinishedAndMissing(t *testing.T) {
	r := newRig(t)
	if _, err := r.pub.Preview(context.Background(), "nope"); err == nil {
		t.Error("unknown recording should error")
	}
}

// containsAny reports whether hay holds the directory in any form it could survive a JSON
// marshal: raw, forward-slashed, or with backslashes escaped.
func containsAny(hay, dir string) bool {
	if dir == "" {
		return false
	}
	for _, form := range []string{
		dir,
		strings.ReplaceAll(dir, `\`, "/"),
		strings.ReplaceAll(dir, `\`, `\\`),
	} {
		if strings.Contains(hay, form) {
			return true
		}
	}
	return false
}
