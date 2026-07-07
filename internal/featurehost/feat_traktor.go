package featurehost

import (
	"context"
	"encoding/json"
	"time"

	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/sources/traktorsrc"
	"rave.page/mate/internal/traktor"
)

func init() { Register("traktor", func() Feature { return &traktorFeature{} }) }

// traktorInit is the init wire config for the traktor feature.
type traktorInit struct {
	Addr        string `json:"addr"`
	LogPath     string `json:"logPath"`
	LogPayloads bool   `json:"logPayloads"`
}

// traktorFeature hosts the Traktor HTTP listener + traktorsrc adapter in the child:
// untrusted HTTP ingest parses out-of-process. Emits "obs" (session.Observation),
// "mon" (ingest monitor lines), "state" ({listening}).
type traktorFeature struct {
	rt  *Runtime
	srv *traktor.Server
}

func (t *traktorFeature) Init(params json.RawMessage, rt *Runtime) error {
	var p traktorInit
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	t.rt = rt
	t.srv = traktor.New(rt.Log, p.Addr, p.LogPath, p.LogPayloads)

	// Ingest monitor lines → "mon" events (daemon routes them into traktorMon).
	mon := newChildMonitor(rt, "mon")
	t.srv.SetMonitor(mon)
	return nil
}

func (t *traktorFeature) Start(ctx context.Context) error {
	// Adapter: traktor events → Observations over the wire. Coalesced: Traktor ticks
	// full deck state many times/sec/deck; uncoalesced that floods the daemon reader.
	src := traktorsrc.New(t.srv)
	co := newObsCoalescer(obsCoalesceInterval, func(o session.Observation) { t.rt.Emit("obs", o) })
	go func() {
		_ = src.Start(ctx, co.Add)
	}()
	// Listening mirror for the dashboard badge.
	go func() {
		last := false
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if l := t.srv.Listening(); l != last {
					last = l
					t.rt.Emit("state", map[string]bool{"listening": l})
				}
			}
		}
	}()
	// Blocks until ctx cancel; a bind clash returns an error → child exits 1 → host
	// logs + retries with backoff (the daemon never crashes over a busy port).
	return t.srv.Start(ctx)
}

func (t *traktorFeature) Handle(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "setLogging":
		var p struct {
			On bool `json:"on"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		t.srv.SetLogging(p.On)
		return nil, nil
	}
	return nil, errUnknownMethod(method)
}
