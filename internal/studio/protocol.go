// Package studio is the Go port of the web↔desktop "Local Studio" WebSocket channel
// (electron/src/main/studio/studioWsServer.ts). Loopback-only control server with a
// relay-ready handshake: ECDH P-256 + HKDF session key, mutual /auth/me identity match
// (same rave.page user on both ends), per-frame HMAC bound to the token pair, monotonic
// seq for replay/reorder protection. Same security mechanisms as the Electron server.
package studio

const protocolVersion = 1

const (
	hkdfInfo     = "rave-studio-v1"
	heartbeatMS  = 15_000
	deadMS       = 30_000
	maxPayload   = 8 * 1024 * 1024
	appVersion   = "0.1.0" // rave-mate studio server version (server-hello.appVersion)
	defaultExpHr = 12      // session lifetime when neither token carries exp
)

// portRange mirrors the Electron server's fixed loopback range.
var portRange = []int{47615, 47616, 47617, 47618, 47619}

// WS application close codes (StudioClose in protocol.ts).
const (
	closeTokenExpired    = 4001
	closeLoggedOut       = 4002
	closeSubMismatch     = 4003
	closeHandshakeFailed = 4004
	closeProtocolError   = 4005
	closeReplaced        = 4006
	closeGoingAway       = 4007
)

// handshake-fail codes.
type handshakeFailCode string

const (
	failVersion      handshakeFailCode = "version"
	failOrigin       handshakeFailCode = "origin"
	failTokenInvalid handshakeFailCode = "token-invalid"
	failSubMismatch  handshakeFailCode = "sub-mismatch"
	failExpired      handshakeFailCode = "expired"
	failReplay       handshakeFailCode = "replay"
	failMAC          handshakeFailCode = "mac"
	failInternal     handshakeFailCode = "internal"
)

// Studio error codes (terminal res errors).
type errorCode string

const (
	errBadRequest    errorCode = "bad-request"
	errUnknownMethod errorCode = "unknown-method"
	errUnauthorized  errorCode = "unauthorized"
	errNotFound      errorCode = "not-found"
	errFS            errorCode = "fs-error"
	errFFmpeg        errorCode = "ffmpeg-error"
	errCancelled     errorCode = "cancelled"
	errRateLimited   errorCode = "rate-limited"
	errInternal      errorCode = "internal"
)

// ── inbound frames we parse (only the fields we read) ────────────────────────

type clientHello struct {
	T                string `json:"t"`
	ProtocolVersion  int    `json:"protocolVersion"`
	ClientNonce      string `json:"clientNonce"`
	ClientEcdhPubJwk jwk    `json:"clientEcdhPubJwk"`
	ClientInstanceID string `json:"clientInstanceId"`
	Origin           string `json:"origin"`
}

type clientAuth struct {
	T           string `json:"t"`
	AccessToken string `json:"accessToken"`
	Jti         string `json:"jti"`
	AuthTag     string `json:"authTag"`
}

// frameTag peeks just the discriminant + seq/mac of any inbound frame.
type frameTag struct {
	T   string `json:"t"`
	Seq *int64 `json:"seq"`
	Mac string `json:"mac"`
}

// dataReq is a post-handshake request frame. Target, when set, routes the (unary) call to a
// paired remote peer over the peer gateway instead of handling it locally.
type dataReq struct {
	T      string `json:"t"`
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
	Target string `json:"target"`
	Seq    int64  `json:"seq"`
}
