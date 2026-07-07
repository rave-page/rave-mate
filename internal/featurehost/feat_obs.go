package featurehost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"rave.page/mate/internal/obs"
)

func init() { Register("obs", func() Feature { return &obsFeature{} }) }

// obsInit configures the child's obs-websocket connection + optional overlay-source management.
type obsInit struct {
	Host     string        `json:"host"`
	Port     int           `json:"port"`
	Password string        `json:"password"`
	Overlay  ObsOverlayCfg `json:"overlay"`
}

// ObsOverlayCfg drives auto-management of the overlay browser source in OBS (built by app.go
// from config + the overlay URL; empty/disabled → no-op). Exported so app.go can populate it.
type ObsOverlayCfg struct {
	Enabled       bool   `json:"enabled"`
	URL           string `json:"url"`           // http://127.0.0.1:<port>/
	Scene         string `json:"scene"`         // dedicated scene name
	Source        string `json:"source"`        // browser-source input name
	Width         int    `json:"width"`         // 0 = match OBS canvas
	Height        int    `json:"height"`        // 0 = match OBS canvas
	NestInProgram bool   `json:"nestInProgram"` // also nest the dedicated scene in the program scene
}

// obsState mirrors the OBS bridge state to the daemon ("state" events, pushed on change).
type obsState struct {
	Connected    bool      `json:"connected"`
	Recording    bool      `json:"recording"`
	RecStartedAt time.Time `json:"recStartedAt,omitzero"`
}

