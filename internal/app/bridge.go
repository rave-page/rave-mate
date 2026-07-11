package app

import (
	"context"
	"strings"
	"sync"

	"rave.page/mate/internal/authz"
	"rave.page/mate/internal/bridge"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/studio"
)

// bridgeTunnel binds the three layers the account bridge sits between:
//
//	bridge.Manager  finds the other device and hands us a raw relay Conn
//	peerlink        makes that Conn mutually authenticated (Ed25519), CONFIDENTIAL (AEAD), and
//	                authorized (the TOTP/token gate) - identical to a LAN link from here on
//	peerlink Link   ← mate↔mate: every existing data channel works unchanged (remote control,
//	                  the RemoteUI Library mirror)
//	studio channel  ← web Local Studio: the byte-exact studio protocol, inside the same tunnel
//
// Neither protocol is forked for the WAN. The bridge is a transport, nothing more.
type bridgeTunnel struct {
	peers  *peerlink.Manager
	studio *studio.Server
	log    *logbus.Bus
	cfg    func() config.AccountBridgeFeature
}

// ServePeer joins a bridge Conn to the peer link. AdoptConn runs the AKE, the AEAD upgrade and
// the access gate, then registers the connection exactly as a LAN dial would.
func (t *bridgeTunnel) ServePeer(_ context.Context, conn *bridge.Conn, initiator bool, peerName string) {
	t.peers.AdoptConn(conn, initiator, peerName, "rave.page bridge")
}

// ServeStudio serves the Local Studio channel to a remote browser over the bridge.
//
// The SAME secure tunnel runs first (Authenticate: Ed25519 AKE → AEAD → the TOTP/token gate),
// and only then does the studio protocol start inside it. That ordering is load-bearing:
// studio's client-auth carries a raw bearer token, which must never cross the relay in the
// clear. Everything after the upgrade is ciphertext to rave.page.
func (t *bridgeTunnel) ServeStudio(ctx context.Context, conn *bridge.Conn) {
	if _, err := t.peers.Authenticate(ctx, conn, false, "", "rave.page bridge"); err != nil {
		return // peerlink logged + closed
	}
	// Origin is advisory over the bridge (there is no HTTP Origin header). The real guarantees
	// are the tunnel above and studio's own mutual /auth/me identity match below.
	t.studio.ServeConn(ctx, bridge.StudioConn{Conn: conn}, "https://rave.page", studio.TransportBridge)
}

// StudioEnabled gates inbound studio dials on the per-instance feature toggle.
func (t *bridgeTunnel) StudioEnabled() bool { return t.cfg().Enabled && t.cfg().LocalStudio }

// bridgeCaps advertises what this instance serves, so the far end knows what it can ask for.
func bridgeCaps(cfgFn func() config.AccountBridgeFeature) func() []string {
	return func() []string {
		caps := []string{bridge.CapPeerlink}
		if cfgFn().LocalStudio {
			caps = append(caps, bridge.CapLocalStudio)
		}
		return caps
	}
}

// codePrompt is the UI seam for "this instance wants a TOTP code for a peer we've never paired
// with". The frontend registers a prompt; until it does (headless/service mode) we have no way
// to ask a human, so the dial fails closed rather than hanging.
type codePrompt struct {
	mu sync.Mutex
	fn authz.CredentialFunc
}

func (p *codePrompt) set(fn authz.CredentialFunc) { p.mu.Lock(); p.fn = fn; p.mu.Unlock() }

func (p *codePrompt) ask(peerID string) string {
	p.mu.Lock()
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return "" // headless: no human to ask → abort the pairing
	}
	return fn(peerID)
}

// instanceLabel is how this box describes itself in a peer's trusted-session list.
func instanceLabel(cfg config.Config) string {
	n := strings.TrimSpace(cfg.Features.Peers.Nickname)
	if n == "" {
		n = defaultNodeNickname()
	}
	return "rave-mate on " + n
}
