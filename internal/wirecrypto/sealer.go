package wirecrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
)

// sealer.go lifts the AES-256-GCM counter-nonce pattern the transports share (bridge, medialink,
// filexfer) into one primitive. Every one of them keyed per-direction from an HKDF master, sealed
// with a 96-bit big-endian counter nonce (4 zero bytes || uint64) that is never transmitted -
// both ends step in lockstep on an ordered stream, so the nonce never repeats under a key that is
// itself fresh per connection (the one rule GCM cannot survive breaking). The peerlink LAN
// transport is the fourth adopter; this exists so it did not become a fourth copy.

// Sealer is one direction of an AES-256-GCM stream with an internal never-transmitted counter
// nonce. NOT safe for concurrent use: guard each direction with its own lock (the two directions
// are independent, so a duplex link runs its send and recv Sealers in parallel unguarded).
type Sealer struct {
	aead cipher.AEAD
	ctr  uint64
}

// NewSealer builds a single-direction Sealer over a 32-byte (AES-256) key.
func NewSealer(key []byte) (*Sealer, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal appends the sealed frame to dst and returns the grown slice, advancing the counter. Pass
// dst with any header bytes (e.g. a length prefix) already reserved to keep the write zero-copy.
func (s *Sealer) Seal(dst, plaintext []byte) []byte {
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], s.ctr)
	s.ctr++
	return s.aead.Seal(dst, nonce[:], plaintext, nil)
}

// Open appends the opened plaintext to dst and returns it, advancing the counter only on success.
// On error the direction is dead: a forged or reordered frame desyncs the counter unrecoverably.
func (s *Sealer) Open(dst, ciphertext []byte) ([]byte, error) {
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], s.ctr)
	pt, err := s.aead.Open(dst, nonce[:], ciphertext, nil)
	if err != nil {
		return nil, err
	}
	s.ctr++
	return pt, nil
}

// Overhead is the per-frame ciphertext expansion (the GCM tag).
func (s *Sealer) Overhead() int { return s.aead.Overhead() }

// NewDuplexSealer derives a per-direction Sealer pair from a shared master via HKDF-SHA256.
// labelA keys the initiator→responder direction, labelB the reverse; salt may be nil. The dialing
// end (initiator=true) seals under labelA and opens under labelB, the accepting end mirrors, so
// the two ends never seal under the same key. Keys are 256-bit.
func NewDuplexSealer(master, salt []byte, initiator bool, labelA, labelB string) (send, recv *Sealer, err error) {
	ka, err := hkdf.Key(sha256.New, master, salt, labelA, 32)
	if err != nil {
		return nil, nil, err
	}
	kb, err := hkdf.Key(sha256.New, master, salt, labelB, 32)
	if err != nil {
		return nil, nil, err
	}
	sk, rk := ka, kb
	if !initiator {
		sk, rk = kb, ka
	}
	if send, err = NewSealer(sk); err != nil {
		return nil, nil, err
	}
	if recv, err = NewSealer(rk); err != nil {
		return nil, nil, err
	}
	return send, recv, nil
}
