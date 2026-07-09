package midiemit

import (
	"sync"
	"testing"
	"time"
)

// fakeOut records every short message + Close, for asserting emitted bytes without real hardware.
type fakeOut struct {
	mu     sync.Mutex
	msgs   [][3]byte
	closed int
}

func (f *fakeOut) Send(s, d1, d2 byte) {
	f.mu.Lock()
	f.msgs = append(f.msgs, [3]byte{s, d1, d2})
	f.mu.Unlock()
}
func (f *fakeOut) Close() {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
}
func (f *fakeOut) last() [3]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		return [3]byte{}
	}
	return f.msgs[len(f.msgs)-1]
}
func (f *fakeOut) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.msgs)
}

// newFake builds an emitter over a fakeOut (no real port opened).
func newFake(want string) (*Emitter, *fakeOut, *int) {
	f := &fakeOut{}
	opens := 0
	e := &Emitter{want: want, openFn: func(s string) (Out, string, error) {
		opens++
		return f, "FakePort:" + s, nil
	}}
	return e, f, &opens
}

func TestSendCC(t *testing.T) {
	e, f, _ := newFake("")
	if err := e.SendCC(0, 20, 100); err != nil {
		t.Fatalf("SendCC: %v", err)
	}
	if got, want := f.last(), [3]byte{0xB0, 20, 100}; got != want {
		t.Fatalf("CC ch0: got %v want %v", got, want)
	}
	// channel + value masking: ch 15 → 0xBF, val clamped to 7-bit.
	if err := e.SendCC(15, 200, 200); err != nil {
		t.Fatalf("SendCC: %v", err)
	}
	if got, want := f.last(), [3]byte{0xBF, 200 & 0x7F, 200 & 0x7F}; got != want {
		t.Fatalf("CC ch15 mask: got %v want %v", got, want)
	}
}

func TestSendNote(t *testing.T) {
	e, f, _ := newFake("")
	if err := e.SendNoteOn(0, 36, 127); err != nil {
		t.Fatalf("NoteOn: %v", err)
	}
	if got, want := f.last(), [3]byte{0x90, 36, 127}; got != want {
		t.Fatalf("NoteOn: got %v want %v", got, want)
	}
	if err := e.SendNoteOff(0, 36); err != nil {
		t.Fatalf("NoteOff: %v", err)
	}
	if got, want := f.last(), [3]byte{0x80, 36, 0}; got != want {
		t.Fatalf("NoteOff: got %v want %v", got, want)
	}
}

func TestTriggerPadAutoNoteOff(t *testing.T) {
	e, f, _ := newFake("")
	if err := e.TriggerPad(0, 40, 127); err != nil {
		t.Fatalf("TriggerPad: %v", err)
	}
	if got, want := f.msgs[0], [3]byte{0x90, 40, 127}; got != want {
		t.Fatalf("pad NoteOn: got %v want %v", got, want)
	}
	// the bounded timer fires the matching Note Off shortly after.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if f.count() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, want := f.last(), [3]byte{0x80, 40, 0}; got != want {
		t.Fatalf("pad auto NoteOff: got %v want %v", got, want)
	}
}

func TestPanic(t *testing.T) {
	e, f, _ := newFake("")
	e.Panic(0, 36, 39)
	// CC123=0 then a Note Off for 36..39.
	want := [][3]byte{{0xB0, 123, 0}, {0x80, 36, 0}, {0x80, 37, 0}, {0x80, 38, 0}, {0x80, 39, 0}}
	if f.count() != len(want) {
		t.Fatalf("panic count: got %d want %d (%v)", f.count(), len(want), f.msgs)
	}
	for i, w := range want {
		if f.msgs[i] != w {
			t.Fatalf("panic msg %d: got %v want %v", i, f.msgs[i], w)
		}
	}
}

func TestLazyOpenOncePerPort(t *testing.T) {
	e, _, opens := newFake("")
	_ = e.SendCC(0, 20, 1)
	_ = e.SendCC(0, 20, 2)
	if *opens != 1 {
		t.Fatalf("expected 1 lazy open, got %d", *opens)
	}
	if e.ActivePort() != "FakePort:" {
		t.Fatalf("ActivePort: got %q", e.ActivePort())
	}
}

func TestSetPortReopens(t *testing.T) {
	e, f, opens := newFake("")
	_ = e.SendCC(0, 20, 1) // opens (1)
	e.SetPort("loopbe")
	if f.closed != 1 {
		t.Fatalf("SetPort should close the old port once, got %d", f.closed)
	}
	if e.Want() != "loopbe" {
		t.Fatalf("Want after SetPort: got %q", e.Want())
	}
	_ = e.SendCC(0, 20, 2) // reopens (2)
	if *opens != 2 {
		t.Fatalf("expected reopen, opens=%d", *opens)
	}
	if e.ActivePort() != "FakePort:loopbe" {
		t.Fatalf("ActivePort after SetPort: got %q", e.ActivePort())
	}
}

func TestCloseThenSendFails(t *testing.T) {
	e, f, _ := newFake("")
	_ = e.SendCC(0, 20, 1)
	e.Close()
	if f.closed != 1 {
		t.Fatalf("Close should release the port, got %d", f.closed)
	}
	if err := e.SendCC(0, 20, 2); err == nil {
		t.Fatal("send after Close should error")
	}
	e.Close() // idempotent
}
