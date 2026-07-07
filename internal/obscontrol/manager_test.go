package obscontrol

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/obs"
)

// fakeOBS is an in-memory OBS surface recording control calls.
type fakeOBS struct {
	connected   bool
	streaming   atomic.Bool
	recording   atomic.Bool
	startStream atomic.Int32
}

func (f *fakeOBS) Connected() bool { return f.connected }
func (f *fakeOBS) StartStream(context.Context) error {
	f.startStream.Add(1)
	f.streaming.Store(true)
	return nil
}
func (f *fakeOBS) StopStream(context.Context) error                 { f.streaming.Store(false); return nil }
func (f *fakeOBS) ToggleStream(context.Context) (bool, error)       { return f.streaming.Load(), nil }
func (f *fakeOBS) StartRecord(context.Context) error                { f.recording.Store(true); return nil }
func (f *fakeOBS) StopRecord(context.Context) error                 { f.recording.Store(false); return nil }
func (f *fakeOBS) ToggleRecord(context.Context) (bool, error)       { return f.recording.Load(), nil }
func (f *fakeOBS) ToggleRecordPause(context.Context) (bool, error)  { return false, nil }
func (f *fakeOBS) ToggleMute(context.Context, string) (bool, error) { return false, nil }
func (f *fakeOBS) GetStreamStatus(context.Context) (obs.StreamStatus, error) {
	return obs.StreamStatus{Active: f.streaming.Load(), Bytes: 1_000_000, Congestion: 0.1}, nil
}
func (f *fakeOBS) GetRecordStatus(context.Context) (obs.RecordStatus, error) {
	return obs.RecordStatus{Active: f.recording.Load()}, nil
}

func TestPollPublishesStatus(t *testing.T) {
	log := logbus.New(16)
	bus := eventbus.New(log, "node-a")
	f := &fakeOBS{connected: true}
	m := New(log, bus, f, "host-a", "node-a", nil)
	bus.Subscribe(TopicStatus, m.onStatus) // simulate the subscription Start would register

	m.pollAll(context.Background())
	got := m.Statuses()
	if len(got) != 1 || !got[0].Local || !got[0].Connected {
		t.Fatalf("want one local connected status, got %+v", got)
	}
	if got[0].Label != "host-a" || got[0].ID != "node-a" {
		t.Fatalf("label/id = %q/%q", got[0].Label, got[0].ID)
	}
}

func TestCommandExecutesOnTargetedInstance(t *testing.T) {
	log := logbus.New(16)
	bus := eventbus.New(log, "node-a")
	f := &fakeOBS{connected: true}
	m := New(log, bus, f, "host-a", "node-a", nil)
	m.reconcileSources() // register the local source so onCmd can find it by id
	bus.Subscribe(TopicCmd, func(e eventbus.Event) { m.onCmd(context.Background(), e) })

	// Targeted at us → executes.
	if err := m.Command(context.Background(), Cmd{Target: "node-a", Action: ActStreamStart}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return f.startStream.Load() == 1 }, "stream start")

	// Targeted at another source → ignored.
	_ = m.Command(context.Background(), Cmd{Target: "node-z", Action: ActStreamStart})
	time.Sleep(50 * time.Millisecond)
	if f.startStream.Load() != 1 {
		t.Fatalf("command for another node executed locally (count=%d)", f.startStream.Load())
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
