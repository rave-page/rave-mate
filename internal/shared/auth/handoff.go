package auth

// Authenticated co-located token handoff. The receiver (rave-mate) writes a 32-byte secret
// to an owner-only (0o600) file; the sender (rave-app) reads it and AES-256-GCM-encrypts the
// token bundle under it before pushing over the loopback control socket. Loopback TCP is
// cross-user accessible, so a port-squatting process from ANOTHER user could otherwise harvest
// the token; without the owner-only secret it can't decrypt. (A same-user attacker can already
// read the sealed token store, so that case is out of scope.)

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// HandoffSecretFile is the receiver-written shared-secret filename (under its config dir).
const HandoffSecretFile = "ctl.secret"

// NewHandoffSecret returns 32 random bytes (an AES-256 key) for a fresh handoff secret.
func NewHandoffSecret() ([]byte, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// SealHandoff AES-256-GCM-encrypts plaintext under secret; returns base64(nonce‖ciphertext).
func SealHandoff(secret, plaintext []byte) (string, error) {
	a, err := handoffAEAD(secret)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := a.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// OpenHandoff reverses SealHandoff. Fails (no plaintext) if secret is wrong or blob tampered.
func OpenHandoff(secret []byte, blob string) ([]byte, error) {
	a, err := handoffAEAD(secret)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil {
		return nil, err
	}
	if len(raw) < a.NonceSize() {
		return nil, errors.New("handoff blob too short")
	}
	nonce, ct := raw[:a.NonceSize()], raw[a.NonceSize():]
	return a.Open(nil, nonce, ct, nil)
}

func handoffAEAD(secret []byte) (cipher.AEAD, error) {
	if len(secret) != 32 {
		return nil, errors.New("handoff secret must be 32 bytes")
	}
	blk, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}
