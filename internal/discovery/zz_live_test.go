//go:build manual

package discovery

import (
	"context"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// Two real Discovery instances on this host must find each other (multicast loopback +
// SO_REUSEADDR). Run: go test -tags manual -run TestLiveSameHost ./internal/discovery/
func TestLiveSameHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	a := New(Service{NodeID: "nodeA", Name: "A", Port: 47620, ProtoVersion: 1, IdentityFP: "nodeA"}, logbus.New(64))
	b := New(Service{NodeID: "nodeB", Name: "B", Port: 47621, ProtoVersion: 1, IdentityFP: "nodeB"}, logbus.New(64))
	if err := a.Start(ctx); err != nil {
		t.Fatalf("A start: %v", err)
	}
	defer a.Stop()
	if err := b.Start(ctx); err != nil {
		t.Fatalf("B start: %v", err)
	}
	defer b.Stop()

	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		aSeesB, bSeesA := false, false
		for _, p := range a.Peers() {
			if p.NodeID == "nodeB" {
				aSeesB = true
			}
		}
		for _, p := range b.Peers() {
			if p.NodeID == "nodeA" {
				bSeesA = true
			}
		}
		if aSeesB && bSeesA {
			t.Logf("OK: A sees B and B sees A")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("instances did not discover each other; A peers=%+v B peers=%+v", a.Peers(), b.Peers())
}
