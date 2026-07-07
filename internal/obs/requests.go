package obs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Profile/scene-collection/settings request methods live in settings.go.

// RecordStatus mirrors GetRecordStatus: whether OBS is recording and for how long.
type RecordStatus struct {
	Active   bool
	Paused   bool
	Duration time.Duration // elapsed recording time
	Bytes    int64
}

// GetRecordStatus returns the current OBS record-output state.
func (c *Client) GetRecordStatus(ctx context.Context) (RecordStatus, error) {
	raw, err := c.Request(ctx, "GetRecordStatus", nil)
	if err != nil {
		return RecordStatus{}, err
	}
	var v struct {
		OutputActive   bool    `json:"outputActive"`
		OutputPaused   bool    `json:"outputPaused"`
		OutputDuration float64 `json:"outputDuration"` // ms
		OutputBytes    int64   `json:"outputBytes"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return RecordStatus{}, fmt.Errorf("obs GetRecordStatus decode: %w", err)
	}
	return RecordStatus{
		Active:   v.OutputActive,
		Paused:   v.OutputPaused,
		Duration: time.Duration(v.OutputDuration) * time.Millisecond,
		Bytes:    v.OutputBytes,
	}, nil
}

// StreamEncoder returns the OBS-configured stream + record encoder ids (raw OBS ids like
// "jim_nvenc", "h264_texture_amf", "obs_qsv11", "obs_x264"/"x264"). Reads the active output mode
// ("Simple"/"Advanced") then the matching key. Record encoder is best-effort ("" if unset).
func (c *Client) StreamEncoder() (stream, record string, err error) {
	ctx := context.Background()
	mode, err := c.GetProfileParameter(ctx, "Output", "Mode")
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(mode, "Advanced") {
		stream, _ = c.GetProfileParameter(ctx, "AdvOut", "Encoder")
		record, _ = c.GetProfileParameter(ctx, "AdvOut", "RecEncoder")
		return stream, record, nil
	}
	// Simple mode (default): StreamEncoder / RecEncoder under SimpleOutput.
	stream, _ = c.GetProfileParameter(ctx, "SimpleOutput", "StreamEncoder")
	record, _ = c.GetProfileParameter(ctx, "SimpleOutput", "RecEncoder")
	return stream, record, nil
}

// StreamRequirements is the set of encoding targets the rave.page API expects for a stream.
// DefaultStreamRequirements returns sensible placeholders; the lead wires real values from the API.
type StreamRequirements struct {
	VideoBitrateKbps int    // minimum video bitrate in kbps
	KeyframeSec      int    // required keyframe interval in seconds
	Encoder          string // OBS encoder id (e.g. "obs_x264", "ffmpeg_nvenc")
}

// DefaultStreamRequirements returns conservative placeholder requirements.
// Lead: replace with values from the rave.page stream-requirements API endpoint (TBD).
func DefaultStreamRequirements() StreamRequirements {
	return StreamRequirements{
		VideoBitrateKbps: 6000,
		KeyframeSec:      2,
		Encoder:          "obs_x264",
	}
}

// ValidateStreamSettings checks current OBS settings against req and returns human-readable
// diff strings. Empty slice means compliant.
//
// Bitrate + keyframe: queried via GetProfileParameter from the "SimpleOutput" category
// (OBS simple-mode keys: "VBitrate" for bitrate kbps, "KeyframeInterval" for interval).
// If the profile uses advanced output mode the keys live under "Output" category instead -
// we try "SimpleOutput" first, then "Output" (best effort; comment each case).
// Encoder: from "SimpleOutput"/"StreamEncoder" in simple mode.
//
// NOTE: OBS does not expose a single "current bitrate" endpoint; GetProfileParameter is
// the v5-sanctioned way to read profile INI values. Key names depend on the output mode
// the user has selected - this is a best-effort implementation.
func (c *Client) ValidateStreamSettings(req StreamRequirements) ([]string, error) {
	ctx := context.Background()
	var diffs []string

	// ── bitrate ──────────────────────────────────────────────────────────────
	// Try SimpleOutput first (simple mode), then Output (advanced mode).
	bitrateStr, err := c.GetProfileParameter(ctx, "SimpleOutput", "VBitrate")
	if err != nil || bitrateStr == "" {
		// advanced output mode: bitrate key is "bitrate" under "Output" section.
		bitrateStr, _ = c.GetProfileParameter(ctx, "Output", "bitrate")
	}
	if bitrateStr != "" {
		bitrate, convErr := strconv.Atoi(strings.TrimSpace(bitrateStr))
		if convErr == nil && bitrate < req.VideoBitrateKbps {
			diffs = append(diffs, fmt.Sprintf(
				"video bitrate %d kbps < required %d kbps", bitrate, req.VideoBitrateKbps))
		}
	}

	// ── keyframe interval ─────────────────────────────────────────────────────
	// SimpleOutput: "KeyframeInterval"; advanced Output: "keyint_sec".
	kfStr, err := c.GetProfileParameter(ctx, "SimpleOutput", "KeyframeInterval")
	if err != nil || kfStr == "" {
		kfStr, _ = c.GetProfileParameter(ctx, "Output", "keyint_sec")
	}
	if kfStr != "" {
		kf, convErr := strconv.Atoi(strings.TrimSpace(kfStr))
		if convErr == nil && kf != req.KeyframeSec {
			diffs = append(diffs, fmt.Sprintf(
				"keyframe interval %ds != required %ds", kf, req.KeyframeSec))
		}
	}

	// ── encoder ──────────────────────────────────────────────────────────────
	// "StreamEncoder" in SimpleOutput; advanced stores it elsewhere per-output.
	encStr, _ := c.GetProfileParameter(ctx, "SimpleOutput", "StreamEncoder")
	if encStr == "" {
		encStr, _ = c.GetProfileParameter(ctx, "Output", "encoder")
	}
	if encStr != "" && !strings.EqualFold(strings.TrimSpace(encStr), req.Encoder) {
		diffs = append(diffs, fmt.Sprintf(
			"encoder %q != required %q", strings.TrimSpace(encStr), req.Encoder))
	}

	return diffs, nil
}
