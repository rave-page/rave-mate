package peerlink

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"

	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/wirecrypto"
)

// Conn is the message transport the handshake runs over (a websocket in production, an
// in-memory pipe in tests). Send/Recv exchange whole frames.
type Conn interface {
	Send(ctx context.Context, b []byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close()
}

// TrustLookup returns a peer's stored identity key (and true) if it's already trusted.
type TrustLookup func(nodeID string) (ed25519.PublicKey, bool)

// Result is the outcome of a completed authenticated key exchange.
type Result struct {
	Role            role
	PeerNodeID      string
	PeerIdentityPub ed25519.PublicKey
	SessionKey      []byte
	BindKey         []byte // post-handshake frame MAC key
	Transcript      []byte
	SAS             string
	Trusted         bool // peer's key already matched a stored trusted peer → no SAS needed
}

var (
	// ErrKeyChanged means a known node id presented a different identity key - possible
	// impersonation; the caller must refuse and require a fresh pairing.
	ErrKeyChanged = errors.New("peerlink: peer identity key changed for a known node")
	errProtocol   = errors.New("peerlink: protocol error")
	errBadSig     = errors.New("peerlink: peer signature verification failed")
)

// doHandshake runs the AKE as the given role over c, authenticating with id and consulting
// trust (may be nil). On success it returns a Result; for an untrusted peer SAS must still
// be confirmed by the user before the link is used (see Manager).
func doHandshake(ctx context.Context, c Conn, r role, id *identity.Identity, trust TrustLookup) (*Result, error) {
	eph, ephJwk, err := wirecrypto.GenerateEcdh()
	if err != nil {
		return nil, err
	}
	nonce := wirecrypto.RandomBytes(32)
	hello := helloFrame{
		T: frameHello, PV: protocolVersion, Role: string(r), EphPubJwk: ephJwk,
		Nonce: wirecrypto.EncB64url(nonce), IDPub: wirecrypto.EncB64url(id.Pub), NodeID: id.NodeID,
	}
	myRaw, err := wirecrypto.MarshalNoHTMLEscape(hello)
	if err != nil {
		return nil, err
	}

	// Exchange hellos (initiator sends first to avoid a deadlock).
	peerRaw, err := exchange(ctx, c, r, myRaw)
	if err != nil {
		return nil, err
	}
	var ph helloFrame
	if json.Unmarshal(peerRaw, &ph) != nil || ph.T != frameHello || ph.PV != protocolVersion {
		return nil, errProtocol
	}
	if !oppositeRole(r, ph.Role) {
		return nil, fmt.Errorf("%w: role conflict", errProtocol)
	}
	peerIDPub, err := wirecrypto.DecB64url(ph.IDPub)
	if err != nil || len(peerIDPub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: bad id pub", errProtocol)
	}
	if identity.NodeIDFromPub(peerIDPub) != ph.NodeID {
		return nil, fmt.Errorf("%w: node id does not match id key", errProtocol)
	}
	peerNonce, err := wirecrypto.DecB64url(ph.Nonce)
	if err != nil || len(peerNonce) != 32 {
		return nil, fmt.Errorf("%w: bad nonce", errProtocol)
	}
	peerEph, err := wirecrypto.PublicKeyFromJwk(ph.EphPubJwk)
	if err != nil {
		return nil, fmt.Errorf("%w: bad eph key", errProtocol)
	}

	// Shared secret + role-ordered transcript/salt (identical on both ends).
	z, err := wirecrypto.DeriveSharedSecret(eph, peerEph)
	if err != nil {
		return nil, err
	}
	initRaw, respRaw, nonceInit, nonceResp := myRaw, peerRaw, nonce, peerNonce
	if r == roleResponder {
		initRaw, respRaw, nonceInit, nonceResp = peerRaw, myRaw, peerNonce, nonce
	}
	transcript, err := transcriptBytes(initRaw, respRaw)
	if err != nil {
		return nil, err
	}
	salt := append(append([]byte(nil), nonceInit...), nonceResp...)
	sessionKey, err := wirecrypto.HkdfSha256(z, salt, append(append([]byte(nil), transcript...), keyInfo...), 32)
	if err != nil {
		return nil, err
	}

	// Authenticate: sign the transcript, exchange + verify.
	auth := authFrame{T: frameAuth, Sig: wirecrypto.EncB64url(id.Sign(transcript))}
	myAuthRaw, _ := wirecrypto.MarshalNoHTMLEscape(auth)
	peerAuthRaw, err := exchange(ctx, c, r, myAuthRaw)
	if err != nil {
		return nil, err
	}
	var pa authFrame
	if json.Unmarshal(peerAuthRaw, &pa) != nil || pa.T != frameAuth {
		return nil, errProtocol
	}
	peerSig, err := wirecrypto.DecB64url(pa.Sig)
	if err != nil || !identity.VerifyPeer(peerIDPub, transcript, peerSig) {
		return nil, errBadSig
	}

	// Both signatures verified - safe to derive the SAS + frame key now.
	sas, err := DeriveSAS(sessionKey, transcript)
	if err != nil {
		return nil, err
	}
	bindKey, err := wirecrypto.HkdfSha256(sessionKey, transcript, []byte(frameInfo), 32)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Role: r, PeerNodeID: ph.NodeID, PeerIdentityPub: peerIDPub,
		SessionKey: sessionKey, BindKey: bindKey, Transcript: transcript, SAS: sas,
	}
	if trust != nil {
		if stored, ok := trust(ph.NodeID); ok {
			if bytes.Equal(stored, peerIDPub) {
				res.Trusted = true // known + key matches → skip the SAS prompt
			} else {
				return nil, ErrKeyChanged
			}
		}
	}
	return res, nil
}

// exchange sends mine and returns the peer's frame, ordering send/recv by role to avoid a
// mutual-blocking deadlock on a half-duplex transport.
func exchange(ctx context.Context, c Conn, r role, mine []byte) ([]byte, error) {
	if r == roleInitiator {
		if err := c.Send(ctx, mine); err != nil {
			return nil, err
		}
		return c.Recv(ctx)
	}
	peer, err := c.Recv(ctx)
	if err != nil {
		return nil, err
	}
	return peer, c.Send(ctx, mine)
}

func oppositeRole(mine role, theirs string) bool {
	if mine == roleInitiator {
		return theirs == string(roleResponder)
	}
	return theirs == string(roleInitiator)
}

// transcriptBytes canonicalises [initHello, respHello] - byte-identical on both ends
// regardless of who sent first or each side's JSON key order.
func transcriptBytes(initRaw, respRaw []byte) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('[')
	b.Write(initRaw)
	b.WriteByte(',')
	b.Write(respRaw)
	b.WriteByte(']')
	return wirecrypto.CanonicalJSON(b.Bytes())
}
