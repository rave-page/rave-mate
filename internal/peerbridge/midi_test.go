package peerbridge

import (
	"encoding/json"
	"sync"
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peerlink"
)

// sentFrame is one recorded send; to=="" marks a broadcast.
type sentFrame struct {
	to, channel string
	payload     []byte
}

// fakeLink is an in-memory linkManager recording every send.
type fakeLink struct {
	mu      sync.Mutex
	handler func(peerNodeID, channel string, payload []byte)
	sent    []sentFrame
}

func (f *fakeLink) SetDataHandler(fn func(peerNodeID, channel string, payload []byte)) {
	f.mu.Lock()
	f.handler = fn
	f.mu.Unlock()
}

func (f *fakeLink) SendTo(nodeID, channel string, payload []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, sentFrame{nodeID, channel, payload})
	f.mu.Unlock()
	return nil
}

func (f *fakeLink) Broadcast(channel string, payload []byte) {
	f.mu.Lock()
	f.sent = append(f.sent, sentFrame{"", channel, payload})
	f.mu.Unlock()
}

func (f *fakeLink) frames() []sentFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentFrame(nil), f.sent...)
}

func newTestBridge() (*Bridge, *fakeLink) {
	link := &fakeLink{}
	return New(logbus.New(8), link), link
}

func TestForwardMIDIOffByDefault(t *testing.T) {
	b, link := newTestBridge()
	b.ForwardMIDI(0x90, 60, 100)
	if n := len(link.frames()); n != 0 {
		t.Fatalf("forwarding off: want 0 sends, got %d", n)
	}
}

func TestForwardMIDIDirected(t *testing.T) {
	b, link := newTestBridge()
	b.SetControlTarget("peer-b")
	b.SetMIDIForwarding(true)
	b.ForwardMIDI(0x90, 60, 100)

	fs := link.frames()
	if len(fs) != 1 {
		t.Fatalf("want 1 send, got %d", len(fs))
	}
	if fs[0].to != "peer-b" || fs[0].channel != peerlink.ChanMIDI {
		t.Fatalf("want directed send to peer-b on %s, got to=%q channel=%q", peerlink.ChanMIDI, fs[0].to, fs[0].channel)
	}
	var mm MIDIMsg
	if err := json.Unmarshal(fs[0].payload, &mm); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if mm != (MIDIMsg{S: 0x90, D1: 60, D2: 100}) {
		t.Fatalf("payload mismatch: %+v", mm)
	}
}

func TestForwardMIDIDirectedNoTargetBroadcasts(t *testing.T) {
	b, link := newTestBridge()
	b.SetMIDIForwarding(true)
	b.ForwardMIDI(0xB0, 1, 2)

	fs := link.frames()
	if len(fs) != 1 || fs[0].to != "" {
		t.Fatalf("want 1 broadcast, got %+v", fs)
	}
}

func TestForwardMIDIMeshBroadcastsIgnoringTarget(t *testing.T) {
	b, link := newTestBridge()
	b.SetControlTarget("peer-b") // stale/narrow directed target must not limit mesh
	b.SetMIDIMesh(true)
	b.ForwardMIDI(0x90, 61, 90)

	fs := link.frames()
	if len(fs) != 1 || fs[0].to != "" || fs[0].channel != peerlink.ChanMIDI {
		t.Fatalf("mesh: want 1 broadcast on %s, got %+v", peerlink.ChanMIDI, fs)
	}
}

func TestForwardMIDIMeshPlusDirectedSingleBroadcast(t *testing.T) {
	b, link := newTestBridge()
	b.SetControlTarget("peer-b")
	b.SetMIDIForwarding(true)
	b.SetMIDIMesh(true)
	b.ForwardMIDI(0x90, 62, 80)

	// broadcast covers the directed target - no duplicate directed send
	fs := link.frames()
	if len(fs) != 1 || fs[0].to != "" {
		t.Fatalf("mesh+directed: want exactly 1 broadcast, got %+v", fs)
	}
}

func TestMIDIMeshToggle(t *testing.T) {
	b, link := newTestBridge()
	b.SetMIDIMesh(true)
	if !b.MIDIMesh() {
		t.Fatal("mesh should report on")
	}
	b.SetMIDIMesh(false)
	if b.MIDIMesh() {
		t.Fatal("mesh should report off")
	}
	b.ForwardMIDI(0x90, 60, 100)
	if n := len(link.frames()); n != 0 {
		t.Fatalf("mesh off: want 0 sends, got %d", n)
	}
}

// Inbound bridged MIDI must reach the sink and NEVER trigger an outbound send from the
// bridge itself - the only re-forward path would be the app's midisrc tap, which fires only
// for locally-sourced port messages (covered in midisrc tests).
func TestInboundMIDIDeliveredToSinkNotReForwarded(t *testing.T) {
	b, link := newTestBridge()
	b.SetMIDIMesh(true) // worst case: mesh armed while a peer message arrives

	var got []MIDIMsg
	b.SetMIDISink(func(peer string, payload []byte) {
		var mm MIDIMsg
		if err := json.Unmarshal(payload, &mm); err != nil {
			t.Fatalf("sink decode: %v", err)
		}
		if peer != "peer-a" {
			t.Fatalf("want origin peer-a, got %q", peer)
		}
		got = append(got, mm)
	})

	raw, err := json.Marshal(MIDIMsg{S: 0x90, D1: 64, D2: 127})
	if err != nil {
		t.Fatal(err)
	}
	b.onData("peer-a", peerlink.ChanMIDI, raw)

	if len(got) != 1 {
		t.Fatalf("want 1 sink delivery, got %d", len(got))
	}
	if n := len(link.frames()); n != 0 {
		t.Fatalf("inbound MIDI re-forwarded (loop!): %d sends", n)
	}
}
