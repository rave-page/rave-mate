package featurehost

import (
	"context"
	"encoding/json"

	"rave.page/mate/internal/audioengine"
)

// playerFeature hosts the audio engine in the `player` child: it serves play/togglePause/stop/
// state requests + a fire-and-forget seek event, and streams tick/end/perror events back to the
// daemon (the PlayerProxy mirror). A decode/codec/oto fault kills only this child; the host
// restarts it and the next play comes back clean.
type playerFeature struct {
	rt  *Runtime
	eng *audioengine.Engine
}

func init() { Register("player", func() Feature { return &playerFeature{} }) }

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

func (f *playerFeature) Init(_ json.RawMessage, rt *Runtime) error {
	f.rt = rt
	f.eng = audioengine.New(rt.Log,
		func(cur, total float64) { rt.Emit("tick", playerTick{Cur: cur, Total: total}) },
		func() { rt.Emit("end", struct{}{}) },
		func(path, msg string) { rt.Emit("perror", playerError{Path: path, Msg: msg}) },
	)
	return nil
}

func (f *playerFeature) Start(ctx context.Context) error {
	<-ctx.Done()
	f.eng.Stop()
	return nil
}

// HandleEvent serves fire-and-forget commands (seek is hot - no response round-trip).
func (f *playerFeature) HandleEvent(event string, data json.RawMessage) {
	if event == "seek" {
		var p struct {
			Sec      float64 `json:"sec"`
			Explicit bool    `json:"explicit"` // user intent: bypass the near-position noop guard
		}
		if json.Unmarshal(data, &p) == nil {
			f.eng.SeekTo(p.Sec, p.Explicit)
		}
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
