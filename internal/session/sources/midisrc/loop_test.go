package midisrc

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/peerbridge"
	"rave.page/mate/internal/session"
)

// recDecoder records handled messages.
type recDecoder struct {
	mu   sync.Mutex
	msgs []midi.Message
}

func (d *recDecoder) id() string { return "rec" }
func (d *recDecoder) handle(_ time.Time, m midi.Message, _ func(session.Observation)) {
	d.mu.Lock()
	d.msgs = append(d.msgs, m)
	d.mu.Unlock()
}
func (d *recDecoder) tick(time.Time, func(session.Observation)) {}
func (d *recDecoder) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.msgs)
}

func noEmit(session.Observation) {}

func TestHandleLocalFiresForwardTap(t *testing.T) {
	s := New(logbus.New(8), "", "custom")
	var tapped []midi.Message
	s.SetForwarder(func(m midi.Message) { tapped = append(tapped, m) })
	dec := &recDecoder{}
	b := portBinding{name: "custom", decoders: []decoder{dec}, acceptsInject: true}

	s.handleLocal(b, time.Now(), midi.Message{Status: 0x90, Data1: 60, Data2: 100}, noEmit)
	if len(tapped) != 1 || dec.count() != 1 {
		t.Fatalf("local data msg: want tap=1 decode=1, got tap=%d decode=%d", len(tapped), dec.count())
	}

	// System/real-time (clock) never forwards but still decodes.
	s.handleLocal(b, time.Now(), midi.Message{Status: 0xF8}, noEmit)
	if len(tapped) != 1 || dec.count() != 2 {
		t.Fatalf("system msg: want tap=1 decode=2, got tap=%d decode=%d", len(tapped), dec.count())
	}
}

// Peer-injected MIDI reaches the decoders but NEVER the forward tap - the structural
// guarantee that a bridged-in message can't re-enter ForwardMIDI and loop between peers.
func TestHandleInjectedNeverFiresForwardTap(t *testing.T) {
	s := New(logbus.New(8), "", "custom")
	tapCount := 0
	s.SetForwarder(func(midi.Message) { tapCount++ })
	dec := &recDecoder{}
	b := portBinding{name: "custom", decoders: []decoder{dec}, acceptsInject: true}

	s.handleInjected(b, time.Now(), midi.Message{Status: 0x90, Data1: 64, Data2: 127}, noEmit)
	if tapCount != 0 {
		t.Fatalf("injected msg hit the forward tap %d times (loop path!)", tapCount)
	}
	if dec.count() != 1 {
		t.Fatalf("injected msg: want decode=1, got %d", dec.count())
	}
}

// loopLink is a peerbridge link whose broadcasts are delivered to the other instance
// (transport stand-in). sends counts outbound frames.
type loopLink struct {
	mu          sync.Mutex
	sends       int
	onBroadcast func(payload []byte)
}

func (l *loopLink) SetDataHandler(func(peerNodeID, channel string, payload []byte)) {}
func (l *loopLink) SendTo(_, _ string, _ []byte) error {
	l.mu.Lock()
	l.sends++
	l.mu.Unlock()
	return nil
}
func (l *loopLink) Broadcast(_ string, payload []byte) {
	l.mu.Lock()
	l.sends++
	fn := l.onBroadcast
	l.mu.Unlock()
	if fn != nil {
		fn(payload)
	}
}
func (l *loopLink) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sends
}

// Full mesh chain, wired like app.go on BOTH instances: local port msg on A → forward tap →
// bridge A broadcast → transport → B receives it as injected MIDI (peerbridge delivers inbound
// ChanMIDI to the app sink, which Injects into midisrc; the inject drain is handleInjected).
// The message must reach B's decoders exactly once and must never echo back to A. A wrong
// wiring (injected feeding the tap) would recurse A→B→A… - the send counters catch it.
func TestMeshNoEchoLoopAcrossInstances(t *testing.T) {
	mk := func() (*Source, *loopLink, *peerbridge.Bridge, *recDecoder, portBinding) {
		s := New(logbus.New(8), "", "custom")
		link := &loopLink{}
		br := peerbridge.New(logbus.New(8), link)
		br.SetMIDIMesh(true)
		s.SetForwarder(func(m midi.Message) { br.ForwardMIDI(m.Status, m.Data1, m.Data2) })
		dec := &recDecoder{}
		return s, link, br, dec, portBinding{name: "custom", decoders: []decoder{dec}, acceptsInject: true}
	}
	srcA, linkA, _, decA, bindA := mk()
	srcB, linkB, _, decB, bindB := mk()

	inject := func(s *Source, b portBinding) func(payload []byte) {
		return func(payload []byte) {
			var mm peerbridge.MIDIMsg
			if json.Unmarshal(payload, &mm) == nil {
				s.handleInjected(b, time.Now(), midi.Message{Status: mm.S, Data1: mm.D1, Data2: mm.D2}, noEmit)
			}
		}
	}
	linkA.onBroadcast = inject(srcB, bindB)
	linkB.onBroadcast = inject(srcA, bindA)

	// Controller note-on hits A's local port.
	srcA.handleLocal(bindA, time.Now(), midi.Message{Status: 0x90, Data1: 60, Data2: 100}, noEmit)

	if got := linkA.count(); got != 1 {
		t.Fatalf("A must forward exactly once, sent %d", got)
	}
	if got := linkB.count(); got != 0 {
		t.Fatalf("B echoed bridged-in MIDI back to the mesh (loop!): %d sends", got)
	}
	if decA.count() != 1 || decB.count() != 1 {
		t.Fatalf("each instance decodes once: A=%d B=%d", decA.count(), decB.count())
	}
}
