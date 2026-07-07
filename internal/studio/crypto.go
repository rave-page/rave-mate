package studio

// Crypto primitives live in internal/wirecrypto (shared with internal/peerlink). These
// unexported aliases keep the studio call sites + byte-exact web handshake test unchanged.

import "rave.page/mate/internal/wirecrypto"

type jwk = wirecrypto.Jwk

var (
	randomBytes        = wirecrypto.RandomBytes
	generateEcdh       = wirecrypto.GenerateEcdh
	publicKeyFromJwk   = wirecrypto.PublicKeyFromJwk
	deriveSharedSecret = wirecrypto.DeriveSharedSecret
	hkdfSha256         = wirecrypto.HkdfSha256
	hmacSha256         = wirecrypto.HmacSha256
)
