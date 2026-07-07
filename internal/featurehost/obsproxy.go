package featurehost

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/obs"
)

// ObsConfig builds the child's init params (re-read on every (re)spawn).
type ObsConfig = obsInit

// ObsStatus mirrors the child OBS bridge's state for the UI.
type ObsStatus struct {
	Connected    bool
	Recording    bool
	RecStartedAt time.Time
}

// ObsRecording is one finished OBS recording reported by the child.
type ObsRecording = obsRecEvent

// ObsProxy is the daemon-side stand-in for the subprocessed OBS bridge: a status mirror
// for the cockpit + finished-recording events for tracklist linking.
type ObsProxy struct {
	host *Host

	mu     sync.Mutex
	status ObsStatus

	recMu   sync.Mutex
	recSubs map[int]chan ObsRecording
	nextSub int
}

// NewObsProxy builds the proxy + its host. init is re-evaluated per (re)spawn, so a
// settings edit takes effect on module restart.
func NewObsProxy(log *logbus.Bus, initFn func() ObsConfig) (*ObsProxy, error) {
	p := &ObsProxy{recSubs: map[int]chan ObsRecording{}}
	h, err := New(Options{
		Name: "obs",
		Log:  log,
		Init: func() any { return initFn() },
		OnEvent: map[string]func(json.RawMessage){
			"state": func(data json.RawMessage) {
				var st obsState
				if json.Unmarshal(data, &st) != nil {
					return
				}
				p.mu.Lock()
				p.status = ObsStatus(st)
				p.mu.Unlock()
			},
			"rec": func(data json.RawMessage) {
				var r ObsRecording
				if json.Unmarshal(data, &r) != nil {
					return
				}
				p.recMu.Lock()
				subs := make([]chan ObsRecording, 0, len(p.recSubs))
				for _, ch := range p.recSubs {
					subs = append(subs, ch)
				}
				p.recMu.Unlock()
				for _, ch := range subs {
					select {
					case ch <- r:
					default: // finished recordings are rare; drop on overflow
					}
				}
			},
		},
		OnDown: func() {
			p.mu.Lock()
			p.status = ObsStatus{}
			p.mu.Unlock()
		},
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	return p, nil
}

// Host exposes the supervising host (module Start/Stop, SetNotifier, Stats).
func (p *ObsProxy) Host() *Host { return p.host }

// Status mirrors the child bridge's state; zero (disconnected) while the child is down.
func (p *ObsProxy) Status() ObsStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.host.Running() {
		return ObsStatus{}
	}
	return p.status
}

// ── obs-websocket request forwarding (overlayobs.OBSClient surface) ──
// Methods forward to the live *obs.Client in the "obs" child via the host RPC. Method-name
// strings + param shapes are shared with feat_obs.go's Handle. A down child surfaces as a
// Call error, which the overlayobs sink treats as "OBS unavailable" and retries.

// obsRequester mirrors overlayobs.OBSClient locally (avoids importing overlayobs, which
// would form an import cycle). The compile-time assertion below keeps it in sync.
type obsRequester interface {
	GetCurrentProgramScene(ctx context.Context) (string, error)
	GetInputList(ctx context.Context, kind string) ([]obs.InputInfo, error)
	CreateInput(ctx context.Context, p obs.CreateInputParams) (int, error)
	SetInputSettings(ctx context.Context, inputName string, settings map[string]any, overlay bool) error
	GetSceneItemID(ctx context.Context, sceneName, sourceName string) (int, error)
	SetSceneItemEnabled(ctx context.Context, sceneName string, itemID int, enabled bool) error
	SetSceneItemTransform(ctx context.Context, sceneName string, itemID int, transform map[string]any) error
}

var _ obsRequester = (*ObsProxy)(nil)

// GetCurrentProgramScene returns the active program scene name.
func (p *ObsProxy) GetCurrentProgramScene(ctx context.Context) (string, error) {
	raw, err := p.host.Call(ctx, "getCurrentProgramScene", nil)
	if err != nil {
		return "", err
	}
	var scene string
	if err := json.Unmarshal(raw, &scene); err != nil {
		return "", err
	}
	return scene, nil
}

// GetInputList returns all inputs (optionally filtered by kind; "" = all).
func (p *ObsProxy) GetInputList(ctx context.Context, kind string) ([]obs.InputInfo, error) {
	raw, err := p.host.Call(ctx, "getInputList", obsGetInputListReq{Kind: kind})
	if err != nil {
		return nil, err
	}
	var inputs []obs.InputInfo
	if err := json.Unmarshal(raw, &inputs); err != nil {
		return nil, err
	}
	return inputs, nil
}

// CreateInput creates an input + scene item, returning the new sceneItemId.
func (p *ObsProxy) CreateInput(ctx context.Context, params obs.CreateInputParams) (int, error) {
	raw, err := p.host.Call(ctx, "createInput", params)
	if err != nil {
		return 0, err
	}
	var id int
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, err
	}
	return id, nil
}