// obsRecEvent is one finished OBS recording ("rec" events). StartedAt/EndedAt bound the
// recording window for tracklist linking; Bytes is best-effort (stat of the output file -
// 0 when OBS runs on another host).
type obsRecEvent struct {
	Path      string    `json:"path"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Bytes     int64     `json:"bytes"`
}

// obsReconnectDelay paces connect retries - OBS comes and goes as the user opens/closes it.
// After obsBackoffAfter consecutive never-connected attempts (OBS closed for a while) the
// retry slows to obsReconnectSlow so a closed OBS isn't dialed every 5s forever; a successful
// connect resets to the fast cadence.
var (
	obsReconnectDelay = 5 * time.Second
	obsReconnectSlow  = 30 * time.Second
	obsBackoffAfter   = 6 // ~30s of fast retries before slowing down
)

// obsFeature hosts the OBS bridge in the child: maintains the obs-websocket connection,
// watches RecordStateChanged, and reports finished recordings to the daemon (which links
// them to the tracklist recorded over the same span).
type obsFeature struct {
	rt   *Runtime
	cfg  obsInit
	last obsState

	mu  sync.Mutex
	cli *obs.Client // live obs-websocket client while a session is connected; nil otherwise
}

// setCli stores the live client (nil on disconnect) for request forwarding.
func (f *obsFeature) setCli(c *obs.Client) {
	f.mu.Lock()
	f.cli = c
	f.mu.Unlock()
}

// getCli returns the live client or nil when no session is connected.
func (f *obsFeature) getCli() *obs.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cli
}

func (f *obsFeature) Init(params json.RawMessage, rt *Runtime) error {
	if err := json.Unmarshal(params, &f.cfg); err != nil {
		return err
	}
	f.rt = rt
	return nil
}

// Start loops connect → watch → reconnect until ctx is done. A missing/closed OBS is the
// normal state, not an error - the child stays up and keeps retrying quietly.
func (f *obsFeature) Start(ctx context.Context) error {
	fails := 0 // consecutive attempts that never connected
	for {
		connected, err := f.session(ctx)
		if err != nil && ctx.Err() == nil {
			f.rt.Log.Debug("obs", "session ended", map[string]any{"error": err.Error()})
		}
		f.emitState(obsState{}) // disconnected
		if connected {
			fails = 0
		} else {
			fails++
		}
		delay := obsReconnectDelay
		if fails >= obsBackoffAfter {
			delay = obsReconnectSlow
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// session runs one connected obs-websocket lifetime. connected reports whether the dial
// succeeded (drives the retry backoff - a lost session retries fast, a closed OBS slows).
func (f *obsFeature) session(ctx context.Context) (connected bool, _ error) {
	c, err := obs.Connect(ctx, f.cfg.Host, f.cfg.Port, f.cfg.Password)
	if err != nil {
		return false, err
	}
	defer func() { _ = c.Close() }()
	f.setCli(c)
	defer f.setCli(nil)
	ev, unsub := c.SubscribeEvents()
	defer unsub()

	// Already mid-recording on connect? Back-date the start from the elapsed duration.
	var recStart time.Time
	stCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	rs, rerr := c.GetRecordStatus(stCtx)
	cancel()
	if rerr == nil && rs.Active {
		recStart = time.Now().Add(-rs.Duration)
	}
	f.rt.Log.Info("obs", "connected", map[string]any{"host": f.cfg.Host, "recording": !recStart.IsZero()})
	f.emitState(obsState{Connected: true, Recording: !recStart.IsZero(), RecStartedAt: recStart})

	// Auto-manage the overlay browser source (best-effort; errors are logged, never fatal).
	if f.cfg.Overlay.Enabled && f.cfg.Overlay.URL != "" {
		oc, ocancel := context.WithTimeout(ctx, 10*time.Second)
		f.ensureOverlaySource(oc, c)
		ocancel()
	}

	for {
		select {
		case <-ctx.Done():
			return true, nil
		case <-c.Done():
			return true, errors.New("connection lost")
		case e, ok := <-ev:
			if !ok {
				return true, errors.New("connection lost")
			}
			if e.Type != "RecordStateChanged" {
				continue
			}
			var d struct {
				OutputState string `json:"outputState"`
				OutputPath  string `json:"outputPath"`
			}
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			switch d.OutputState {
			case "OBS_WEBSOCKET_OUTPUT_STARTED":
				recStart = time.Now()
				f.emitState(obsState{Connected: true, Recording: true, RecStartedAt: recStart})
			case "OBS_WEBSOCKET_OUTPUT_STOPPED":
				end := time.Now()
				start := recStart
				if start.IsZero() {
					start = end
				}
				var size int64
				if fi, serr := os.Stat(d.OutputPath); serr == nil {
					size = fi.Size()
				}
				f.rt.Log.Info("obs", "recording finished", map[string]any{"path": d.OutputPath, "dur": end.Sub(start).Truncate(time.Second).String()})
				f.rt.Emit("rec", obsRecEvent{Path: d.OutputPath, StartedAt: start, EndedAt: end, Bytes: size})
				recStart = time.Time{}
				f.emitState(obsState{Connected: true})
			}
		}
	}
}

// ensureOverlaySource makes sure the overlay browser source exists in OBS, in a dedicated scene,
// sized to the OBS canvas (or the configured size), with the correct URL - creating the scene +
// input as needed and updating an existing one. Optionally nests the scene into the program scene.
// Idempotent + best-effort: every OBS call is allowed to fail without aborting the session.
func (f *obsFeature) ensureOverlaySource(ctx context.Context, c *obs.Client) {
	o := f.cfg.Overlay
	scene, src := o.Scene, o.Source
	if scene == "" {
		scene = "rave-mate"
	}
	if src == "" {
		src = "rave-mate overlay"
	}
	w, h := o.Width, o.Height
	if w <= 0 || h <= 0 {
		if bw, bh, err := c.CanvasSize(ctx); err == nil && bw > 0 && bh > 0 {
			w, h = bw, bh
		} else {
			w, h = 1920, 1080
		}
	}
	// Ensure the dedicated scene exists.
	scenes, _, err := c.GetSceneList(ctx)
	if err != nil {
		f.rt.Log.Debug("obs", "overlay ensure: scene list", map[string]any{"error": err.Error()})
		return
	}
	hasScene := false
	for _, s := range scenes {
		if s.Name == scene {
			hasScene = true
			break
		}
	}
	if !hasScene {
		if err := c.CreateScene(ctx, scene); err != nil {
			f.rt.Log.Debug("obs", "overlay ensure: create scene", map[string]any{"error": err.Error()})
			return
		}
	}
	// Ensure the browser source exists with the right URL + size.
	settings := map[string]any{"url": o.URL, "width": w, "height": h}
	exists := false
	if inputs, ierr := c.GetInputList(ctx, ""); ierr == nil {
		for _, in := range inputs {
			if in.Name == src {
				exists = true
				break
			}
		}
	}
	if exists {
		_ = c.SetInputSettings(ctx, src, settings, true)
		if _, gerr := c.GetSceneItemID(ctx, scene, src); gerr != nil { // not in the dedicated scene yet
			_, _ = c.CreateSceneItem(ctx, scene, src, true)
		}
	} else if _, cerr := c.CreateInput(ctx, obs.CreateInputParams{
		SceneName: scene, InputName: src, InputKind: "browser_source", InputSettings: settings, SceneItemEnabled: true,
	}); cerr != nil {
		f.rt.Log.Debug("obs", "overlay ensure: create input", map[string]any{"error": cerr.Error()})
		return
	}
	// Optionally nest the dedicated scene into the current program scene.
	if o.NestInProgram {
		if prog, perr := c.GetCurrentProgramScene(ctx); perr == nil && prog != "" && prog != scene {
			if _, gerr := c.GetSceneItemID(ctx, prog, scene); gerr != nil {
				_, _ = c.CreateSceneItem(ctx, prog, scene, true)
			}
		}
	}
	// Reload the page no-cache so the source always runs the CURRENT overlay HTML/JS (a source left
	// open across a rave-mate update would otherwise keep stale cached JS and miss live style pushes).
	_ = c.PressInputPropertiesButton(ctx, src, "refreshnocache")
	f.rt.Log.Info("obs", "overlay source ensured", map[string]any{"scene": scene, "source": src, "w": w, "h": h})
}

// emitState pushes a "state" event on change only.
func (f *obsFeature) emitState(st obsState) {
	if st == f.last {
		return
	}
	f.last = st
	f.rt.Emit("state", st)
}

// ── obs-websocket request RPC (daemon ObsProxy → child) ──
// Method-name strings are shared with obsproxy.go; keep both sides in sync.

var errObsNotConnected = errors.New("obs not connected")

type obsGetInputListReq struct {
	Kind string `json:"kind"`
}

type obsSetInputSettingsReq struct {
	InputName string         `json:"inputName"`
	Settings  map[string]any `json:"settings"`
	Overlay   bool           `json:"overlay"`
}

type obsGetSceneItemIDReq struct {
	Scene  string `json:"scene"`
	Source string `json:"source"`
}

type obsSetSceneItemEnabledReq struct {
	Scene   string `json:"scene"`
	ItemID  int    `json:"itemId"`
	Enabled bool   `json:"enabled"`
}

type obsSetSceneItemTransformReq struct {
	Scene     string         `json:"scene"`
	ItemID    int            `json:"itemId"`
	Transform map[string]any `json:"transform"`
}

type obsMediaStatusReq struct {
	Input string `json:"input"`
}

type obsSetMediaCursorReq struct {
	Input    string `json:"input"`
	CursorMs int    `json:"cursorMs"`
}

type obsOffsetMediaCursorReq struct {
	Input    string `json:"input"`
	OffsetMs int    `json:"offsetMs"`
}

type obsMediaActionReq struct {
	Input  string `json:"input"`
	Action string `json:"action"`
}

type obsAudioSyncOffsetReq struct {
	Input    string `json:"input"`
	OffsetMs int    `json:"offsetMs"`
}

type obsSourceFilterSettingsReq struct {
	Source   string         `json:"source"`
	Filter   string         `json:"filter"`
	Settings map[string]any `json:"settings"`
	Overlay  bool           `json:"overlay"`
}

type obsCreateSourceFilterReq struct {
	Source   string         `json:"source"`
	Filter   string         `json:"filter"`
	Kind     string         `json:"kind"`
	Settings map[string]any `json:"settings"`
}

type obsSetSourceFilterEnabledReq struct {
	Source  string `json:"source"`
	Filter  string `json:"filter"`
	Enabled bool   `json:"enabled"`
}

type obsSourceFilterListReq struct {
	Source string `json:"source"`
}

// Handle forwards obs-websocket requests to the live client. Responses marshal the bare
// return value (string / []obs.InputInfo / int); error-only methods return a nil result.
func (f *obsFeature) Handle(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	c := f.getCli()
	if c == nil {
		return nil, errObsNotConnected
	}
	switch method {
	case "getCurrentProgramScene":
		scene, err := c.GetCurrentProgramScene(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(scene)
	case "getInputList":
		var req obsGetInputListReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		inputs, err := c.GetInputList(ctx, req.Kind)
		if err != nil {
			return nil, err
		}
		return json.Marshal(inputs)
	case "createInput":
		var p obs.CreateInputParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		id, err := c.CreateInput(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(id)
	case "setInputSettings":
		var req obsSetInputSettingsReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.SetInputSettings(ctx, req.InputName, req.Settings, req.Overlay)
	case "getSceneItemId":
		var req obsGetSceneItemIDReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		id, err := c.GetSceneItemID(ctx, req.Scene, req.Source)
		if err != nil {
			return nil, err
		}
		return json.Marshal(id)
	case "setSceneItemEnabled":
		var req obsSetSceneItemEnabledReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.SetSceneItemEnabled(ctx, req.Scene, req.ItemID, req.Enabled)
	case "setSceneItemTransform":
		var req obsSetSceneItemTransformReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.SetSceneItemTransform(ctx, req.Scene, req.ItemID, req.Transform)
	case "startStream":
		return nil, c.StartStream(ctx)
	case "stopStream":
		return nil, c.StopStream(ctx)
	case "toggleStream":
		active, err := c.ToggleStream(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(active)
	case "startRecord":
		return nil, c.StartRecord(ctx)
	case "stopRecord":
		return nil, c.StopRecord(ctx)
	case "toggleRecord":
		active, err := c.ToggleRecord(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(active)
	case "toggleRecordPause":
		paused, err := c.ToggleRecordPause(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(paused)
	case "toggleMute":
		var req struct {
			Input string `json:"input"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		muted, err := c.ToggleInputMute(ctx, req.Input)
		if err != nil {
			return nil, err
		}
		return json.Marshal(muted)
	case "getStreamStatus":
		st, err := c.GetStreamStatus(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(st)
	case "getRecordStatus":
		st, err := c.GetRecordStatus(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(st)
	case "getMediaInputStatus":
		var req obsMediaStatusReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		st, err := c.GetMediaInputStatus(ctx, req.Input)
		if err != nil {
			return nil, err
		}
		return json.Marshal(st)
	case "setMediaInputCursor":
		var req obsSetMediaCursorReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.SetMediaInputCursor(ctx, req.Input, req.CursorMs)
	case "offsetMediaInputCursor":
		var req obsOffsetMediaCursorReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.OffsetMediaInputCursor(ctx, req.Input, req.OffsetMs)
	case "triggerMediaInputAction":
		var req obsMediaActionReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.TriggerMediaInputAction(ctx, req.Input, req.Action)
	case "setInputAudioSyncOffset":
		var req obsAudioSyncOffsetReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.SetInputAudioSyncOffset(ctx, req.Input, req.OffsetMs)
	case "setSourceFilterSettings":
		var req obsSourceFilterSettingsReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.SetSourceFilterSettings(ctx, req.Source, req.Filter, req.Settings, req.Overlay)
	case "createSourceFilter":
		var req obsCreateSourceFilterReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.CreateSourceFilter(ctx, req.Source, req.Filter, req.Kind, req.Settings)
	case "setSourceFilterEnabled":
		var req obsSetSourceFilterEnabledReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, c.SetSourceFilterEnabled(ctx, req.Source, req.Filter, req.Enabled)
	case "getSourceFilterList":
		var req obsSourceFilterListReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		filters, err := c.GetSourceFilterList(ctx, req.Source)
		if err != nil {
			return nil, err
		}
		return json.Marshal(filters)
	default:
		return nil, errUnknownMethod(method)
	}
}
