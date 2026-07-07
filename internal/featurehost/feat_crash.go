package featurehost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// crashFeature drives the host integration tests: controllable ticking, panics, hard
// exits, hangs. Registered ("crash") but never wired as a module - harmless in prod.
type crashFeature struct {
	rt    *Runtime
	tick  time.Duration
	wedge chan struct{} // closed by "wedge" → Start loop stops beating (simulates a hung loop)
}

type crashInit struct {
	TickMS int `json:"tickMs"`
}

func (c *crashFeature) Init(params json.RawMessage, rt *Runtime) error {
	var p crashInit
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	if p.TickMS < 0 {
		return fmt.Errorf("bad tickMs %d", p.TickMS)
	}
	c.tick = 50 * time.Millisecond
	if p.TickMS > 0 {
		c.tick = time.Duration(p.TickMS) * time.Millisecond
	}
	c.rt = rt
	c.wedge = make(chan struct{})
	return nil
}

func (c *crashFeature) Start(ctx context.Context) error {
	c.rt.Log.Info("crash", "started", nil)
	t := time.NewTicker(c.tick)
	defer t.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.wedge:
			<-ctx.Done() // hung: stop beating/ticking, keep the process alive (heartbeat-monitor bait)
			return nil
		case <-t.C:
			n++
			c.rt.Beat()
			c.rt.Emit("tick", map[string]int{"n": n})
		}
	}
}

func (c *crashFeature) Handle(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "echo":
		return params, nil
	case "panic":
		panic("crash feature: requested panic")
	case "exit": // hard death, no response - exercises the pending-fail path
		os.Exit(3)
		return nil, nil
	case "hang":
		select {} // never responds - exercises Call ctx timeout
	case "wedge": // Start loop stops beating (heartbeat-monitor test); process stays alive
		close(c.wedge)
		return nil, nil
	case "log":
		c.rt.Log.Warn("crash", "requested log line", map[string]any{"via": "handle"})
		return nil, nil
	}
	return nil, fmt.Errorf("unknown method %s", method)
}
