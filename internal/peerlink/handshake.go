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

	// LAN control-plane encryption negotiation (see gate.go). LocalEncPref/PeerEncPref are the
	// two signed hello preferences; PeerEncPref is "" for an older peer that sent no encPref.
	// LinkEncrypted is set true by upgradeTransport when the LAN AEAD upgrade actually ran.
	LocalEncPref  string
	PeerEncPref   string
	LinkEncrypted bool
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
//
// expectPeerID is the peer node id known a priori (an outbound reconnect to a remembered peer);
// "" when unknown (a seed dial or an inbound connection). resolvePref (may be nil → encOn)
// yields this side's per-peer LAN-plane preference by peer id; the responder learns the peer id
// from the received hello before it signs its own, so its opt-out is applied symmetrically.
func doHandshake(ctx context.Context, c Conn, r role, id *identity.Identity, trust TrustLookup, expectPeerID string, resolvePref func(peerID string) string) (*Result, error) {
	eph, ephJwk, err := wirecrypto.GenerateEcdh()
	if err != nil {
		return nil, err
	}
	nonce := wirecrypto.RandomBytes(32)
	prefOf := func(peerID string) string {
		if resolvePref == nil {
			return encOn
		}
		if resolvePref(peerID) == encOff {
			return encOff
		}
		return encOn
	}
	buildHello := func(pref string) ([]byte, error) {
		return wirecrypto.MarshalNoHTMLEscape(helloFrame{
			T: frameHello, PV: protocolVersion, Role: string(r), EphPubJwk: ephJwk,
			Nonce: wirecrypto.EncB64url(nonce), IDPub: wirecrypto.EncB64url(id.Pub), NodeID: id.NodeID,
			EncPref: pref,
		})
	}

	// Exchange hellos. The initiator sends first (it applies the a-priori peer preference); the
	// responder receives first, so it can look up its own per-peer preference for the now-known
	// peer id before signing its reply. Either way the transcript covers both encPref values.
	var myRaw, peerRaw []byte
	var myPref string
	var ph helloFrame
	if r == roleInitiator {
		myPref = prefOf(expectPeerID)
		if myRaw, err = buildHello(myPref); err != nil {
			return nil, err
		}
		if err = c.Send(ctx, myRaw); err != nil {
			return nil, err
		}
		if peerRaw, err = c.Recv(ctx); err != nil {
			return nil, err
		}
		if json.Unmarshal(peerRaw, &ph) != nil || ph.T != frameHello || ph.PV != protocolVersion {
			return nil, errProtocol
		}
	} else {
		if peerRaw, err = c.Recv(ctx); err != nil {
			return nil, err
		}
		if json.Unmarshal(peerRaw, &ph) != nil || ph.T != frameHello || ph.PV != protocolVersion {
			return nil, errProtocol
		}
		myPref = prefOf(ph.NodeID) // claimed id only picks OUR pref; a spoof still fails the sig below
		if myRaw, err = buildHello(myPref); err != nil {
			return nil, err
		}
		if err = c.Send(ctx, myRaw); err != nil {
			return nil, err
		}
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
		LocalEncPref: myPref, PeerEncPref: ph.EncPref,
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
