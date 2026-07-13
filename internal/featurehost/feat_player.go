package featurehost

import (
	"context"
	"encoding/json"

	"rave.page/mate/internal/audioengine"
)

// playerFeature hosts the audio engine in the `player` child: it serves play/togglePause/stop/
// state/preview/preload requests + a fire-and-forget seek event, and streams tick/end/perror
// events back to the daemon (the PlayerProxy mirror). A decode/codec/oto fault kills only this
// child; the host restarts it and the next play comes back clean.
//
// Two backends, chosen per child by the init flag nativeDecode:
//   - legacy (default): audioengine.Engine (beep + ffmpeg).
//   - native: audioengine.NativeEngine (internal/audio: low-latency oto, RAM preload,
//     sample-accurate seek). AAC/M4A + any native-decode failure fall through to ffmpeg.
//
// Both satisfy playerBackend; the feature is otherwise identical.
type playerFeature struct {
	rt  *Runtime
	eng playerBackend
}

// playerBackend is the surface the feature drives — satisfied by both the legacy beep engine and
// the native engine (adapter in native_engine.go). preview* implement the cue-edit hold-audition.
type playerBackend interface {
	PlayFrom(path string, startSec float64) error
	TogglePause() bool
	SeekTo(sec float64, explicit bool)
	PreviewFrom(path string, startSec float64) error
	PreviewRelease(fallbackSec float64)
	Preload(path string) error
	Stop()
	State() audioengine.State
}

func init() { Register("player", func() Feature { return &playerFeature{} }) }

// playerInit is the child's init params (from the daemon proxy).
type playerInit struct {
	NativeDecode bool `json:"nativeDecode"`
}

// playerTick is the position event payload (~5/s while playing).
type playerTick struct {
	Cur   float64 `json:"cur"`
	Total float64 `json:"total"`
}

// playerError is the decode-failure event payload (drives a daemon toast).
type playerError struct {
	Path string `json:"path"`
	Msg  string `json:"msg"`
}

func (f *playerFeature) Init(raw json.RawMessage, rt *Runtime) error {
	f.rt = rt
	var p playerInit
	_ = json.Unmarshal(raw, &p) // absent/old daemon => legacy
	tick := func(cur, total float64) { rt.Emit("tick", playerTick{Cur: cur, Total: total}) }
	end := func() { rt.Emit("end", struct{}{}) }
	perr := func(path, msg string) { rt.Emit("perror", playerError{Path: path, Msg: msg}) }
	if p.NativeDecode {
		f.eng = newNativeEngine(rt.Log, tick, end, perr)
		rt.Log.Info("player", "audio engine = native (internal/audio)", nil)
	} else {
		f.eng = audioengine.New(rt.Log, tick, end, perr)
		rt.Log.Info("player", "audio engine = legacy (beep/ffmpeg)", nil)
	}
	return nil
}

func (f *playerFeature) Start(ctx context.Context) error {
	<-ctx.Done()
	f.eng.Stop()
	return nil
}

// HandleEvent serves fire-and-forget commands (seek + preview release are hot - no round-trip).
func (f *playerFeature) HandleEvent(event string, data json.RawMessage) {
	switch event {
	case "seek":
		var p struct {
			Sec      float64 `json:"sec"`
			Explicit bool    `json:"explicit"` // user intent: bypass the near-position noop guard
		}
		if json.Unmarshal(data, &p) == nil {
			f.eng.SeekTo(p.Sec, p.Explicit)
		}
	case "previewRelease":
		var p struct {
			FallbackSec float64 `json:"fallbackSec"`
		}
		p.FallbackSec = -1
		_ = json.Unmarshal(data, &p)
		f.eng.PreviewRelease(p.FallbackSec)
	}
}

func (f *playerFeature) Handle(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "play":
		var p struct {
			Path     string  `json:"path"`
			StartSec float64 `json:"startSec"` // optional start offset (0 = beginning)
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if err := f.eng.PlayFrom(p.Path, p.StartSec); err != nil {
			return nil, err
		}
		return json.Marshal(f.eng.State())
	case "previewFrom": // cue-edit hold-Space press: play from the cursor, remember the return point
		var p struct {
			Path     string  `json:"path"`
			StartSec float64 `json:"startSec"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if err := f.eng.PreviewFrom(p.Path, p.StartSec); err != nil {
			return nil, err
		}
		return json.Marshal(f.eng.State())
	case "preload": // cue-edit track open: prewarm the RAM buffer so the first press is instant
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, f.eng.Preload(p.Path)
	case "togglePause":
		paused := f.eng.TogglePause()
		return json.Marshal(struct {
			Paused bool `json:"paused"`
		}{paused})
	case "halt": // playback stop; NOT "stop" (reserved lifecycle method that exits the child)
		f.eng.Stop()
		return nil, nil
	case "state":
		return json.Marshal(f.eng.State())
	default:
		return nil, errUnknownMethod(method)
	}
}

// Compile-time: both engines satisfy playerBackend (legacy via preview_shim.go, native via
// native_engine.go).
var (
	_ playerBackend = (*audioengine.Engine)(nil)
	_ playerBackend = (*nativeEngine)(nil)
)
