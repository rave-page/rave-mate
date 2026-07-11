package peerlink

import (
	"context"

	"rave.page/mate/internal/authz"
	"rave.page/mate/internal/wirecrypto"
)

// This file is the transport seam: everything peerlink needs in order to run over something
// that is NOT the LAN ws:// listener - today the rave.page account bridge, tomorrow a direct
// ip:port dial. The AKE, the link, the data channels and every consumer above them (remote
// control, the RemoteUI Library mirror) are untouched.

// linkSecretInfo derives the transport-AEAD master from the handshake. Its own HKDF domain, so
// the transport key is never the session key, the frame-MAC key, or the media/file masters.
const linkSecretInfo = "rave-peer-link-v1"

// Upgrader is an OPTIONAL Conn capability: a transport that can switch to authenticated
// encryption once the handshake has established a shared secret.
//
// The LAN ws:// transport does NOT implement it - there, frames are plaintext + HMAC, a
// documented tradeoff on a trusted local network (see link.go). A transport that crosses a
// third party MUST implement it: peerlink's data plane would otherwise hand the relay operator
// every remote-control command and the entire RemoteUI stream in the clear.
//
// initiator tells the transport which direction key is its own, so the two ends never seal
// under the same key.
type Upgrader interface {
	Upgrade(master []byte, initiator bool) error
}

// Gated is an OPTIONAL Conn capability marking a transport where the SAS compare is not
// available - there is no second human looking at the other screen. Such a transport
// authorizes an unknown peer through the Authorizer instead (TOTP → pinned identity → token).
type Gated interface {
	// GateTransport names the medium, for the trusted-session list in the UI.
	GateTransport() authz.Transport
}

// Authorizer decides whether an untrusted peer on a gated transport may connect. Satisfied by
// *authz.Gate. Nil → untrusted peers on gated transports are REFUSED (fail closed).
type Authorizer interface {
	// Verify runs on the side BEING REACHED: challenge the caller for a credential.
	Verify(ctx context.Context, peerID string, tr authz.Transport, ch authz.Channel) (authz.Grant, error)
	// Prove runs on the side CALLING OUT: present a stored token, or a code from the user.
	Prove(ctx context.Context, ch authz.Channel, label string, codeFn authz.CredentialFunc) error
}

// SetAuthorizer installs the gate used for untrusted peers on gated transports. label describes
// THIS instance to the peer's trusted-session list; codeFn is asked for a TOTP code when we dial
// a peer we hold no token for (the UI prompts; "" aborts).
func (m *Manager) SetAuthorizer(a Authorizer, label string, codeFn authz.CredentialFunc) {
	m.mu.Lock()
	m.authz, m.authzLabel, m.authzCode = a, label, codeFn
	m.mu.Unlock()
}

// AdoptConn runs the peerlink handshake over an EXTERNALLY supplied transport and, on success,
// registers the connection exactly as a LAN dial would - so every data channel, the remote
// control endpoint and the RemoteUI mirror work over it unchanged.
//
// initiator picks the AKE role. addr is a display string for the UI ("rave.page bridge").
// Blocking: call it on your own goroutine.
func (m *Manager) AdoptConn(conn Conn, initiator bool, nickname, addr string) {
	r := roleResponder
	if initiator {
		r = roleInitiator
	}
	m.runHandshake(conn, r, nickname, addr)
}

// Authenticate establishes the SECURE TUNNEL over conn - mutual Ed25519 AKE, AEAD upgrade, and
// the access gate for an untrusted peer - and returns the peer's verified node id WITHOUT
// starting a peerlink Link.
//
// It exists so a DIFFERENT protocol can ride inside the same authenticated, encrypted tunnel:
// the Local Studio channel over the account bridge does exactly this, then hands the conn to
// studio.ServeConn. Everything the tunnel guarantees (identity pinning, confidentiality from
// the relay, TOTP/token authorization) applies to it unchanged.
//
// Blocking: call it on your own goroutine.
func (m *Manager) Authenticate(ctx context.Context, conn Conn, initiator bool, nickname, addr string) (string, error) {
	r := roleResponder
	if initiator {
		r = roleInitiator
	}
	res, err := m.secureTunnel(ctx, conn, r, nickname, addr)
	if err != nil {
		return "", err
	}
	return res.PeerNodeID, nil
}

// upgradeTransport switches a capable transport to AEAD, keyed from the completed handshake.
// No-op on transports that don't implement Upgrader (the LAN listener).
func upgradeTransport(conn Conn, res *Result) error {
	up, ok := conn.(Upgrader)
	if !ok {
		return nil
	}
	master, err := wirecrypto.HkdfSha256(res.SessionKey, res.Transcript, []byte(linkSecretInfo), 32)
	if err != nil {
		return err
	}
	return up.Upgrade(master, res.Role == roleInitiator)
}

// gateTransport reports the transport's gate medium, and whether it is gated at all.
func gateTransport(conn Conn) (authz.Transport, bool) {
	g, ok := conn.(Gated)
	if !ok {
		return "", false
	}
	return g.GateTransport(), true
}

// authorize runs the gate over an established, ENCRYPTED link for a peer we don't yet trust.
// The reached side (responder) challenges; the caller (initiator) proves. On success both ends
// pin each other's Ed25519 identity - the SAS-equivalent for a link with no human at the far
// end.
//
// PRECONDITION: the conn has already been Upgrade()d. The credential travels inside it; on a
// cleartext transport it would go straight to the relay operator.
func (m *Manager) authorize(ctx context.Context, conn Conn, res *Result, tr authz.Transport) error {
	m.mu.Lock()
	a, label, codeFn := m.authz, m.authzLabel, m.authzCode
	m.mu.Unlock()
	if a == nil {
		return errNoAuthorizer
	}
	// authz.Channel is Send/Recv - Conn satisfies it.
	if res.Role == roleResponder {
		_, err := a.Verify(ctx, res.PeerNodeID, tr, conn)
		return err
	}
	return a.Prove(ctx, conn, label, codeFn)
}
