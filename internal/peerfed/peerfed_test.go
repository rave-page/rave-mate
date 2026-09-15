package peerfed

import (
	"context"
	"errors"
	"testing"
)

// fakeWorld is the manually-clocked test harness: peers + local state are set by the test,
// arm/disarm record the last transition. Stepping the watcher stands in for the ticker.
type fakeWorld struct {
	local   bool
	peers   []Peer
	linked  map[string]bool // nodeID -> Probe ok
	probeEr map[string]error

	armedNode string
	armedName string
	armedInfo string
	disarms   int
	arms      int
}

func (w *fakeWorld) watcher() Watcher[string] {
	return Watcher[string]{
		LocalActive: func() bool { return w.local },
		Peers:       func() []Peer { return w.peers },
		Probe: func(_ context.Context, node string) (string, bool, error) {
			if err := w.probeEr[node]; err != nil {
				return "", false, err
			}
			return "info-" + node, w.linked[node], nil
		},
		Arm: func(node, name, info string) {
			w.arms++
			w.armedNode, w.armedName, w.armedInfo = node, name, info
		},
		Disarm: func() {
			w.disarms++
			w.armedNode, w.armedName, w.armedInfo = "", "", ""
		},
	}
}

// TestArmDisarmLifecycle drives the full state machine: arm on a linked peer, stay armed
// while it is healthy, disarm on local login, and (independently) disarm on peer loss.
func TestArmDisarmLifecycle(t *testing.T) {
	w := &fakeWorld{
		peers:  []Peer{{NodeID: "A", Name: "Desk"}},
		linked: map[string]bool{"A": true},
	}
	wt := w.watcher()
	ctx := context.Background()

	// 1) no local session, peer A linked -> arm A.
	armed := wt.step(ctx, "")
	if armed != "A" || w.arms != 1 || w.armedNode != "A" || w.armedName != "Desk" || w.armedInfo != "info-A" {
		t.Fatalf("arm on peer: armed=%q arms=%d node=%q name=%q info=%q", armed, w.arms, w.armedNode, w.armedName, w.armedInfo)
	}

	// 2) re-probe: A still healthy -> stay armed, no new Arm/Disarm.
	armed = wt.step(ctx, armed)
	if armed != "A" || w.arms != 1 || w.disarms != 0 {
		t.Fatalf("re-probe should keep A: armed=%q arms=%d disarms=%d", armed, w.arms, w.disarms)
	}

	// 3) local login -> disarm, local always wins.
	w.local = true
	armed = wt.step(ctx, armed)
	if armed != "" || w.disarms != 1 || w.armedNode != "" {
		t.Fatalf("local login must disarm: armed=%q disarms=%d node=%q", armed, w.disarms, w.armedNode)
	}
	// while local, an available peer never re-arms.
	armed = wt.step(ctx, armed)
	if armed != "" || w.arms != 1 {
		t.Fatalf("local session must stay disarmed: armed=%q arms=%d", armed, w.arms)
	}
}

func TestDisarmOnPeerLoss(t *testing.T) {
	w := &fakeWorld{peers: []Peer{{NodeID: "A", Name: "Desk"}}, linked: map[string]bool{"A": true}}
	wt := w.watcher()
	ctx := context.Background()
	armed := wt.step(ctx, "")
	if armed != "A" {
		t.Fatalf("expected arm on A, got %q", armed)
	}
	// peer A drops off the connected list entirely.
	w.peers = nil
	armed = wt.step(ctx, armed)
	if armed != "" || w.disarms != 1 {
		t.Fatalf("peer loss must disarm: armed=%q disarms=%d", armed, w.disarms)
	}
}

func TestDisarmOnPeerUnlink(t *testing.T) {
	w := &fakeWorld{peers: []Peer{{NodeID: "A", Name: "Desk"}}, linked: map[string]bool{"A": true}}
	wt := w.watcher()
	ctx := context.Background()
	armed := wt.step(ctx, "")
	// A stays connected but unlinks its platform session (Probe ok=false).
	w.linked["A"] = false
	armed = wt.step(ctx, armed)
	if armed != "" || w.disarms != 1 {
		t.Fatalf("peer unlink must disarm: armed=%q disarms=%d", armed, w.disarms)
	}
}

// TestFailoverToSecondPeer: the armed peer unlinks while a second peer is still linked ->
// the loop switches to the healthy peer in the same pass (never leaves federation dark).
func TestFailoverToSecondPeer(t *testing.T) {
	w := &fakeWorld{
		peers:  []Peer{{NodeID: "A", Name: "Desk"}, {NodeID: "B", Name: "VRPC"}},
		linked: map[string]bool{"A": true, "B": true},
	}
	wt := w.watcher()
	ctx := context.Background()
	armed := wt.step(ctx, "") // arms first linked peer A
	if armed != "A" {
		t.Fatalf("expected A, got %q", armed)
	}
	// A goes unhealthy; B still linked -> arm B, one disarm (A) + one arm (B).
	w.probeEr = map[string]error{"A": errors.New("gone")}
	armed = wt.step(ctx, armed)
	if armed != "B" || w.armedNode != "B" {
		t.Fatalf("failover: armed=%q node=%q", armed, w.armedNode)
	}
	if w.disarms != 1 || w.arms != 2 {
		t.Fatalf("failover transitions: disarms=%d arms=%d", w.disarms, w.arms)
	}
}

// TestNoPeerNeverArms: no candidate peer -> stays disarmed, no callbacks.
func TestNoPeerNeverArms(t *testing.T) {
	w := &fakeWorld{}
	wt := w.watcher()
	if armed := wt.step(context.Background(), ""); armed != "" || w.arms != 0 || w.disarms != 0 {
		t.Fatalf("no peer must stay disarmed: armed=%q arms=%d disarms=%d", armed, w.arms, w.disarms)
	}
}
