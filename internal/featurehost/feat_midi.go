package featurehost

import (
	"context"
	"encoding/json"

	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/sources/midisrc"
)

func init() { Register("midi", func() Feature { return &midiFeature{} }) }

// midiInit is the init wire config for the midi feature.
type midiInit struct {
	DenonPort  string `json:"denonPort"`
	CustomPort string `json:"customPort"`
}

// midiMsg is one raw MIDI message on the wire (peer-bridge tap + inject).
type midiMsg struct {
	S  byte `json:"s"`
	D1 byte `json:"d1"`
	D2 byte `json:"d2"`
}

// midiFeature hosts the winmm MIDI driver + decoders in the child: a callback-thread
// fault kills only the child. Emits "obs", "mon" (raw-message monitor), "midi" (peer
// forward tap); handles "inject" (peer-bridged MIDI into the local decoders).
type midiFeature struct {
	rt  *Runtime
	src *midisrc.Source
}

func (f *midiFeature) Init(params json.RawMessage, rt *Runtime) error {
	var p midiInit
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	f.rt = rt
	f.src = midisrc.New(rt.Log, p.DenonPort, p.CustomPort)
	f.src.SetMonitor(newChildMonitor(rt, "mon"))
	f.src.SetForwarder(func(m midi.Message) {
		rt.Emit("midi", midiMsg{S: m.Status, D1: m.Data1, D2: m.Data2})
	})
	return nil
}

func (f *midiFeature) Start(ctx context.Context) error {
	// Coalesced: CC twiddles (fader/EQ) tick many times/sec; discrete track text passes through.
	co := newObsCoalescer(obsCoalesceInterval, func(o session.Observation) { f.rt.Emit("obs", o) })
	return f.src.Start(ctx, co.Add)
}

func (f *midiFeature) Handle(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "inject":
		var m midiMsg
		if err := json.Unmarshal(params, &m); err != nil {
			return nil, err
		}
		f.src.Inject(midi.Message{Status: m.S, Data1: m.D1, Data2: m.D2})
		return nil, nil
	}
	return nil, errUnknownMethod(method)
}
