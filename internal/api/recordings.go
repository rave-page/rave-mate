package api

// Recordings: publish a finished DJ set to rave.page as a first-class recording (audio +
// tracklist offsets + waveform + loudness). Replaces the backdated live-stream ingest fallback
// in playsync.UploadRecordedSet. Hand-written over the redacted-logging doers - the endpoints
// are newer than the generated spec.
//
//	POST /recordings                  → {id, tracklist_id}
//	PUT  /recordings/{id}/audio       {media_upload_id}    (after the upload reports ready)
//	PUT  /recordings/{id}/waveform    {peaks_b64, bands_b64, duration_ms}
//	PUT  /recordings/{id}/loudness    {integrated_lufs, true_peak_db, lra, step_ms, momentary_b64}

import (
	"context"
	"fmt"
	"net/http"
)

// Recording visibility values.
const (
	VisibilityPublic   = "public"
	VisibilityUnlisted = "unlisted"
	VisibilityPrivate  = "private"
)

// SourceRecorded marks a set captured locally and uploaded (vs. an ingested live stream).
const SourceRecorded = "recorded"

// RecordingTrackIn is one tracklist entry. Offsets are milliseconds into the audio file
// (track.StartedAt − capture.StartedAt, clamped ≥ 0), NOT wall-clock times.
type RecordingTrackIn struct {
	Number           int     `json:"number"` // 1-based play order
	Title            string  `json:"title"`
	Artist           string  `json:"artist,omitempty"`
	Album            string  `json:"album,omitempty"`
	Key              string  `json:"key,omitempty"`
	BPM              float64 `json:"bpm,omitempty"`
	Deck             string  `json:"deck,omitempty"`
	StartOffsetMs    int64   `json:"start_offset_ms"`
	EndOffsetMs      int64   `json:"end_offset_ms,omitempty"`
	CanonicalTrackID string  `json:"canonical_track_id,omitempty"` // libdb.track_links resolution
}

// CreateRecordingReq creates the recording + its tracklist in one call. RightsConfirmed must be
// true - the uploader affirms they hold the rights/clearance (the UI gates on an explicit
// checkbox; this field is that consent on the wire).
type CreateRecordingReq struct {
	Title           string             `json:"title"`
	StartedAt       string             `json:"started_at"` // RFC3339 UTC
	EndedAt         string             `json:"ended_at"`   // RFC3339 UTC
	Visibility      string             `json:"visibility"`
	Source          string             `json:"source"` // SourceRecorded
	PerformerID     string             `json:"performer_id,omitempty"`
	EventID         string             `json:"event_id,omitempty"`
	RightsConfirmed bool               `json:"rights_confirmed"`
	Tracklist       []RecordingTrackIn `json:"tracklist"`
}

// CreateRecordingResp identifies the created recording + its tracklist.
type CreateRecordingResp struct {
	ID          string `json:"id"`
	TracklistID string `json:"tracklist_id"`
}

// RecordingLoudness is the EBU R128 summary + momentary timeline for a recording.
// MomentaryB64 is base64 of a little-endian float32 array, one sample per StepMs.
type RecordingLoudness struct {
	IntegratedLUFS float64 `json:"integrated_lufs"`
	TruePeakDB     float64 `json:"true_peak_db"`
	LRA            float64 `json:"lra"`
	StepMs         int     `json:"step_ms"`
	MomentaryB64   string  `json:"momentary_b64"`
}

// CreateRecording publishes the recording shell + tracklist. Rights confirmation is mandatory.
func (c *Client) CreateRecording(ctx context.Context, token string, req CreateRecordingReq) (CreateRecordingResp, error) {
	if req.Title == "" {
		return CreateRecordingResp{}, fmt.Errorf("create recording: missing title")
	}
	if !req.RightsConfirmed {
		return CreateRecordingResp{}, fmt.Errorf("create recording: rights not confirmed")
	}
	if req.Source == "" {
		req.Source = SourceRecorded
	}
	if req.Visibility == "" {
		req.Visibility = VisibilityPrivate
	}
	var out CreateRecordingResp
	if err := c.doJSON(ctx, http.MethodPost, c.base+"/recordings", token, req, &out, c.bulkDoer); err != nil {
		return CreateRecordingResp{}, err
	}
	if out.ID == "" {
		return CreateRecordingResp{}, fmt.Errorf("create recording: server returned no id")
	}
	return out, nil
}

// SetRecordingAudio attaches a completed media upload as the recording's audio. Call only once
// the upload reports status=ready.
func (c *Client) SetRecordingAudio(ctx context.Context, token, recordingID, mediaUploadID string) error {
	if recordingID == "" || mediaUploadID == "" {
		return fmt.Errorf("recording audio: missing recording or upload id")
	}
	body := map[string]string{"media_upload_id": mediaUploadID}
	return c.doJSON(ctx, http.MethodPut, c.base+"/recordings/"+recordingID+"/audio", token, body, nil, c.bulkDoer)
}

// SetRecordingWaveform PUTs the set's peak overview. peaksB64 = base64 uint8 max-abs buckets;
// bandsB64 = optional 3-bytes-per-bucket spectral bands (the web app's layered waveform).
func (c *Client) SetRecordingWaveform(ctx context.Context, token, recordingID, peaksB64, bandsB64 string, durationMs int) error {
	if recordingID == "" || peaksB64 == "" {
		return fmt.Errorf("recording waveform: missing recording id or peaks")
	}
	body := map[string]any{"peaks_b64": peaksB64, "duration_ms": durationMs}
	if bandsB64 != "" {
		body["bands_b64"] = bandsB64
	}
	return c.doJSON(ctx, http.MethodPut, c.base+"/recordings/"+recordingID+"/waveform", token, body, nil, c.bulkDoer)
}

// SetRecordingLoudness PUTs the R128 summary + momentary timeline.
func (c *Client) SetRecordingLoudness(ctx context.Context, token, recordingID string, l RecordingLoudness) error {
	if recordingID == "" {
		return fmt.Errorf("recording loudness: missing recording id")
	}
	return c.doJSON(ctx, http.MethodPut, c.base+"/recordings/"+recordingID+"/loudness", token, l, nil, c.bulkDoer)
}
