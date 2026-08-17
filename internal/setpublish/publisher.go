package setpublish

// Publisher runs the publish flow: assemble → upload audio (in the publish worker child) →
// create the recording → attach audio → PUT waveform + loudness → record the ledger.
//
// Order matters. The audio goes up BEFORE the recording is created: the transfer is the long,
// failure-prone part, and a failure there must not leave an orphan recording on the server. If a
// later step fails, the next attempt re-hashes the file and re-initiates - idempotent on
// (file_hash, user_id), so the server hands back the same upload with every chunk already
// present and the retry costs a hash pass, not a re-upload.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/shared/logbus"
	"rave.page/mate/internal/worker"
)

// ErrRightsNotConfirmed is returned when the caller hasn't affirmed they hold the rights.
// The UI gates on an explicit checkbox; this is the last line of defence.
var ErrRightsNotConfirmed = errors.New("setpublish: rights not confirmed")

// ErrUnauthenticated is returned when no access token is available.
var ErrUnauthenticated = errors.New("setpublish: no access token")

// Publish stages, in order. Also the Progress.Stage value.
const (
	StagePreparing  = "preparing"  // assembling the manifest (may analyse - minutes)
	StageUploading  = "uploading"  // chunked transfer
	StageProcessing = "processing" // server-side scan + promote
	StagePublishing = "publishing" // recording + tracklist + waveform + loudness
	StageDone       = "done"
	StageFailed     = "failed"
)

// Overall progress weighting per stage (the UI shows one bar across the whole flow).
const (
	pctPrepared   = 10.0
	pctUploaded   = 70.0
	pctProcessed  = 85.0
	uploadTimeout = 6 * time.Hour
	metaTimeout   = 2 * time.Minute
)

// Progress is one publish progress snapshot (overall, not per-stage).
type Progress struct {
	Stage      string  `json:"stage"`
	Percent    float64 `json:"percent"` // 0-100 across the WHOLE flow
	Detail     string  `json:"detail,omitempty"`
	BytesSent  int64   `json:"bytesSent,omitempty"`
	BytesTotal int64   `json:"bytesTotal,omitempty"`
}

// Result is a completed publish.
type Result struct {
	RecordingID    string `json:"recordingId"`    // local recorder id
	APIRecordingID string `json:"apiRecordingId"` // rave.page recording id
	TracklistID    string `json:"tracklistId,omitempty"`
	MediaUploadID  string `json:"mediaUploadId,omitempty"`
	FileHash       string `json:"fileHash,omitempty"`
	Tracks         int    `json:"tracks"`
	AudioReused    bool   `json:"audioReused"` // hash unchanged - no bytes re-sent
	Republished    bool   `json:"republished"` // updated an existing recording
	WaveformSent   bool   `json:"waveformSent"`
	LoudnessSent   bool   `json:"loudnessSent"`
}

// Request is one publish invocation.
type Request struct {
	RecordingID     string
	Title           string // "" = the manifest's title
	Visibility      string // api.Visibility*; "" = private
	PerformerID     string
	EventID         string
	RightsConfirmed bool
}

// PublishAPI is the api.Client subset the publisher calls (an interface so the flow is
// unit-testable without a live API).
type PublishAPI interface {
	CreateRecording(ctx context.Context, token string, req api.CreateRecordingReq) (api.CreateRecordingResp, error)
	SetRecordingAudio(ctx context.Context, token, recordingID, mediaUploadID string) error
	SetRecordingWaveform(ctx context.Context, token, recordingID, peaksB64, bandsB64 string, durationMs int) error
	SetRecordingLoudness(ctx context.Context, token, recordingID string, l api.RecordingLoudness) error
	BaseURL() string
}

// Publisher publishes assembled sets.
type Publisher struct {
	asm   *Assembler
	api   PublishAPI
	lib   *libdb.DB
	jobs  JobRunner
	log   *logbus.Bus
	token func() string
}

// NewPublisher wires the publisher. token returns the current access token ("" = signed out).
func NewPublisher(asm *Assembler, apiC PublishAPI, lib *libdb.DB, jobs JobRunner, log *logbus.Bus, token func() string) *Publisher {
	if token == nil {
		token = func() string { return "" }
	}
	return &Publisher{asm: asm, api: apiC, lib: lib, jobs: jobs, log: log, token: token}
}

// Preview assembles the manifest without publishing anything - what the confirm dialog shows.
func (p *Publisher) Preview(ctx context.Context, recordingID string) (SetManifest, error) {
	if p == nil || p.asm == nil {
		return SetManifest{}, fmt.Errorf("setpublish: unavailable")
	}
	return p.asm.Assemble(ctx, recordingID)
}

