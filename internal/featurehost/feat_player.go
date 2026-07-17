package featurehost

import (
	"context"
	"encoding/json"
)

// playerFeature hosts the audio engine in the `player` child: it serves play/togglePause/stop/
// state/preview/preload requests + a fire-and-forget seek event, and streams tick/end/perror
// events back to the daemon (the PlayerProxy mirror). A decode/codec/oto fault kills only this
// child; the host restarts it and the next play comes back clean.
//
// One engine: the native internal/audio engine (low-latency oto ~15ms, RAM preload, sample-accurate
// seek). AAC/M4A + any native-decode failure fall through to ffmpeg (internal/audio.OpenFFmpeg) on
// the SAME transport. The legacy beep engine was retired (nativeEngine adapter in native_engine.go).
type playerFeature struct {
	rt  *Runtime
	eng playerBackend
}

// playerBackend is the surface the feature drives (native engine adapter in native_engine.go).
// preview* implement the cue-edit hold-audition.
type playerBackend interface {
	PlayFrom(path string, startSec float64) error
	TogglePause() bool
	SeekTo(sec float64, explicit bool)
	PreviewFrom(path string, startSec float64) error
	PreviewRelease(fallbackSec float64)
	Preload(path string) error
	SetVolume(v float64)
	Stop()
	State() State
}

func init() { Register("player", func() Feature { return &playerFeature{} }) }

// playerTick is the position event payload (~5/s while playing). Paused rides every tick so the
// daemon mirror tracks pause state continuously - without it a hold-audition release (a
// fire-and-forget previewRelease that pauses the child) left the mirror reading Playing+!Paused,
// and the next hold-Space skipped the unpause → silence until Stop.
type playerTick struct {
	Cur    float64 `json:"cur"`
	Total  float64 `json:"total"`
	Paused bool    `json:"paused"`
}

// playerError is the decode-failure event payload (drives a daemon toast).
type playerError struct {
	Path string `json:"path"`
	Msg  string `json:"msg"`
}

func (f *playerFeature) Init(_ json.RawMessage, rt *Runtime) error {
	f.rt = rt
	tick := func(cur, total float64) {
		paused := false
		if f.eng != nil { // set before ticks fire; the tick reads live pause state each emit
			paused = f.eng.State().Paused
		}
		rt.Emit("tick", playerTick{Cur: cur, Total: total, Paused: paused})
	}
	end := func() { rt.Emit("end", struct{}{}) }
	perr := func(path, msg string) { rt.Emit("perror", playerError{Path: path, Msg: msg}) }
	f.eng = newNativeEngine(rt.Log, tick, end, perr)
	rt.Log.Info("player", "audio engine = native (internal/audio)", nil)
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
	case "setVolume": // global output gain (0..1), persisted in config by the daemon
		var p struct {
			Volume float64 `json:"volume"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		f.eng.SetVolume(clamp01(p.Volume))
		return nil, nil
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

// clamp01 bounds a gain value into [0,1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Compile-time: the native engine adapter satisfies playerBackend (native_engine.go).
var _ playerBackend = (*nativeEngine)(nil)
