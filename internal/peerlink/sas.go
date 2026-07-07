package peerlink

import (
	"encoding/binary"
	"fmt"

	"rave.page/mate/internal/wirecrypto"
)

// DeriveSAS produces the human-comparable short auth string: sasDigits decimal digits from
// HKDF(sessionKey, salt = transcript, info = sasInfo). Because the transcript carries both
// ephemeral pubs, both identity pubs, and both nonces, two peers that completed the SAME
// authenticated exchange derive the SAME code; a relay MITM (two distinct sessions) derives
// two different codes, so the two humans comparing them will see a mismatch and abort.
func DeriveSAS(sessionKey, transcript []byte) (string, error) {
	out, err := wirecrypto.HkdfSha256(sessionKey, transcript, []byte(sasInfo), 4)
	if err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(out) % pow10(sasDigits)
	return fmt.Sprintf("%0*d", sasDigits, n), nil
}

func pow10(d int) uint32 {
	p := uint32(1)
	for i := 0; i < d; i++ {
		p *= 10
	}
	return p
}
