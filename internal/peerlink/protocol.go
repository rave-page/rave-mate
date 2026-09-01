// Package peerlink is the secure peer-to-peer link between two rave-mate instances on a
// LAN. It does NOT use the rave.page API (that's a future linking path) - peers authenticate
// each other directly with their long-term Ed25519 node identities (internal/identity) plus
// a one-time SAS-PIN check on first pairing, then remember each other's key for silent
// reconnects. The key exchange reuses internal/wirecrypto (ECDH-P256 + HKDF + HMAC +
// canonical-JSON), the same primitives as the studio channel.
//
// Handshake (authenticated key exchange, MITM-safe):
//  1. peer-hello (both ways): ephemeral ECDH pub + 32B nonce + long-term Ed25519 id pub.
//  2. sessionKey = HKDF(ECDH(eph), salt = nonceInit|nonceResp, info = transcript|"v1").
//  3. peer-auth (both ways): Ed25519 signature over the canonical transcript (which
//     contains BOTH id pubs + both eph pubs + both nonces) → binds the ephemeral exchange
//     to the long-term identities; a substituted ECDH key breaks the signature.
//  4. SAS = 6 digits from HKDF(sessionKey, transcript) - shown on both screens only AFTER
//     both signatures verify. A relay MITM runs two sessions → two SAS → humans reject.
//  5. On a known peer, the signature verifies against the stored key → no SAS. A changed
//     key for a known node id is rejected loudly.
//
// Encryption (default ON): after the AKE the LAN transport upgrades to AES-256-GCM
// (transport.go), so every control frame and data channel is sealed, not just MAC'd. The
// decision is negotiated in the signed hello (encPref) and holds unless BOTH peers opted the
// control plane out - a lone or wire-injected "off" cannot force a downgrade (see gate.go).
// The opt-out granularity is PER-PLANE, not per-channel: there is deliberately no mixed-mode
// framing that would leave MIDI plaintext inside an otherwise-sealed tunnel. AES-GCM on a
// sub-KB control frame costs microseconds; the latency escape hatch that actually matters is
// the A/V plane (medialink), where a 4K route's per-frame budget lives, and that plane has its
// own per-peer opt-out (peers.PlaneMedia).
package peerlink

import "rave.page/mate/internal/wirecrypto"

const (
	protocolVersion = 1

	// HKDF info/label constants - distinct domains so the same sessionKey can't be reused
	// across purposes.
	keyInfo   = "rave-peer-v1"       // session-key derivation
	sasInfo   = "rave-peer-sas-v1"   // short-auth-string derivation
	frameInfo = "rave-peer-frame-v1" // post-handshake frame MAC key
	// mediaSecretInfo derives the medialink data-plane master (MediaSecret) - domain-separated
	// from the session key + frame-MAC key so the media transport never touches the raw secret.
	mediaSecretInfo = "rave-peer-media-v1"
	// fileSecretInfo derives the filexfer data-plane master (FileSecret) - its own domain,
	// so the file transport never shares keys with media or control.
	fileSecretInfo = "rave-peer-file-v1"

	sasDigits = 6

	// encOn/encOff are the control-plane encryption preferences carried (signed, transcript-
	// covered) in the hello. Default ON: a new build always sends one, so a wire attacker who
	// strips the field to force a downgrade breaks the transcript signature. An OLD build sends
	// no encPref at all (absent) → the peer is treated as unable to encrypt the LAN plane.
	encOn  = "on"
	encOff = "off"

	frameHello   = "peer-hello"
	frameAuth    = "peer-auth"
	frameConfirm = "peer-confirm" // user confirmed the SAS matches
	frameReject  = "peer-reject"  // user said the SAS does not match
	framePing    = "peer-ping"
	framePong    = "peer-pong"
	frameData    = "peer-data" // app-level data channel (now-playing, MIDI, control)
)

// Data-channel sub-channels (the "ch" field of a frameData). The payload ("data") is opaque
// JSON the channel's handler owns.
const (
	ChanSession = "session" // now-playing / UnifiedState updates
	ChanMIDI    = "midi"    // bridged MIDI messages
	ChanControl = "control" // remote-control commands (control-the-other-PC)
	ChanBus     = "bus"     // generic pub/sub event bus (twitch, vr, obs.mic, capability ads)
	// ChanRemoteUI streams a headless Library-tab session between paired instances: the
	// controlled side sends its Go-rendered document + eval/patch stream; the controller
	// sends back page input + media-fetch requests (internal/webui remoteui).
	ChanRemoteUI = "remoteui"
)

type role string

const (
	roleInitiator role = "init"
	roleResponder role = "resp"
)

// helloFrame is the first message each side sends.
type helloFrame struct {
	T         string         `json:"t"`
	PV        int            `json:"pv"`
	Role      string         `json:"role"`
	EphPubJwk wirecrypto.Jwk `json:"ephPubJwk"`
	Nonce     string         `json:"nonce"` // b64url 32B
	IDPub     string         `json:"idPub"` // b64url Ed25519 public key
	NodeID    string         `json:"nodeId"`
	// EncPref is this side's LAN-plane encryption preference (encOn/encOff). Signed via the
	// transcript. Absent on the wire = an older peer that predates LAN encryption; see gate.go.
	EncPref string `json:"encPref,omitempty"`
}

// authFrame proves possession of the long-term identity key over the transcript.
type authFrame struct {
	T   string `json:"t"`
	Sig string `json:"sig"` // b64url Ed25519 signature over the transcript
}
