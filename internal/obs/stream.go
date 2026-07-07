package obs

// obs-websocket v5 stream + record output control + live status (bitrate/congestion/frames).
// Same request-correlator pattern as requests.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// StartStream starts the OBS stream output.
func (c *Client) StartStream(ctx context.Context) error {
	_, err := c.Request(ctx, "StartStream", nil)
	return err
}

// StopStream stops the OBS stream output.
func (c *Client) StopStream(ctx context.Context) error {
	_, err := c.Request(ctx, "StopStream", nil)
	return err
}

// ToggleStream toggles the stream output; returns the resulting active state.
func (c *Client) ToggleStream(ctx context.Context) (bool, error) {
	raw, err := c.Request(ctx, "ToggleStream", nil)
	if err != nil {
		return false, err
	}
	var v struct {
		OutputActive bool `json:"outputActive"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("obs ToggleStream decode: %w", err)
	}
	return v.OutputActive, nil
}

// StartRecord starts the OBS record output.
func (c *Client) StartRecord(ctx context.Context) error {
	_, err := c.Request(ctx, "StartRecord", nil)
	return err
}

// StopRecord stops the OBS record output.
func (c *Client) StopRecord(ctx context.Context) error {
	_, err := c.Request(ctx, "StopRecord", nil)
	return err
}

// ToggleRecord toggles the record output; returns the resulting active state.
func (c *Client) ToggleRecord(ctx context.Context) (bool, error) {
	raw, err := c.Request(ctx, "ToggleRecord", nil)
	if err != nil {
		return false, err
	}
	var v struct {
		OutputActive bool `json:"outputActive"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("obs ToggleRecord decode: %w", err)
	}
	return v.OutputActive, nil
}

// ToggleRecordPause pauses/unpauses the record output; returns the resulting paused state.
func (c *Client) ToggleRecordPause(ctx context.Context) (bool, error) {
	raw, err := c.Request(ctx, "ToggleRecordPause", nil)
	if err != nil {
		return false, err
	}
	var v struct {
		OutputPaused bool `json:"outputPaused"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("obs ToggleRecordPause decode: %w", err)
	}
	return v.OutputPaused, nil
}

// ToggleInputMute toggles the mute state of a named input/source; returns the resulting muted state.
func (c *Client) ToggleInputMute(ctx context.Context, inputName string) (bool, error) {
	raw, err := c.Request(ctx, "ToggleInputMute", map[string]any{"inputName": inputName})
	if err != nil {
		return false, err
	}
	var v struct {
		InputMuted bool `json:"inputMuted"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("obs ToggleInputMute decode: %w", err)
	}
	return v.InputMuted, nil
}

// StreamStatus mirrors GetStreamStatus: stream-output liveness + the data the cockpit shows.
// OBS exposes cumulative OutputBytes (not a bitrate) - compute kbps from the delta over time;
// Congestion is 0..1 (network strain); SkippedFrames/TotalFrames give the drop ratio.
type StreamStatus struct {
	Active       bool
	Reconnecting bool
	Duration     time.Duration
	Bytes        int64
	Congestion   float64
	Skipped      int
	Total        int
}

// GetStreamStatus returns the current OBS stream-output state.
func (c *Client) GetStreamStatus(ctx context.Context) (StreamStatus, error) {
	raw, err := c.Request(ctx, "GetStreamStatus", nil)
	if err != nil {
		return StreamStatus{}, err
	}
	var v struct {
		OutputActive        bool    `json:"outputActive"`
		OutputReconnecting  bool    `json:"outputReconnecting"`
		OutputDuration      float64 `json:"outputDuration"` // ms
		OutputBytes         int64   `json:"outputBytes"`
		OutputCongestion    float64 `json:"outputCongestion"`
		OutputSkippedFrames int     `json:"outputSkippedFrames"`
		OutputTotalFrames   int     `json:"outputTotalFrames"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return StreamStatus{}, fmt.Errorf("obs GetStreamStatus decode: %w", err)
	}
	return StreamStatus{
		Active:       v.OutputActive,
		Reconnecting: v.OutputReconnecting,
		Duration:     time.Duration(v.OutputDuration) * time.Millisecond,
		Bytes:        v.OutputBytes,
		Congestion:   v.OutputCongestion,
		Skipped:      v.OutputSkippedFrames,
		Total:        v.OutputTotalFrames,
	}, nil
}
