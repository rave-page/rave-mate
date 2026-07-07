package peerlink

import (
	"context"
	"testing"
	"time"
)

// twoLinks wires two Links over an in-memory pipe sharing one bind key (as a completed
// handshake would). Returns sender, and a channel the receiver pushes (channel,payload) onto.
func twoLinks(t *testing.T, bindKey []byte) (*Link, <-chan [2]string, context.CancelFunc) {
	t.Helper()
	a, b := newPipe()
	sender := newLink(a, &Result{BindKey: bindKey, PeerNodeID: "peer-a"})
	recv := newLink(b, &Result{BindKey: bindKey, PeerNodeID: "peer-b"})

	got := make(chan [2]string, 8)
	recv.onFrame = func(ty string, mp map[string]any) {
		if ty == frameData {
			ch, _ := mp["ch"].(string)
			data, _ := mp["data"].(string)
			got <- [2]string{ch, data}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go recv.readLoop(ctx)
	return sender, got, cancel
}

func TestDataChannelRoundTrip(t *testing.T) {
	sender, got, cancel := twoLinks(t, []byte("0123456789abcdef0123456789abcdef"))
	defer cancel()

	if err := sender.SendData(context.Background(), ChanSession, []byte(`{"track":"x"}`)); err != nil {
		t.Fatalf("send session: %v", err)
	}
	if err := sender.SendData(context.Background(), ChanMIDI, []byte(`{"b0":1}`)); err != nil {
		t.Fatalf("send midi: %v", err)
	}
	for i, want := range [][2]string{{ChanSession, `{"track":"x"}`}, {ChanMIDI, `{"b0":1}`}} {
		select {
		case g := <-got:
			if g != want {
				t.Fatalf("frame %d: got %v want %v", i, g, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("frame %d: timeout", i)
		}
	}
}

// TestDataChannelTamperRejected: a flipped payload byte breaks the MAC → receiver stops
// dispatching (drops the link), so nothing arrives.
func TestDataChannelTamperRejected(t *testing.T) {
	a, b := newPipe()
	a.tamper = func(_ int, raw []byte) []byte {
		for i, c := range raw {
			if c == 'x' { // corrupt the payload after the MAC was computed
				raw[i] = 'y'
				break
			}
		}
		return raw
	}
	sender := newLink(a, &Result{BindKey: []byte("0123456789abcdef0123456789abcdef"), PeerNodeID: "a"})
	recv := newLink(b, &Result{BindKey: []byte("0123456789abcdef0123456789abcdef"), PeerNodeID: "b"})
	got := make(chan struct{}, 1)
	recv.onFrame = func(ty string, _ map[string]any) {
		if ty == frameData {
			got <- struct{}{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go recv.readLoop(ctx)

	_ = sender.SendData(context.Background(), ChanSession, []byte(`track-x`))
	select {
	case <-got:
		t.Fatal("tampered frame should have been rejected, not dispatched")
	case <-time.After(300 * time.Millisecond):
		// expected: nothing dispatched
	}
}
