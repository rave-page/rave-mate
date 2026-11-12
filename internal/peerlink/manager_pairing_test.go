package peerlink

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peers"
	"rave.page/mate/internal/store"
)

// Manager-level SAS pairing over an in-memory pipe (no sockets): two Managers run the handshake,
// each raises the SAS to its listener, and trust is persisted ONLY when BOTH ends confirm. This is
// the trust path the headless `ctl pair` / `ctl pair-confirm` front-ends drive.

func mkPairMgr(t *testing.T, name string) (*Manager, *peers.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	id, err := identity.LoadOrCreate(st)
	if err != nil {
		t.Fatal(err)
	}
	m := New(id, peers.New(st), logbus.New(32))
	m.ctx = context.Background() // runHandshake uses m.ctx; no listener needed for the pipe path
	return m, peers.New(st)
}

// drivePair connects two managers over a pipe and returns their SAS channels.
func drivePair(t *testing.T, mA, mB *Manager) (<-chan SASRequest, <-chan SASRequest) {
	t.Helper()
	sasA := make(chan SASRequest, 1)
	sasB := make(chan SASRequest, 1)
	mA.AddListener(func(r SASRequest) {
		select {
		case sasA <- r:
		default:
		}
	}, nil)
	mB.AddListener(func(r SASRequest) {
		select {
		case sasB <- r:
		default:
		}
	}, nil)
	a, b := newPipe()
	go mA.runHandshake(a, roleInitiator, "B", "pipeB", "")
	go mB.runHandshake(b, roleResponder, "A", "pipeA", "")
	return sasA, sasB
}

func recvSAS(t *testing.T, ch <-chan SASRequest) SASRequest {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SAS prompt")
		return SASRequest{}
	}
}

func waitTrusted(ps *peers.Store, nodeID string) bool {
	for i := 0; i < 40; i++ {
		if _, ok := ps.TrustedKey(nodeID); ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestManagerPairBothConfirm(t *testing.T) {
	mA, psA := mkPairMgr(t, "A")
	mB, psB := mkPairMgr(t, "B")
	sasA, sasB := drivePair(t, mA, mB)

	rA, rB := recvSAS(t, sasA), recvSAS(t, sasB)
	if rA.SAS != rB.SAS {
		t.Fatalf("SAS differ across ends: %q vs %q", rA.SAS, rB.SAS)
	}
	if len(rA.SAS) != sasDigits {
		t.Fatalf("SAS wrong length: %q", rA.SAS)
	}

	// Both humans confirm → mutual trust persisted.
	mA.ConfirmSAS(mB.id.NodeID, true)
	mB.ConfirmSAS(mA.id.NodeID, true)
	if !waitTrusted(psA, mB.id.NodeID) {
		t.Fatal("A did not persist trust for B after both confirmed")
	}
	if !waitTrusted(psB, mA.id.NodeID) {
		t.Fatal("B did not persist trust for A after both confirmed")
	}
}

func TestManagerPairRejectNotTrusted(t *testing.T) {
	mA, psA := mkPairMgr(t, "A")
	mB, psB := mkPairMgr(t, "B")
	sasA, sasB := drivePair(t, mA, mB)
	recvSAS(t, sasA)
	recvSAS(t, sasB)

	mA.ConfirmSAS(mB.id.NodeID, false) // reject on one end aborts the pairing
	time.Sleep(400 * time.Millisecond)
	if _, ok := psA.TrustedKey(mB.id.NodeID); ok {
		t.Fatal("A trusted B after a reject")
	}
	if _, ok := psB.TrustedKey(mA.id.NodeID); ok {
		t.Fatal("B trusted A after a reject")
	}
}

func TestManagerPairOneSideOnlyNotTrusted(t *testing.T) {
	mA, psA := mkPairMgr(t, "A")
	mB, psB := mkPairMgr(t, "B")
	sasA, sasB := drivePair(t, mA, mB)
	recvSAS(t, sasA)
	recvSAS(t, sasB)

	mA.ConfirmSAS(mB.id.NodeID, true) // only A confirms; B never does
	time.Sleep(500 * time.Millisecond)
	if _, ok := psA.TrustedKey(mB.id.NodeID); ok {
		t.Fatal("A trusted B with only one confirm")
	}
	if _, ok := psB.TrustedKey(mA.id.NodeID); ok {
		t.Fatal("B trusted A with only one confirm")
	}
}
