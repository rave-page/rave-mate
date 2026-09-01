package wirecrypto

import (
	"bytes"
	"testing"
)

func TestDuplexSealerRoundTrip(t *testing.T) {
	master := bytes.Repeat([]byte{0xab}, 32)
	as, ar, err := NewDuplexSealer(master, nil, true, "dir-a", "dir-b")
	if err != nil {
		t.Fatal(err)
	}
	bs, br, err := NewDuplexSealer(master, nil, false, "dir-a", "dir-b")
	if err != nil {
		t.Fatal(err)
	}
	// A→B and B→A both round-trip, in order.
	for i, msg := range [][]byte{[]byte("hello"), []byte("world"), {}, bytes.Repeat([]byte("x"), 4096)} {
		ct := as.Seal(nil, msg)
		if bytes.Contains(ct, msg) && len(msg) > 0 {
			t.Fatalf("frame %d: plaintext visible in ciphertext", i)
		}
		pt, err := br.Open(nil, ct)
		if err != nil {
			t.Fatalf("frame %d A→B open: %v", i, err)
		}
		if !bytes.Equal(pt, msg) {
			t.Fatalf("frame %d A→B: got %q want %q", i, pt, msg)
		}
		rt, err := ar.Open(nil, bs.Seal(nil, msg))
		if err != nil || !bytes.Equal(rt, msg) {
			t.Fatalf("frame %d B→A: %q err %v", i, rt, err)
		}
	}
}

// TestSealerNonceSequence: identical plaintexts seal to distinct ciphertexts (the counter
// advances), and the receiver must open them in the SAME order (lockstep counters).
func TestSealerNonceSequence(t *testing.T) {
	master := bytes.Repeat([]byte{0x01}, 32)
	s, _, _ := NewDuplexSealer(master, nil, true, "a", "b")
	_, r, _ := NewDuplexSealer(master, nil, false, "a", "b")
	c0 := s.Seal(nil, []byte("same"))
	c1 := s.Seal(nil, []byte("same"))
	if bytes.Equal(c0, c1) {
		t.Fatal("repeated plaintext produced identical ciphertext (nonce reuse)")
	}
	if _, err := r.Open(nil, c0); err != nil {
		t.Fatalf("open c0: %v", err)
	}
	// Opening c1 as the SECOND frame succeeds; opening it first would have failed the AEAD.
	if _, err := r.Open(nil, c1); err != nil {
		t.Fatalf("open c1: %v", err)
	}
}

// TestSealerDirectionSeparation: the send and recv keys differ, so a frame the initiator sealed
// on its send direction cannot be opened by the initiator's own recv Sealer.
func TestSealerDirectionSeparation(t *testing.T) {
	master := bytes.Repeat([]byte{0x02}, 32)
	send, recv, _ := NewDuplexSealer(master, nil, true, "a", "b")
	ct := send.Seal(nil, []byte("payload"))
	if _, err := recv.Open(nil, ct); err == nil {
		t.Fatal("initiator recv opened an initiator-sent frame: send and recv share a key")
	}
}

// TestSealerTamperRejected: any bit flip in the ciphertext (or tag) fails Open.
func TestSealerTamperRejected(t *testing.T) {
	master := bytes.Repeat([]byte{0x03}, 32)
	s, _, _ := NewDuplexSealer(master, nil, true, "a", "b")
	_, r, _ := NewDuplexSealer(master, nil, false, "a", "b")
	ct := s.Seal(nil, []byte("integrity"))
	ct[0] ^= 0x80
	if _, err := r.Open(nil, ct); err == nil {
		t.Fatal("tampered ciphertext opened without error")
	}
}

// TestSealerSaltDomainSeparation: a different HKDF salt yields disjoint keys (filexfer salts per
// transfer id), so a frame sealed under salt A never opens under salt B.
func TestSealerSaltDomainSeparation(t *testing.T) {
	master := bytes.Repeat([]byte{0x04}, 32)
	sA, _, _ := NewDuplexSealer(master, []byte("xfer-A"), true, "a", "b")
	_, rB, _ := NewDuplexSealer(master, []byte("xfer-B"), false, "a", "b")
	if _, err := rB.Open(nil, sA.Seal(nil, []byte("x"))); err == nil {
		t.Fatal("salt did not separate keys")
	}
}
