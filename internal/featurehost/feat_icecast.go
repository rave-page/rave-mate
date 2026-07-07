package featurehost

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"rave.page/mate/internal/icecast"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/sources/icecastsrc"
)

func init() { Register("icecast", func() Feature { return &icecastFeature{} }) }

// icecastFeature hosts the Icecast set-capture receiver in the child: the TCP listener,
// source-protocol parsing, and capture-file writing all run out-of-process. Emits "obs"
// (now-playing observations), "capture" (icecast.Capture start/end), "state"
// (icecast.Status mirror, pushed on change ≤1/s).
type icecastFeature struct {
	rt  *Runtime
	rcv *icecast.Receiver
}

func (f *icecastFeature) Init(params json.RawMessage, rt *Runtime) error {
	var cfg icecast.Config
	if err := json.Unmarshal(params, &cfg); err != nil {
		return err
	}
	f.rt = rt
	f.rcv = icecast.New(rt.Log, cfg)
	return nil
}

func (f *icecastFeature) Start(ctx context.Context) error {
	// Now-playing metadata → Observations.
	src := icecastsrc.New(f.rcv)
	go func() {
		_ = src.Start(ctx, func(o session.Observation) { f.rt.Emit("obs", o) })
	}()
	// Capture lifecycle events (daemon does the libdb linking).
	go func() {
		ch, unsub := f.rcv.SubscribeCapture()
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-ch:
				if !ok {
					return
				}
				f.rt.Emit("capture", c)
			}
		}
	}()
	// Status mirror for the settings card (push on change, ≤1/s).
	go func() {
		var last icecast.Status
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if st := f.rcv.Snapshot(); !reflect.DeepEqual(st, last) {
					last = st
					f.rt.Emit("state", st)
				}
			}
		}
	}()
	// Bind error → child exit 1 → host restarts with backoff (daemon unaffected).
	if err := f.rcv.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done() // receiver Start is non-blocking; the feature owns the lifetime
	return nil
}

func (f *icecastFeature) Handle(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, errUnknownMethod(method)
}
