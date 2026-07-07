package obs

// obs-websocket v5 media-input + audio-sync + source-filter requests, used by the media-sync
// tier (chase a media source's playback cursor to a house clock). Same request-correlator
// pattern as inputs.go/stream.go. Cursor/offset/duration are all milliseconds.
//
// Gotchas encoded here (verified, DMX_TIMECODE_RESEARCH.md § OBS):
//   - PLAY only un-pauses; a stopped/ended source won't cold-start from PLAY → use RESTART.
//   - Media Source (ffmpeg_source) seeks snap to the nearest keyframe (40–80ms error); a
//     VLC Video Source (vlc_source) seeks ms-exact → prefer vlc_source for tight sync.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Media input kinds (InputInfo.Kind) relevant to sync.
const (
	KindVLCSource    = "vlc_source"    // ms-exact seeks - the good one
	KindMediaSource  = "ffmpeg_source" // OBS "Media Source" - keyframe-snapped seeks (40–80ms)
	KindMediaSource2 = "ffmpeg_source_v2"
)

// Media states (MediaInputStatus.State).
const (
	MediaStateNone      = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_NONE"
	MediaStatePlaying   = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_PLAYING"
	MediaStateOpening   = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_OPENING"
	MediaStateBuffering = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_BUFFERING"
	MediaStatePaused    = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_PAUSED"
	MediaStateStopped   = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_STOPPED"
	MediaStateEnded     = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_ENDED"
	MediaStateError     = "OBS_WEBSOCKET_MEDIA_INPUT_STATE_ERROR"
)

// Media actions (TriggerMediaInputAction). PLAY is un-pause only - cold start = RESTART.
const (
	MediaActionNone     = "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_NONE"
	MediaActionPlay     = "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_PLAY"
	MediaActionPause    = "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_PAUSE"
	MediaActionStop     = "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_STOP"
	MediaActionRestart  = "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_RESTART"
	MediaActionNext     = "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_NEXT"
	MediaActionPrevious = "OBS_WEBSOCKET_MEDIA_INPUT_ACTION_PREVIOUS"
)

// SeekKeyframeSnapped reports whether seeks on this input kind snap to the nearest keyframe
// (Media Source) rather than being frame-exact (VLC source). Drives the UI accuracy hint.
func SeekKeyframeSnapped(kind string) bool {
	return kind == KindMediaSource || kind == KindMediaSource2
}

// ── request-data shapes (exported-field structs; marshalled into requestData) ──

type setInputAudioSyncOffsetData struct {
	InputName            string `json:"inputName"`
	InputAudioSyncOffset int    `json:"inputAudioSyncOffset"` // ms, −950…+20000
}

type getMediaInputStatusData struct {
	InputName string `json:"inputName"`
}

type setMediaInputCursorData struct {
	InputName   string `json:"inputName"`
	MediaCursor int    `json:"mediaCursor"` // ms from start
}

type offsetMediaInputCursorData struct {
	InputName         string `json:"inputName"`
	MediaCursorOffset int    `json:"mediaCursorOffset"` // ms delta (±)
}

type triggerMediaInputActionData struct {
	InputName   string `json:"inputName"`
	MediaAction string `json:"mediaAction"`
}

type setSourceFilterSettingsData struct {
	SourceName     string         `json:"sourceName"`
	FilterName     string         `json:"filterName"`
	FilterSettings map[string]any `json:"filterSettings"`
	Overlay        bool           `json:"overlay"`
}

type createSourceFilterData struct {
	SourceName     string         `json:"sourceName"`
	FilterName     string         `json:"filterName"`
	FilterKind     string         `json:"filterKind"`
	FilterSettings map[string]any `json:"filterSettings,omitempty"`
}

type setSourceFilterEnabledData struct {
	SourceName    string `json:"sourceName"`
	FilterName    string `json:"filterName"`
	FilterEnabled bool   `json:"filterEnabled"`
}

type getSourceFilterListData struct {
	SourceName string `json:"sourceName"`
}

// Delay-filter kinds (FilterInfo.Kind) used for per-source video delay compensation.
const (
	FilterKindGPUDelay   = "gpu_delay"          // Render Delay: ≤500ms, works on any source incl. Media Source
	FilterKindAsyncDelay = "async_delay_filter" // Video Delay (Async): ≤20s, NOT valid on Media Sources
)

// FilterInfo is one entry from GetSourceFilterList.
type FilterInfo struct {
	Enabled  bool           `json:"filterEnabled"`
	Index    int            `json:"filterIndex"`
	Kind     string         `json:"filterKind"`
	Name     string         `json:"filterName"`
	Settings map[string]any `json:"filterSettings"`
}

// MediaInputStatus mirrors GetMediaInputStatus: playback state + cursor/duration.
type MediaInputStatus struct {
	State    string        // one of MediaState*
	Duration time.Duration // total media length (0 if unknown/-1)
	Cursor   time.Duration // current playback position
}

// Playing reports whether the media is actively advancing (playing or buffering into play).
func (s MediaInputStatus) Playing() bool {
	return s.State == MediaStatePlaying || s.State == MediaStateBuffering
}

