package logbus

import "testing"

func TestRingRetainsLastN(t *testing.T) {
	b := New(3)
	for i := 0; i < 5; i++ {
		b.Info("test", "msg", map[string]any{"i": i})
	}
	snap := b.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("want 3 retained, got %d", len(snap))
	}
	if snap[0].Fields["i"] != 2 || snap[2].Fields["i"] != 4 {
		t.Fatalf("ring kept wrong window: %v..%v", snap[0].Fields["i"], snap[2].Fields["i"])
	}
}

func TestSubscribeReceivesNewEntries(t *testing.T) {
	b := New(8)
	ch, cancel := b.Subscribe()
	defer cancel()
	b.Warn("src", "hello", nil)
	e := <-ch
	if e.Level != Warn || e.Source != "src" || e.Msg != "hello" {
		t.Fatalf("unexpected entry: %+v", e)
	}
}
