package api

// Library metadata bulk upload (POST /library/tracks/bulk): rave-mate pushes the user's local
// library tags so the backend can mirror them + link canonical tracks. The endpoint is newer
// than the generated spec, so the call is hand-written over the same redacted-logging doer
// (method/path/status only - never tokens or payloads).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// maxLibraryBulk is the backend's per-request item cap.
const maxLibraryBulk = 500

// LibraryCue is one cue/hotcue/loop on the wire (backend dto.CuePoint).
type LibraryCue struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Type    int     `json:"type"`
	StartMs float64 `json:"start_ms"`
	LenMs   float64 `json:"len_ms"`
	Hotcue  int     `json:"hotcue"` // -1 = not a hotcue
}

// LibraryGridMarker is one beatgrid marker on the wire (backend dto.BeatgridMarker).
type LibraryGridMarker struct {
	PositionMs int64   `json:"position_ms"`
	BPM        float64 `json:"bpm"`
	Beat       int     `json:"beat"` // 1 = downbeat anchor (Traktor grids)
}

// LibraryTrack is one local track's uploaded metadata. Everything but Title is omitempty.
type LibraryTrack struct {
	Title          string              `json:"title"`
	ArtistText     string              `json:"artist_text,omitempty"`
	Album          string              `json:"album,omitempty"`
	Label          string              `json:"label,omitempty"`
	Genre          string              `json:"genre,omitempty"`
	BPM            float64             `json:"bpm,omitempty"`
	Key            string              `json:"key,omitempty"`
	DurationMs     int                 `json:"duration_ms,omitempty"`
	ISRC           string              `json:"isrc,omitempty"`
	ReleaseYear    int                 `json:"release_year,omitempty"`
	FingerprintB64 string              `json:"fingerprint_b64,omitempty"`
	Rating         int                 `json:"rating,omitempty"` // 0-5
	PlayCount      int                 `json:"play_count,omitempty"`
	Comment        string              `json:"comment,omitempty"`
	LastPlayedAt   string              `json:"last_played_at,omitempty"` // RFC3339
	Cues           []LibraryCue        `json:"cues,omitempty"`
	Beatgrid       []LibraryGridMarker `json:"beatgrid,omitempty"`
	// DropsMs = DJ-marked drop points (ms from track start, sorted asc, deduped).
	// nil = field omitted (server keeps stored drops); non-nil empty = explicit clear.
	DropsMs []int64 `json:"drops_ms,omitzero"`
}

// LibraryBulkResult is the per-row outcome; Index points into the request batch. JSON nulls
// (canonical_track_id, match_confidence, error) decode to zero values.
type LibraryBulkResult struct {
	Index             int     `json:"index"`
	LibraryTrackID    string  `json:"library_track_id"`
	CanonicalTrackID  string  `json:"canonical_track_id"` // "" = no match
	MatchConfidence   float64 `json:"match_confidence"`
	MatchSource       string  `json:"match_source"`
	FingerprintStatus string  `json:"fingerprint_status"`
	Status            string  `json:"status"` // created|updated|error
	Error             string  `json:"error"`
}

// LibraryBulkSummary aggregates one bulk request.
type LibraryBulkSummary struct {
	Received int `json:"received"`
	Created  int `json:"created"`
	Updated  int `json:"updated"`
	Matched  int `json:"matched"`
	Failed   int `json:"failed"`
}

// LibraryBulkResp is the bulk-upload response.
type LibraryBulkResp struct {
	Results []LibraryBulkResult `json:"results"`
	Summary LibraryBulkSummary  `json:"summary"`
}

// UploadLibraryTracks bulk-uploads local library metadata (authed). Hard cap 500 per request;
// callers batch below that (playsync sends 200).
func (c *Client) UploadLibraryTracks(ctx context.Context, token string, tracks []LibraryTrack) (LibraryBulkResp, error) {
	if token == "" {
		return LibraryBulkResp{}, fmt.Errorf("library upload: unauthenticated")
	}
	if len(tracks) == 0 {
		return LibraryBulkResp{}, fmt.Errorf("library upload: empty batch")
	}
	if len(tracks) > maxLibraryBulk {
		return LibraryBulkResp{}, fmt.Errorf("library upload: batch %d exceeds %d", len(tracks), maxLibraryBulk)
	}
	body, err := json.Marshal(map[string]any{"tracks": tracks})
	if err != nil {
		return LibraryBulkResp{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/library/tracks/bulk", bytes.NewReader(body))
	if err != nil {
		return LibraryBulkResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.bulkDoer.Do(req)
	if err != nil {
		return LibraryBulkResp{}, err
	}
	var out LibraryBulkResp
	err = decode(resp, &out)
	return out, err
}

// LibraryTrackOut is one server library row from GET /library (subset we consume).
type LibraryTrackOut struct {
	ID         string `json:"id"` // lib_…
	Title      string `json:"title"`
	ArtistText string `json:"artist_text"`
	ISRC       string `json:"isrc"`
}

// ListLibraryTracks pages the caller's server library (GET /library). limit caps at the
// backend's 200/page.
func (c *Client) ListLibraryTracks(ctx context.Context, token string, limit, offset int) ([]LibraryTrackOut, error) {
	if token == "" {
		return nil, fmt.Errorf("library list: unauthenticated")
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/library?limit=%d&offset=%d", c.base, limit, offset), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.bulkDoer.Do(req)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tracks []LibraryTrackOut `json:"tracks"`
	}
	err = decode(resp, &out)
	return out.Tracks, err
}

// UploadTrackWaveform PUTs analyzed peak buckets for a server library row (owner only).
// peaksB64 = base64 raw uint8 buckets (decoded 1–65536 bytes); durationMs 0 preserves stored.
// Returns the server-confirmed bucket count.
func (c *Client) UploadTrackWaveform(ctx context.Context, token, libraryTrackID, peaksB64 string, durationMs int) (int, error) {
	if token == "" {
		return 0, fmt.Errorf("waveform upload: unauthenticated")
	}
	if libraryTrackID == "" || peaksB64 == "" {
		return 0, fmt.Errorf("waveform upload: missing id or peaks")
	}
	body, err := json.Marshal(map[string]any{"peaks_b64": peaksB64, "duration_ms": durationMs})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.base+"/library/tracks/"+libraryTrackID+"/waveform", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.bulkDoer.Do(req)
	if err != nil {
		return 0, err
	}
	var out struct {
		Buckets int `json:"buckets"`
	}
	err = decode(resp, &out)
	return out.Buckets, err
}

// UploadTrackArtwork PUTs raw cover-art bytes for a server library row (owner only).
// contentType must be image/jpeg|png|webp; max 262144 bytes (caller recompresses above).
func (c *Client) UploadTrackArtwork(ctx context.Context, token, libraryTrackID, contentType string, data []byte) error {
	if token == "" {
		return fmt.Errorf("artwork upload: unauthenticated")
	}
	if libraryTrackID == "" || len(data) == 0 {
		return fmt.Errorf("artwork upload: missing id or data")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.base+"/library/tracks/"+libraryTrackID+"/artwork", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.bulkDoer.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, nil)
}