// ── requests ──

// SetInputAudioSyncOffset sets an input's audio sync offset in ms (OBS range −950…+20000).
// Positive delays the audio relative to video; use to align a media source's audio to the mix.
func (c *Client) SetInputAudioSyncOffset(ctx context.Context, inputName string, offsetMs int) error {
	_, err := c.Request(ctx, "SetInputAudioSyncOffset",
		setInputAudioSyncOffsetData{InputName: inputName, InputAudioSyncOffset: offsetMs})
	return err
}

// GetMediaInputStatus returns a media input's playback state, total duration, and cursor.
// OBS reports duration/cursor as ms floats; −1 means unknown → mapped to 0.
func (c *Client) GetMediaInputStatus(ctx context.Context, inputName string) (MediaInputStatus, error) {
	raw, err := c.Request(ctx, "GetMediaInputStatus", getMediaInputStatusData{InputName: inputName})
	if err != nil {
		return MediaInputStatus{}, err
	}
	var v struct {
		State    string  `json:"mediaState"`
		Duration float64 `json:"mediaDuration"` // ms (−1 = unknown)
		Cursor   float64 `json:"mediaCursor"`   // ms (−1 = unknown)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return MediaInputStatus{}, fmt.Errorf("obs GetMediaInputStatus decode: %w", err)
	}
	return MediaInputStatus{
		State:    v.State,
		Duration: msToDur(v.Duration),
		Cursor:   msToDur(v.Cursor),
	}, nil
}

// SetMediaInputCursor seeks a media input to an absolute position (ms from start).
func (c *Client) SetMediaInputCursor(ctx context.Context, inputName string, cursorMs int) error {
	_, err := c.Request(ctx, "SetMediaInputCursor",
		setMediaInputCursorData{InputName: inputName, MediaCursor: cursorMs})
	return err
}

// OffsetMediaInputCursor nudges a media input's cursor by a signed ms delta (relative seek).
func (c *Client) OffsetMediaInputCursor(ctx context.Context, inputName string, offsetMs int) error {
	_, err := c.Request(ctx, "OffsetMediaInputCursor",
		offsetMediaInputCursorData{InputName: inputName, MediaCursorOffset: offsetMs})
	return err
}

// TriggerMediaInputAction triggers a media action (use MediaAction* constants).
// Reminder: MediaActionPlay only un-pauses - cold-start a stopped/ended source with MediaActionRestart.
func (c *Client) TriggerMediaInputAction(ctx context.Context, inputName, action string) error {
	_, err := c.Request(ctx, "TriggerMediaInputAction",
		triggerMediaInputActionData{InputName: inputName, MediaAction: action})
	return err
}

// SetSourceFilterSettings updates a source filter's settings (overlay=true merges, keeps
// unspecified keys). Used for delay filters: filterName of a "gpu_delay" (≤500ms) or
// "async_delay_filter" (≤20s, not valid on Media Sources) with {"delay_ms": n}.
func (c *Client) SetSourceFilterSettings(ctx context.Context, sourceName, filterName string, settings map[string]any, overlay bool) error {
	_, err := c.Request(ctx, "SetSourceFilterSettings", setSourceFilterSettingsData{
		SourceName: sourceName, FilterName: filterName, FilterSettings: settings, Overlay: overlay,
	})
	return err
}

// CreateSourceFilter adds a filter to a source. filterKind = FilterKindGPUDelay (≤500ms, any
// source) or FilterKindAsyncDelay (≤20s, not Media Sources); settings e.g. {"delay_ms": n}.
// Errors if a filter of the same name already exists on the source (600).
func (c *Client) CreateSourceFilter(ctx context.Context, sourceName, filterName, filterKind string, settings map[string]any) error {
	_, err := c.Request(ctx, "CreateSourceFilter", createSourceFilterData{
		SourceName: sourceName, FilterName: filterName, FilterKind: filterKind, FilterSettings: settings,
	})
	return err
}

// SetSourceFilterEnabled enables/disables a source filter by name.
func (c *Client) SetSourceFilterEnabled(ctx context.Context, sourceName, filterName string, enabled bool) error {
	_, err := c.Request(ctx, "SetSourceFilterEnabled", setSourceFilterEnabledData{
		SourceName: sourceName, FilterName: filterName, FilterEnabled: enabled,
	})
	return err
}

// GetSourceFilterList returns a source's filters (name/kind/enabled/settings), in filter order.
func (c *Client) GetSourceFilterList(ctx context.Context, sourceName string) ([]FilterInfo, error) {
	raw, err := c.Request(ctx, "GetSourceFilterList", getSourceFilterListData{SourceName: sourceName})
	if err != nil {
		return nil, err
	}
	var v struct {
		Filters []FilterInfo `json:"filters"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("obs GetSourceFilterList decode: %w", err)
	}
	return v.Filters, nil
}

// msToDur converts an OBS ms float (−1 = unknown) to a Duration (unknown → 0).
func msToDur(ms float64) time.Duration {
	if ms < 0 {
		return 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}
