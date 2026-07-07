package peerlink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/wirecrypto"
)

// ── in-memory transport ──────────────────────────────────────────────────────

type pipeConn struct {
	in     chan []byte
	out    chan []byte
	tamper func(idx int, b []byte) []byte
	sent   int
}

func newPipe() (*pipeConn, *pipeConn) {
	a2b := make(chan []byte, 8)
	b2a := make(chan []byte, 8)
	return &pipeConn{in: b2a, out: a2b}, &pipeConn{in: a2b, out: b2a}
}

func (p *pipeConn) Send(_ context.Context, b []byte) error {
	cp := append([]byte(nil), b...)
	if p.tamper != nil {
		cp = p.tamper(p.sent, cp)
	}
	p.sent++
	p.out <- cp
	return nil
}

func (p *pipeConn) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *pipeConn) Close() {}

func newIdentity(t *testing.T) *identity.Identity {
	t.Helper()
	id, err := identity.LoadOrCreate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type hsOut struct {
	res *Result
	err error
}

// run drives both ends concurrently and returns their results.
func run(initConn, respConn Conn, initID, respID *identity.Identity, initTrust, respTrust TrustLookup) (hsOut, hsOut) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ic, rc := make(chan hsOut, 1), make(chan hsOut, 1)
	go func() { r, e := doHandshake(ctx, initConn, roleInitiator, initID, initTrust); ic <- hsOut{r, e} }()
	go func() { r, e := doHandshake(ctx, respConn, roleResponder, respID, respTrust); rc <- hsOut{r, e} }()
	return <-ic, <-rc
}

func TestHandshakeHappyPath(t *testing.T) {
	a, b := newPipe()
	idA, idB := newIdentity(t), newIdentity(t)
	ia, ib := run(a, b, idA, idB, nil, nil)
	if ia.err != nil || ib.err != nil {
		t.Fatalf("handshake errored: init=%v resp=%v", ia.err, ib.err)
	}
	if ia.res.SAS != ib.res.SAS {
		t.Errorf("SAS mismatch: %q vs %q", ia.res.SAS, ib.res.SAS)
	}
	if len(ia.res.SAS) != sasDigits {
		t.Errorf("SAS wrong length: %q", ia.res.SAS)
	}
	if !bytes.Equal(ia.res.SessionKey, ib.res.SessionKey) {
		t.Error("session keys differ")
	}
	if !bytes.Equal(ia.res.BindKey, ib.res.BindKey) {
		t.Error("bind keys differ")
	}
	if ia.res.PeerNodeID != idB.NodeID || ib.res.PeerNodeID != idA.NodeID {
		t.Error("peer node ids wrong")
	}
	if ia.res.Trusted || ib.res.Trusted {
		t.Error("first pairing should not be pre-trusted")
	}
}

func TestHandshakeTrustedAndChangedKey(t *testing.T) {
	idA, idB := newIdentity(t), newIdentity(t)

	// idB is trusted with its real key → no SAS needed on A's side.
	a, b := newPipe()
	trustReal := func(node string) (ed25519.PublicKey, bool) {
		if node == idB.NodeID {
			return idB.Pub, true
		}
		return nil, false
	}
	ia, ib := run(a, b, idA, idB, trustReal, nil)
	if ia.err != nil || ib.err != nil {
		t.Fatalf("trusted handshake errored: %v / %v", ia.err, ib.err)
	}
	if !ia.res.Trusted {
		t.Error("expected initiator to treat known matching key as trusted")
	}

	// idB's node id stored with a DIFFERENT key → reject loudly.
	a2, b2 := newPipe()
	wrong := newIdentity(t)
	trustWrong := func(node string) (ed25519.PublicKey, bool) {
		if node == idB.NodeID {
			return wrong.Pub, true
		}
		return nil, false
	}
	ia2, _ := run(a2, b2, idA, idB, trustWrong, nil)
	if ia2.err != ErrKeyChanged {
		t.Fatalf("expected ErrKeyChanged, got %v", ia2.err)
	}
}

// TestSASMitmProperty: an attacker M relaying between A and B runs two independent
// sessions, so the SAS A computes (with M) differs from the SAS B computes (with M).
func TestSASMitmProperty(t *testing.T) {
	idA, idB, idM := newIdentity(t), newIdentity(t), newIdentity(t)

	aConn, mForA := newPipe()
	bConn, mForB := newPipe()
	// A <-> M
	ia, ma := run(aConn, mForA, idA, idM, nil, nil)
	// B <-> M
	ib, mb := run(bConn, mForB, idB, idM, nil, nil)
	for _, o := range []hsOut{ia, ma, ib, mb} {
		if o.err != nil {
			t.Fatalf("leg errored: %v", o.err)
		}
	}
	// The two legitimate parties would be shown different codes → mismatch → abort.
	if ia.res.SAS == ib.res.SAS {
		t.Error("SAS collided across two relayed sessions - MITM would be undetectable")
	}
}

func TestHandshakeTamperedAuthRejected(t *testing.T) {
	a, b := newPipe()
	idA, idB := newIdentity(t), newIdentity(t)
	// Corrupt the responder's auth signature in flight (2nd message responder sends).
	b.tamper = func(idx int, raw []byte) []byte {
		var f authFrame
		if json.Unmarshal(raw, &f) == nil && f.T == frameAuth {
			sig, _ := wirecrypto.DecB64url(f.Sig)
			sig[0] ^= 0xFF
			f.Sig = wirecrypto.EncB64url(sig)
			out, _ := wirecrypto.MarshalNoHTMLEscape(f)
			return out
		}
		return raw
	}
	ia, _ := run(a, b, idA, idB, nil, nil)
	if ia.err != errBadSig {
		t.Fatalf("expected errBadSig from tampered auth, got %v", ia.err)
	}
}