// Published returns the ledger entry for a recording (ok=false = never published).
func (p *Publisher) Published(recordingID string) (libdb.SetPublish, bool) {
	if p == nil || p.lib == nil {
		return libdb.SetPublish{}, false
	}
	rec, ok, err := p.lib.GetSetPublish(recordingID)
	if err != nil || !ok {
		return libdb.SetPublish{}, false
	}
	return rec, true
}

// Publish runs the full flow. onProgress (may be nil) receives overall progress snapshots.
// Long-running by nature - callers run it off the act lane.
func (p *Publisher) Publish(ctx context.Context, req Request, onProgress func(Progress)) (Result, error) {
	if p == nil || p.asm == nil || p.api == nil || p.jobs == nil {
		return Result{}, fmt.Errorf("setpublish: unavailable")
	}
	if !req.RightsConfirmed {
		return Result{}, ErrRightsNotConfirmed
	}
	token := p.token()
	if token == "" {
		return Result{}, ErrUnauthenticated
	}
	emit := func(pr Progress) {
		if onProgress != nil {
			onProgress(pr)
		}
	}

	emit(Progress{Stage: StagePreparing, Detail: "reading the set"})
	m, err := p.asm.Assemble(ctx, req.RecordingID)
	if err != nil {
		return Result{}, err
	}
	prev, republish := p.Published(req.RecordingID)
	res := Result{RecordingID: m.RecordingID, Tracks: len(m.Tracks), Republished: republish}
	emit(Progress{Stage: StagePreparing, Percent: pctPrepared, Detail: m.Audio.Name, BytesTotal: m.Audio.SizeBytes})

	up, err := p.uploadAudio(ctx, token, m, prev, emit)
	if err != nil {
		return res, err
	}
	res.MediaUploadID, res.FileHash, res.AudioReused = up.MediaUploadID, up.FileHash, up.Reused

	emit(Progress{Stage: StagePublishing, Percent: pctProcessed, Detail: "publishing metadata",
		BytesSent: m.Audio.SizeBytes, BytesTotal: m.Audio.SizeBytes})

	mctx, cancel := context.WithTimeout(ctx, metaTimeout)
	defer cancel()

	if republish && prev.APIRecordingID != "" {
		res.APIRecordingID = prev.APIRecordingID
	} else {
		created, cerr := p.api.CreateRecording(mctx, token, createReqFor(m, req))
		if cerr != nil {
			return res, fmt.Errorf("create recording: %w", cerr)
		}
		res.APIRecordingID, res.TracklistID = created.ID, created.TracklistID
	}

	// Attach audio unless the identical bytes are already attached from a previous publish.
	if !(res.AudioReused && republish) {
		if aerr := p.api.SetRecordingAudio(mctx, token, res.APIRecordingID, res.MediaUploadID); aerr != nil {
			return res, fmt.Errorf("attach audio: %w", aerr)
		}
	}
	if m.Waveform != nil {
		if werr := p.api.SetRecordingWaveform(mctx, token, res.APIRecordingID,
			m.Waveform.PeaksB64, m.Waveform.BandsB64, m.Waveform.DurationMs); werr != nil {
			p.warn("waveform", werr) // non-fatal: the set is published, the overview can be re-sent
		} else {
			res.WaveformSent = true
		}
	}
	if m.Loudness != nil {
		if lerr := p.api.SetRecordingLoudness(mctx, token, res.APIRecordingID, api.RecordingLoudness{
			IntegratedLUFS: m.Loudness.IntegratedLUFS, TruePeakDB: m.Loudness.TruePeakDB,
			LRA: m.Loudness.LRA, StepMs: m.Loudness.StepMs, MomentaryB64: m.Loudness.MomentaryB64,
		}); lerr != nil {
			p.warn("loudness", lerr) // non-fatal, as above
		} else {
			res.LoudnessSent = true
		}
	}

	if serr := p.lib.SaveSetPublish(libdb.SetPublish{
		RecordingID: m.RecordingID, APIRecordingID: res.APIRecordingID,
		MediaUploadID: res.MediaUploadID, FileHash: res.FileHash, TracklistItems: len(m.Tracks),
	}); serr != nil {
		p.warn("ledger", serr) // published fine; only re-publish detection suffers
	}
	p.info("set published", map[string]any{
		"recording": m.RecordingID, "apiRecording": res.APIRecordingID, "tracks": len(m.Tracks),
		"reused": res.AudioReused, "republished": republish,
	})
	emit(Progress{Stage: StageDone, Percent: 100, Detail: res.APIRecordingID,
		BytesSent: m.Audio.SizeBytes, BytesTotal: m.Audio.SizeBytes})
	return res, nil
}

