package identity

import (
	"path/filepath"
	"testing"

	"rave.page/mate/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "id.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestLoadOrCreateIdempotent(t *testing.T) {
	st := openStore(t)

	a, err := LoadOrCreate(st)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	b, err := LoadOrCreate(st)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if a.NodeID != b.NodeID {
		t.Errorf("node id changed across loads: %q vs %q", a.NodeID, b.NodeID)
	}
	if a.NodeID == "" {
		t.Error("empty node id")
	}
	if !a.Pub.Equal(b.Pub) {
		t.Error("public key changed across loads")
	}
}

func TestSignVerifyAndNodeID(t *testing.T) {
	id, err := LoadOrCreate(openStore(t))
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("transcript-bytes")
	sig := id.Sign(msg)
	if !VerifyPeer(id.Pub, msg, sig) {
		t.Error("valid signature rejected")
	}
	if VerifyPeer(id.Pub, []byte("tampered"), sig) {
		t.Error("signature verified against wrong message")
	}
	if id.NodeID != NodeIDFromPub(id.Pub) {
		t.Error("NodeID not derived from pub")
	}
	if id.PubFingerprint() != id.NodeID {
		t.Error("fingerprint != node id")
	}
}

func TestNilStoreEphemeral(t *testing.T) {
	id, err := LoadOrCreate(nil)
	if err != nil {
		t.Fatalf("nil-store LoadOrCreate: %v", err)
	}
	if id.NodeID == "" {
		t.Error("expected an ephemeral identity with a node id")
	}
}
