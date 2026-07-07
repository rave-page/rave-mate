package peerlink

import (
	"bytes"
	"testing"
)

// Both ends of a completed handshake must derive the identical MediaSecret, and it must be
// domain-separated from the raw session key + the frame-MAC bind key.
func TestMediaSecretMatchesAndDomainSeparated(t *testing.T) {
	a, b := newPipe()
	idA, idB := newIdentity(t), newIdentity(t)
	ia, ib := run(a, b, idA, idB, nil, nil)
	if ia.err != nil || ib.err != nil {
		t.Fatalf("handshake errored: %v / %v", ia.err, ib.err)
	}
	sa, err := mediaSecret(ia.res)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := mediaSecret(ib.res)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sa, sb) {
		t.Fatal("media secrets differ across ends")
	}
	if len(sa) != 32 {
		t.Fatalf("media secret length = %d, want 32", len(sa))
	}
	if bytes.Equal(sa, ia.res.SessionKey) {
		t.Fatal("media secret must not equal the raw session key")
	}
	if bytes.Equal(sa, ia.res.BindKey) {
		t.Fatal("media secret must not equal the frame-MAC bind key")
	}
}
