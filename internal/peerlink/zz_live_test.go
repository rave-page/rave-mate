//go:build manual

package peerlink

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peers"
	"rave.page/mate/internal/store"
)

// Full real-socket pairing over websockets: two Managers, auto-confirming SAS, must reach
// StatusConnected and persist each other as trusted.
// Run: go test -tags manual -run TestLivePairing ./internal/peerlink/
func TestLivePairing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	mk := func(name string) (*Manager, *peers.Store) {
		st, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		id, _ := identity.LoadOrCreate(st)
		ps := peers.New(st)
		m := New(id, ps, logbus.New(64))
		m.SetCallbacks(func(req SASRequest) { m.ConfirmSAS(req.NodeID, true) }, nil)
		if err := m.Start(ctx); err != nil {
			t.Fatalf("%s start: %v", name, err)
		}
		t.Cleanup(m.Stop)
		return m, ps
	}

	mA, psA := mk("A")
	mB, psB := mk("B")

	mA.Connect(discovery.Peer{NodeID: mB.id.NodeID, Address: net.IPv4(127, 0, 0, 1), Port: mB.Port()})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if connected(mA) && connected(mB) {
			if _, okA := psA.TrustedKey(mB.id.NodeID); okA {
				if _, okB := psB.TrustedKey(mA.id.NodeID); okB {
					t.Log("OK: both connected and persisted trust")
					return
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("pairing did not complete: A=%+v B=%+v", mA.Connections(), mB.Connections())
}

func connected(m *Manager) bool {
	for _, c := range m.Connections() {
		if c.Status == StatusConnected {
			return true
		}
	}
	return false
}
