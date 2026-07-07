package vrstats

import (
	"encoding/json"
	"testing"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
)

func TestCollectorAggregates(t *testing.T) {
	bus := eventbus.New(logbus.New(16), "node-a")
	c := New(bus)
	bus.Subscribe(TopicPerf, c.onPerf) // simulate the subscription Start registers

	raw, _ := json.Marshal(PerfStats{Connected: true, FPS: 89.5, DisplayHz: 90, HMDModel: "Index"})
	bus.Publish(TopicPerf, raw)

	got := c.Snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 instance, got %d", len(got))
	}
	if !got[0].Local || !got[0].Connected || got[0].FPS != 89.5 || got[0].HMDModel != "Index" {
		t.Fatalf("bad snapshot: %+v", got[0])
	}
}