// uploadAudio runs the publish worker child and maps its per-stage progress onto the overall bar.
func (p *Publisher) uploadAudio(ctx context.Context, token string, m SetManifest, prev libdb.SetPublish, emit func(Progress)) (worker.PublishUploadOut, error) {
	params := worker.PublishUploadIn{
		Path: m.Audio.LocalPath, MimeType: m.Audio.MimeType,
		APIBase: p.api.BaseURL(), Token: token,
		KnownHash: prev.FileHash, KnownUploadID: prev.MediaUploadID,
		TimeoutSec: int(uploadTimeout / time.Second),
	}
	total := m.Audio.SizeBytes
	onProgress := func(event string, data json.RawMessage) {
		if event != "progress" {
			return
		}
		var wp worker.PublishProgress
		if json.Unmarshal(data, &wp) != nil {
			return
		}
		emit(overallProgress(wp, total))
	}
	uctx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	raw, err := p.jobs.RunStreamBackground(uctx, "publish", worker.MethodPublishUpload, params, onProgress)
	if err != nil {
		return worker.PublishUploadOut{}, fmt.Errorf("upload: %w", err)
	}
	var out worker.PublishUploadOut
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		return worker.PublishUploadOut{}, fmt.Errorf("upload: bad worker result: %w", uerr)
	}
	if out.MediaUploadID == "" {
		return out, fmt.Errorf("upload: server returned no upload id")
	}
	return out, nil
}

// overallProgress folds a worker stage percentage into the single end-to-end bar.
func overallProgress(wp worker.PublishProgress, total int64) Progress {
	pr := Progress{BytesSent: wp.BytesSent, BytesTotal: total}
	switch wp.Stage {
	case worker.PublishStageHashing:
		pr.Stage, pr.Percent = StagePreparing, wp.Percent/100*pctPrepared
		pr.Detail = "checking the file"
	case worker.PublishStageUploading:
		pr.Stage = StageUploading
		pr.Percent = pctPrepared + wp.Percent/100*(pctUploaded-pctPrepared)
		if wp.Chunks > 0 {
			pr.Detail = fmt.Sprintf("part %d/%d", wp.Chunk, wp.Chunks)
		}
	case worker.PublishStageProcessing:
		pr.Stage, pr.Percent = StageProcessing, pctUploaded
		pr.Detail = "the server is checking the file"
		if wp.Status != "" {
			pr.Detail = "server: " + wp.Status
		}
	default:
		pr.Stage, pr.Percent = StageProcessing, pctProcessed
	}
	return pr
}

// createReqFor maps a manifest + request onto the create-recording body.
func createReqFor(m SetManifest, req Request) api.CreateRecordingReq {
	title := req.Title
	if title == "" {
		title = m.Title
	}
	vis := req.Visibility
	if vis == "" {
		vis = api.VisibilityPrivate
	}
	out := api.CreateRecordingReq{
		Title: title, Visibility: vis, Source: api.SourceRecorded,
		PerformerID: req.PerformerID, EventID: req.EventID,
		RightsConfirmed: req.RightsConfirmed,
		Tracklist:       make([]api.RecordingTrackIn, 0, len(m.Tracks)),
	}
	if !m.StartedAt.IsZero() {
		out.StartedAt = m.StartedAt.UTC().Format(time.RFC3339)
	}
	if !m.EndedAt.IsZero() {
		out.EndedAt = m.EndedAt.UTC().Format(time.RFC3339)
	}
	for _, t := range m.Tracks {
		out.Tracklist = append(out.Tracklist, api.RecordingTrackIn{
			Number: t.Number, Title: t.Title, Artist: t.Artist, Album: t.Album,
			Key: t.Key, BPM: t.BPM, Deck: t.Deck,
			StartOffsetMs: t.StartOffsetMs, EndOffsetMs: t.EndOffsetMs,
			CanonicalTrackID: t.CanonicalTrackID,
		})
	}
	return out
}

func (p *Publisher) info(msg string, f map[string]any) {
	if p.log != nil {
		p.log.Info(source, msg, f)
	}
}

func (p *Publisher) warn(what string, err error) {
	if p.log != nil {
		p.log.Warn(source, what+" failed", map[string]any{"error": err.Error()})
	}
}
