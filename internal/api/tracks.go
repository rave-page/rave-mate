package api

// Play/tracklist-layer endpoints (TracksService): fingerprint identification + provisional
// catalog growth. These back rave-mate's play-layer sync (internal/playsync): every captured
// played track is fingerprinted, identified against the canonical corpus, and - on a miss -
// seeded as a provisional track so future plays link. Bodies are well-typed in the OAS 3
// spec, so these use the generated typed methods directly (unlike streams, which hand-marshals
// around the freeform-field mis-typing).

import (
	"context"
	"fmt"
	"sort"

	"rave.page/mate/internal/apiclient"
)

// FpCandidate is one acoustic-match candidate for a queried fingerprint.
type FpCandidate struct {
	TrackID    string
	Confidence float64
	BitError   float64
}

// IdentifyFingerprint returns canonical-track candidates for a Chromaprint fingerprint, best
// (highest confidence) first. Anonymous-public corpus lookup - token optional (pass "" when
// unauthed). An empty result means no acoustic match (caller seeds a provisional track).
func (c *Client) IdentifyFingerprint(ctx context.Context, token, fingerprintB64 string, limit int) ([]FpCandidate, error) {
	if fingerprintB64 == "" {
		return nil, fmt.Errorf("identify: empty fingerprint")
	}
	body := apiclient.FingerprintIdentifyIn{FingerprintB64: &fingerprintB64}
	if limit > 0 {
		body.Limit = &limit
	}
	resp, err := c.gen.IdentifyTrackByFingerprint(ctx, body, bearer(token))
	if err != nil {
		return nil, err
	}
	var out apiclient.FingerprintIdentifyOut
	if err := decode(resp, &out); err != nil {
		return nil, err
	}
	if out.Candidates == nil {
		return nil, nil
	}
	cands := make([]FpCandidate, 0, len(*out.Candidates))
	for _, cd := range *out.Candidates {
		fc := FpCandidate{}
		if cd.TrackId != nil {
			fc.TrackID = *cd.TrackId
		}
		if cd.Confidence != nil {
			fc.Confidence = float64(*cd.Confidence)
		}
		if cd.BitErrorRate != nil {
			fc.BitError = float64(*cd.BitErrorRate)
		}
		if fc.TrackID != "" {
			cands = append(cands, fc)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Confidence > cands[j].Confidence })
	return cands, nil
}

// SubmitFingerprint attaches a locally-computed Chromaprint to a canonical track (authed).
// durationMs is required (>=1) by the corpus lookup; sampleRate defaults to Chromaprint's
// canonical 11025 when 0.
func (c *Client) SubmitFingerprint(ctx context.Context, token, trackID, fingerprintB64 string, durationMs, sampleRate int) error {
	if token == "" {
		return fmt.Errorf("submit fingerprint: unauthenticated")
	}
	if trackID == "" || fingerprintB64 == "" {
		return fmt.Errorf("submit fingerprint: missing track or fingerprint")
	}
	if durationMs < 1 {
		durationMs = 1
	}
	if sampleRate == 0 {
		sampleRate = 11025
	}
	body := apiclient.TrackFingerprintCreateIn{
		FingerprintB64: &fingerprintB64, DurationMs: &durationMs, SampleRate: &sampleRate,
	}
	resp, err := c.gen.CreateTrackFingerprint(ctx, trackID, body, bearer(token))
	if err != nil {
		return err
	}
	return decode(resp, nil)
}

// NewTrack is the provisional-track seed for an unmatched played track.
type NewTrack struct {
	Title      string
	DurationMs int
	ISRC       string
	Source     string // defaults to "rave-mate" when empty
}

// CreateProvisionalTrack mints a (provisional, non-canonical) track for an unmatched play and
// returns its id (authed). The backend triages provisional tracks; promote/merge are admin ops.
func (c *Client) CreateProvisionalTrack(ctx context.Context, token string, t NewTrack) (string, error) {
	if token == "" {
		return "", fmt.Errorf("create track: unauthenticated")
	}
	if t.Title == "" {
		return "", fmt.Errorf("create track: empty title")
	}
	src := t.Source
	if src == "" {
		src = "rave-mate"
	}
	body := apiclient.TrackCreateIn{Title: &t.Title, Source: &src}
	if t.DurationMs > 0 {
		body.DurationMs = &t.DurationMs
	}
	if t.ISRC != "" {
		body.Isrc = &t.ISRC
	}
	resp, err := c.gen.CreateTrack(ctx, body, bearer(token))
	if err != nil {
		return "", err
	}
	var out apiclient.TrackOut
	if err := decode(resp, &out); err != nil {
		return "", err
	}
	if out.Id == nil || *out.Id == "" {
		return "", fmt.Errorf("create track: no id in response")
	}
	return *out.Id, nil
}

// Observation is a crowd-sourced metadata report for a track (feeds per-field consensus).
type Observation struct {
	Title      string
	ArtistText string
	Album      string
	Key        string
	BPM        float64
	DurationMs int
}

// ReportObservation submits one metadata observation against a track (authed). Best-effort
// enrichment - at least one field must be set.
func (c *Client) ReportObservation(ctx context.Context, token, trackID string, o Observation) error {
	if token == "" {
		return fmt.Errorf("observation: unauthenticated")
	}
	if trackID == "" {
		return fmt.Errorf("observation: missing track")
	}
	body := apiclient.MetadataObservationIn{}
	if o.Title != "" {
		body.Title = &o.Title
	}
	if o.ArtistText != "" {
		body.ArtistText = &o.ArtistText
	}
	if o.Album != "" {
		body.Album = &o.Album
	}
	if o.Key != "" {
		body.Key = &o.Key
	}
	if o.BPM > 0 {
		bpm := float32(o.BPM)
		body.Bpm = &bpm
	}
	if o.DurationMs > 0 {
		body.DurationMs = &o.DurationMs
	}
	resp, err := c.gen.CreateTrackObservation(ctx, trackID, body, bearer(token))
	if err != nil {
		return err
	}
	return decode(resp, nil)
}
