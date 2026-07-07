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
}

// authFrame proves possession of the long-term identity key over the transcript.
type authFrame struct {
	T   string `json:"t"`
	Sig string `json:"sig"` // b64url Ed25519 signature over the transcript
}
