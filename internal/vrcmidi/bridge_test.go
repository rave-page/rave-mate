package vrcmidi

import (
	"context"
	"errors"
	"testing"
	"time"

	"rave.page/mate/internal/artnet"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

type msg struct{ status, d1, d2 byte }

type fakeOut struct {
	msgs   []msg
	closed bool
}

func (f *fakeOut) Send(status, d1, d2 byte) { f.msgs = append(f.msgs, msg{status, d1, d2}) }
func (f *fakeOut) Close()                   { f.closed = true }

func setUni(t *testing.T, st *artnet.Store, u uint16, seq byte, vals map[int]byte) {
	t.Helper()
	data, _ := st.Get(u)
	for i, v := range vals {
		data[i] = v
	}
	if !st.Set(u, seq, data[:], "test", time.Now()) {
		t.Fatalf("Set(%d) rejected", u)
	}
}

func newTestState(unis []int, rate int, st *artnet.Store) *state {
	return newState(config.DMXMIDIFeature{Universes: unis, MaxPerSecond: rate}, st)
}

func TestMappingAndValues(t *testing.T) {
	store := artnet.NewStore()
	setUni(t, store, 3, 1, map[int]byte{0: 255, 129: 100, 511: 3})
	s := newTestState([]int{3, 7}, 1000, store)
	setUni(t, store, 7, 1, map[int]byte{5: 200})
	out := &fakeOut{}
	s.scan()
	n := s.flush(out, 4096)
	if n != 4 {
		t.Fatalf("sent %d, want 4", n)
	}
	want := map[msg]bool{
		{0xB0, 0, 127}:     true, // uni 3 ch0 → g0: midi ch0 cc0, 255>>1
		{0xB1, 1, 50}:      true, // g129: ch1 cc1
		{0xB3, 127, 1}:     true, // g511: ch3 cc127
		{0xB0 | 4, 5, 100}: true, // uni 7 ch5 → g517: ch4 cc5
	}
	for _, m := range out.msgs {
		if !want[m] {
			t.Errorf("unexpected msg %+v", m)
		}
		delete(want, m)
	}
	if len(want) != 0 {
		t.Errorf("missing msgs %v", want)
	}
}

func TestBudgetCapAndRoundRobin(t *testing.T) {
	store := artnet.NewStore()
	vals := map[int]byte{}
	for i := 0; i < 100; i++ {
		vals[i] = byte(i*2 + 2)
	}
	setUni(t, store, 0, 1, vals)
	s := newTestState(nil, 1000, store)
	out := &fakeOut{}
	s.scan()
	if s.dirtyCount != 100 {
		t.Fatalf("dirty %d, want 100", s.dirtyCount)
	}
	if n := s.flush(out, 30); n != 30 {
		t.Fatalf("flush 1 sent %d, want 30", n)
	}
	if s.dirtyCount != 70 {
		t.Fatalf("backlog %d, want 70", s.dirtyCount)
	}
	// Next flush resumes after the cursor - no channel sent twice.
	if n := s.flush(out, 100); n != 70 {
		t.Fatalf("flush 2 sent %d, want 70", n)
	}
	seen := map[byte]bool{}
	for _, m := range out.msgs {
		if seen[m.d1] {
			t.Fatalf("cc %d sent twice", m.d1)
		}
		seen[m.d1] = true
	}
}

func TestCoalescingKeepsLatestValue(t *testing.T) {
	store := artnet.NewStore()
	setUni(t, store, 0, 1, map[int]byte{9: 40})
	s := newTestState(nil, 1000, store)
	out := &fakeOut{}
	s.scan()
	setUni(t, store, 0, 2, map[int]byte{9: 80})
	setUni(t, store, 0, 3, map[int]byte{9: 120})
	s.scan() // re-scan while ch9 still pending - must stay one dirty entry, latest value
	if s.dirtyCount != 1 {
		t.Fatalf("dirty %d, want 1 (coalesced)", s.dirtyCount)
	}
	if n := s.flush(out, 10); n != 1 {
		t.Fatalf("sent %d, want 1", n)
	}
	if got := out.msgs[0]; got != (msg{0xB0, 9, 60}) {
		t.Fatalf("got %+v, want ch9=120>>1", got)
	}
}

func TestSubMIDIResolutionChangeIgnored(t *testing.T) {
	store := artnet.NewStore()
	setUni(t, store, 0, 1, map[int]byte{4: 10})
	s := newTestState(nil, 1000, store)
	out := &fakeOut{}
	s.scan()
	s.flush(out, 512)
	setUni(t, store, 0, 2, map[int]byte{4: 11}) // 10>>1 == 11>>1 - same 7-bit value
	s.scan()
	if s.dirtyCount != 0 {
		t.Fatalf("dirty %d, want 0 (7-bit unchanged)", s.dirtyCount)
	}
}

func TestStepPacesByElapsedTime(t *testing.T) {
	store := artnet.NewStore()
	vals := map[int]byte{}
	for i := 0; i < 200; i++ {
		vals[i] = 255
	}
	setUni(t, store, 0, 1, vals)
	s := newTestState(nil, 400, store) // 400/s → 8 per 20ms tick
	out := &fakeOut{}
	if n := s.step(out, 20*time.Millisecond); n != 8 {
		t.Fatalf("tick sent %d, want 8", n)
	}
	// A long stall banks at most 1s of budget.
	if n := s.step(out, 10*time.Second); n != 192 {
		t.Fatalf("stall tick sent %d, want remaining 192", n)
	}
}

func TestStartFailsWithoutPort(t *testing.T) {
	b := New(logbus.New(16), artnet.NewStore(), func() config.DMXMIDIFeature { return config.DMXMIDIFeature{Enabled: true} })
	b.openOut = func(string) (Out, string, error) { return nil, "", errors.New("no port") }
	if err := b.Start(context.Background()); err == nil {
		t.Fatal("Start should fail when the MIDI port can't open")
	}
	if b.Status().Running {
		t.Fatal("must not report running")
	}
}

func TestStartStopLifecycle(t *testing.T) {
	store := artnet.NewStore()
	out := &fakeOut{}
	b := New(logbus.New(16), store, func() config.DMXMIDIFeature { return config.DMXMIDIFeature{Enabled: true, MaxPerSecond: 1000} })
	b.openOut = func(string) (Out, string, error) { return out, "fake", nil }
	ctx, cancel := context.WithCancel(context.Background())
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if st := b.Status(); !st.Running || st.Port != "fake" {
		t.Fatalf("status %+v", st)
	}
	setUni(t, store, 0, 1, map[int]byte{0: 254})
	deadline := time.Now().Add(2 * time.Second)
	for b.Status().Sent == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Status().Sent == 0 {
		t.Fatal("no message sent")
	}
	cancel()
	deadline = time.Now().Add(2 * time.Second)
	for b.Status().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.Status().Running {
		t.Fatal("still running after cancel")
	}
}
