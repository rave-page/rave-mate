// Package wirecrypto holds the stdlib crypto + canonical-JSON primitives shared by the
// rave.page-JWT studio channel (internal/studio) and the LAN peer link (internal/peerlink).
// Everything here is byte-compatible with the web client's TS encoding.ts/protocol - the
// canonical-JSON + MAC inputs are load-bearing across both ends; do not let them drift.
package wirecrypto

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// Jwk is the structural P-256 public JWK exchanged in handshakes (mirrors the TS Jwk;
// only kty/crv/x/y are produced/consumed - extra fields a peer sends are ignored).
type Jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// RandomBytes returns n cryptographically-random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// GenerateEcdh makes a P-256 keypair and returns the private key + its public JWK.
func GenerateEcdh() (*ecdh.PrivateKey, Jwk, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, Jwk{}, err
	}
	pub := priv.PublicKey().Bytes() // 0x04 || X(32) || Y(32)
	if len(pub) != 65 || pub[0] != 0x04 {
		return nil, Jwk{}, fmt.Errorf("unexpected P-256 public encoding")
	}
	return priv, Jwk{Kty: "EC", Crv: "P-256", X: EncB64url(pub[1:33]), Y: EncB64url(pub[33:65])}, nil
}

// PublicKeyFromJwk reconstructs a P-256 public key from a peer JWK (x/y only).
func PublicKeyFromJwk(j Jwk) (*ecdh.PublicKey, error) {
	if j.Crv != "P-256" {
		return nil, fmt.Errorf("unsupported curve %q", j.Crv)
	}
	x, err := DecB64url(j.X)
	if err != nil || len(x) != 32 {
		return nil, fmt.Errorf("bad jwk.x")
	}
	y, err := DecB64url(j.Y)
	if err != nil || len(y) != 32 {
		return nil, fmt.Errorf("bad jwk.y")
	}
	uncompressed := append([]byte{0x04}, append(x, y...)...)
	return ecdh.P256().NewPublicKey(uncompressed)
}

// DeriveSharedSecret returns the ECDH-Z (the 32-byte X coordinate of the shared point) -
// byte-identical to Node crypto.diffieHellman / WebCrypto deriveBits(256).
func DeriveSharedSecret(priv *ecdh.PrivateKey, peer *ecdh.PublicKey) ([]byte, error) {
	return priv.ECDH(peer)
}

// HkdfSha256 derives length bytes from ikm via HKDF-SHA256.
func HkdfSha256(ikm, salt, info []byte, length int) ([]byte, error) {
	return hkdf.Key(sha256.New, ikm, salt, string(info), length)
}

// HmacSha256 returns HMAC-SHA256(key, data).
func HmacSha256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}