// SetInputSettings updates an input's settings (overlay=true merges).
func (p *ObsProxy) SetInputSettings(ctx context.Context, inputName string, settings map[string]any, overlay bool) error {
	_, err := p.host.Call(ctx, "setInputSettings", obsSetInputSettingsReq{InputName: inputName, Settings: settings, Overlay: overlay})
	return err
}

// GetSceneItemID resolves the sceneItemId of sourceName within sceneName.
func (p *ObsProxy) GetSceneItemID(ctx context.Context, sceneName, sourceName string) (int, error) {
	raw, err := p.host.Call(ctx, "getSceneItemId", obsGetSceneItemIDReq{Scene: sceneName, Source: sourceName})
	if err != nil {
		return 0, err
	}
	var id int
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, err
	}
	return id, nil
}

// SetSceneItemEnabled shows/hides a scene item.
func (p *ObsProxy) SetSceneItemEnabled(ctx context.Context, sceneName string, itemID int, enabled bool) error {
	_, err := p.host.Call(ctx, "setSceneItemEnabled", obsSetSceneItemEnabledReq{Scene: sceneName, ItemID: itemID, Enabled: enabled})
	return err
}

// SetSceneItemTransform sets a scene item's transform (position/scale/etc.).
func (p *ObsProxy) SetSceneItemTransform(ctx context.Context, sceneName string, itemID int, transform map[string]any) error {
	_, err := p.host.Call(ctx, "setSceneItemTransform", obsSetSceneItemTransformReq{Scene: sceneName, ItemID: itemID, Transform: transform})
	return err
}

// Connected reports whether the child holds a live obs-websocket session.
func (p *ObsProxy) Connected() bool { return p.Status().Connected }

// ── stream / record output control (forwarded to the live obs-websocket client) ──

// StartStream starts the OBS stream output.
func (p *ObsProxy) StartStream(ctx context.Context) error {
	_, err := p.host.Call(ctx, "startStream", nil)
	return err
}

// StopStream stops the OBS stream output.
func (p *ObsProxy) StopStream(ctx context.Context) error {
	_, err := p.host.Call(ctx, "stopStream", nil)
	return err
}

// ToggleStream toggles the stream output; returns the resulting active state.
func (p *ObsProxy) ToggleStream(ctx context.Context) (bool, error) {
	raw, err := p.host.Call(ctx, "toggleStream", nil)
	if err != nil {
		return false, err
	}
	var active bool
	return active, json.Unmarshal(raw, &active)
}

// StartRecord starts the OBS record output.
func (p *ObsProxy) StartRecord(ctx context.Context) error {
	_, err := p.host.Call(ctx, "startRecord", nil)
	return err
}

// StopRecord stops the OBS record output.
func (p *ObsProxy) StopRecord(ctx context.Context) error {
	_, err := p.host.Call(ctx, "stopRecord", nil)
	return err
}

// ToggleRecord toggles the record output; returns the resulting active state.
func (p *ObsProxy) ToggleRecord(ctx context.Context) (bool, error) {
	raw, err := p.host.Call(ctx, "toggleRecord", nil)
	if err != nil {
		return false, err
	}
	var active bool
	return active, json.Unmarshal(raw, &active)
}

// ToggleRecordPause pauses/unpauses the record output; returns the resulting paused state.
func (p *ObsProxy) ToggleRecordPause(ctx context.Context) (bool, error) {
	raw, err := p.host.Call(ctx, "toggleRecordPause", nil)
	if err != nil {
		return false, err
	}
	var paused bool
	return paused, json.Unmarshal(raw, &paused)
}

// ToggleMute toggles mute of a named input/source; returns the resulting muted state.
func (p *ObsProxy) ToggleMute(ctx context.Context, input string) (bool, error) {
	raw, err := p.host.Call(ctx, "toggleMute", map[string]any{"input": input})
	if err != nil {
		return false, err
	}
	var muted bool
	return muted, json.Unmarshal(raw, &muted)
}

// GetStreamStatus returns the live stream-output state (bitrate source, congestion, frames).
func (p *ObsProxy) GetStreamStatus(ctx context.Context) (obs.StreamStatus, error) {
	raw, err := p.host.Call(ctx, "getStreamStatus", nil)
	if err != nil {
		return obs.StreamStatus{}, err
	}
	var st obs.StreamStatus
	return st, json.Unmarshal(raw, &st)
}

