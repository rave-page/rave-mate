package vrstats

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"rave.page/mate/internal/eventbus"
)

const staleAge = 10 * time.Second // instances that stop publishing drop out of snapshots

type entry struct {
	Instance
	seen time.Time
}

// Collector subscribes to TopicPerf and keeps the latest PerfStats per instance (local + peers), for
// the UI monitor panel + `rave-mate ctl vrperf`. Always-on + dependency-light; works on an instance
// with no VR at all (a pure monitor).
type Collector struct {
	bus *eventbus.Bus

	mu    sync.Mutex
	insts map[string]entry
}

// New builds the collector. bus may be nil (then it just stays empty).
func New(bus *eventbus.Bus) *Collector {
	return &Collector{bus: bus, insts: map[string]entry{}}
}

// Start subscribes to TopicPerf until ctx is cancelled. Implements module.Service.Start.
func (c *Collector) Start(ctx context.Context) error {
	if c.bus != nil {
		c.bus.Subscribe(TopicPerf, c.onPerf)
	}
	<-ctx.Done()
	return nil
}

func (c *Collector) onPerf(e eventbus.Event) {
	var ps PerfStats
	if json.Unmarshal(e.Data, &ps) != nil {
		return
	}
	c.mu.Lock()
	c.insts[e.Origin] = entry{Instance: Instance{Origin: e.Origin, Local: e.Local, PerfStats: ps}, seen: time.Now()}
	c.mu.Unlock()
}

// Snapshot returns the latest stats per instance (local first, then by origin), stale entries pruned.
func (c *Collector) Snapshot() []Instance {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	out := make([]Instance, 0, len(c.insts))
	for id, e := range c.insts {
		if now.Sub(e.seen) > staleAge {
			delete(c.insts, id)
			continue
		}
		out = append(out, e.Instance)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		return out[i].Origin < out[j].Origin
	})
	return out
}

// JSON returns the snapshot as a JSON array (for `rave-mate ctl vrperf`).
func (c *Collector) JSON() string {
	b, err := json.Marshal(c.Snapshot())
	if err != nil {
		return "[]"
	}
	return string(b)
}