// GetRecordStatus returns the live record-output state.
func (p *ObsProxy) GetRecordStatus(ctx context.Context) (obs.RecordStatus, error) {
	raw, err := p.host.Call(ctx, "getRecordStatus", nil)
	if err != nil {
		return obs.RecordStatus{}, err
	}
	var st obs.RecordStatus
	return st, json.Unmarshal(raw, &st)
}

// ── media-sync surface (mediasync.MediaController) - forwarded to the live obs-websocket client ──

// GetMediaInputStatus returns a media input's playback state + cursor/duration.
func (p *ObsProxy) GetMediaInputStatus(ctx context.Context, inputName string) (obs.MediaInputStatus, error) {
	raw, err := p.host.Call(ctx, "getMediaInputStatus", obsMediaStatusReq{Input: inputName})
	if err != nil {
		return obs.MediaInputStatus{}, err
	}
	var st obs.MediaInputStatus
	return st, json.Unmarshal(raw, &st)
}

// SetMediaInputCursor seeks a media input to an absolute position (ms).
func (p *ObsProxy) SetMediaInputCursor(ctx context.Context, inputName string, cursorMs int) error {
	_, err := p.host.Call(ctx, "setMediaInputCursor", obsSetMediaCursorReq{Input: inputName, CursorMs: cursorMs})
	return err
}

// OffsetMediaInputCursor nudges a media input's cursor by a signed ms delta.
func (p *ObsProxy) OffsetMediaInputCursor(ctx context.Context, inputName string, offsetMs int) error {
	_, err := p.host.Call(ctx, "offsetMediaInputCursor", obsOffsetMediaCursorReq{Input: inputName, OffsetMs: offsetMs})
	return err
}

// TriggerMediaInputAction triggers a media action (obs.MediaAction* constants).
func (p *ObsProxy) TriggerMediaInputAction(ctx context.Context, inputName, action string) error {
	_, err := p.host.Call(ctx, "triggerMediaInputAction", obsMediaActionReq{Input: inputName, Action: action})
	return err
}

// ── delay-compensation surface (per-source offset push) - forwarded to the live client ──

// SetInputAudioSyncOffset sets an input's audio sync offset (ms, OBS range −950…+20000).
func (p *ObsProxy) SetInputAudioSyncOffset(ctx context.Context, inputName string, offsetMs int) error {
	_, err := p.host.Call(ctx, "setInputAudioSyncOffset", obsAudioSyncOffsetReq{Input: inputName, OffsetMs: offsetMs})
	return err
}

// SetSourceFilterSettings updates a source filter's settings (overlay=true merges).
func (p *ObsProxy) SetSourceFilterSettings(ctx context.Context, sourceName, filterName string, settings map[string]any, overlay bool) error {
	_, err := p.host.Call(ctx, "setSourceFilterSettings", obsSourceFilterSettingsReq{Source: sourceName, Filter: filterName, Settings: settings, Overlay: overlay})
	return err
}

// CreateSourceFilter adds a filter to a source (delay filters: gpu_delay / async_delay_filter).
func (p *ObsProxy) CreateSourceFilter(ctx context.Context, sourceName, filterName, filterKind string, settings map[string]any) error {
	_, err := p.host.Call(ctx, "createSourceFilter", obsCreateSourceFilterReq{Source: sourceName, Filter: filterName, Kind: filterKind, Settings: settings})
	return err
}

// SetSourceFilterEnabled enables/disables a source filter by name.
func (p *ObsProxy) SetSourceFilterEnabled(ctx context.Context, sourceName, filterName string, enabled bool) error {
	_, err := p.host.Call(ctx, "setSourceFilterEnabled", obsSetSourceFilterEnabledReq{Source: sourceName, Filter: filterName, Enabled: enabled})
	return err
}

// GetSourceFilterList returns a source's filters (name/kind/enabled/settings).
func (p *ObsProxy) GetSourceFilterList(ctx context.Context, sourceName string) ([]obs.FilterInfo, error) {
	raw, err := p.host.Call(ctx, "getSourceFilterList", obsSourceFilterListReq{Source: sourceName})
	if err != nil {
		return nil, err
	}
	var filters []obs.FilterInfo
	return filters, json.Unmarshal(raw, &filters)
}

// SubscribeRecordings streams finished OBS recordings (buffered; drops on overflow).
func (p *ObsProxy) SubscribeRecordings() (<-chan ObsRecording, func()) {
	p.recMu.Lock()
	defer p.recMu.Unlock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan ObsRecording, 8)
	p.recSubs[id] = ch
	return ch, func() {
		p.recMu.Lock()
		defer p.recMu.Unlock()
		if c, ok := p.recSubs[id]; ok {
			delete(p.recSubs, id)
			close(c)
		}
	}
}
