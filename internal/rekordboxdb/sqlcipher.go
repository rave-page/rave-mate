// Package rekordboxdb reads a Rekordbox 6/7 live library (master.db). The DB is SQLCipher-4
// encrypted; this package decrypts it (stdlib crypto only) to a temporary plaintext SQLite
// image, then reads it with the pure-Go modernc sqlite driver into the normalized musiclib
// model - tracks + playlists + play history (with per-track timestamps). All local.
package rekordboxdb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// SQLCipher-4 defaults (Rekordbox uses the stock v4 profile).
const (
	scPageSize = 4096
	scKDFIter  = 256000
	scSaltSize = 16
	scIVSize   = 16
	scHMACSize = 64                    // HMAC-SHA512
	scReserve  = scIVSize + scHMACSize // 80, multiple of the AES block
	scKeySize  = 32                    // AES-256
	scRawHexLn = scKeySize * 2         // 64: hex chars for a raw 32-byte key
)

// DefaultRekordboxKey is the widely-documented Rekordbox 6 master.db SQLCipher passphrase.
// Newer versions may differ; override via RAVE_REKORDBOX_KEY.
const DefaultRekordboxKey = "402fd482c38817c35ffa8ffb8c7d93143b749e7d315df7a81732a1ff43608497"

var sqliteMagic = []byte("SQLite format 3\x00")

// keyMode is one candidate key derivation (AES key + HMAC key) for a SQLCipher image.
type keyMode struct {
	name string // "passphrase" | "raw-key" (diagnostics only)
	enc  []byte // AES-256 key
	mac  []byte // HMAC-SHA512 key
}

// decodeRawKey decodes a SQLCipher raw key (exactly 64 hex chars) to its 32 raw bytes.
func decodeRawKey(s string) ([]byte, bool) {
	if len(s) != scRawHexLn {
		return nil, false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != scKeySize {
		return nil, false
	}
	return b, true
}

// deriveModes returns the key derivations to try against salt, in order. Passphrase mode is
// primary: PBKDF2-HMAC-SHA512(passphrase, salt, 256000) → 32-byte AES key. Raw-key mode is a
// fallback appended only when passphrase is exactly 64 hex chars (SQLCipher's PRAGMA
// key="x'…'": the value IS the 32-byte AES key, no outer PBKDF2). Both derive the HMAC key
// the same way - the fast 2-iter KDF over the AES key bytes + the (salt XOR 0x3a) HMAC salt.
func deriveModes(passphrase string, salt []byte) ([]keyMode, error) {
	hmacSalt := make([]byte, scSaltSize)
	for i := range salt {
		hmacSalt[i] = salt[i] ^ 0x3a
	}
	macFor := func(enc []byte) ([]byte, error) {
		// SQLCipher derives the HMAC key at the CIPHER key length (32), not the digest length.
		return pbkdf2.Key(sha512.New, string(enc), hmacSalt, 2, scKeySize)
	}

	encKey, err := pbkdf2.Key(sha512.New, passphrase, salt, scKDFIter, scKeySize)
	if err != nil {
		return nil, err
	}
	macKey, err := macFor(encKey)
	if err != nil {
		return nil, err
	}
	modes := []keyMode{{name: "passphrase", enc: encKey, mac: macKey}}

	if raw, ok := decodeRawKey(passphrase); ok {
		rawMAC, err := macFor(raw)
		if err != nil {
			return nil, err
		}
		modes = append(modes, keyMode{name: "raw-key", enc: raw, mac: rawMAC})
	}
	return modes, nil
}

// decryptSQLCipher turns a SQLCipher-4 image into a standard plaintext SQLite image. Tries
// passphrase mode first; if page-1 HMAC auth fails and passphrase is a 64-hex raw key, retries
// in raw-key mode. Page-1 HMAC failure on every mode ⇒ wrong key / unsupported cipher profile.
func decryptSQLCipher(data []byte, passphrase string) ([]byte, error) {
	if len(data) < scPageSize {
		return nil, fmt.Errorf("rekordboxdb: file too small (%d bytes)", len(data))
	}
	modes, err := deriveModes(passphrase, data[:scSaltSize])
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, m := range modes {
		out, err := decryptPages(data, m.enc, m.mac)
		if err == nil {
			return out, nil
		}
		lastErr = err // keep trying the next mode (raw-key fallback)
	}
	return nil, lastErr
}

// decryptPages runs the SQLCipher-4 page auth+decrypt loop with the given AES + HMAC keys.
// Page-1 HMAC mismatch ⇒ these keys are wrong (caller may retry another key mode).
func decryptPages(data, encKey, macKey []byte) ([]byte, error) {
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	nPages := len(data) / scPageSize
	out := make([]byte, nPages*scPageSize)
	ctEnd := scPageSize - scReserve
	for p := 0; p < nPages; p++ {
		pg := data[p*scPageSize : (p+1)*scPageSize]
		dst := out[p*scPageSize : (p+1)*scPageSize]
		if isAllZero(pg) {
			continue // never-written page stays zero (SQLCipher leaves these plaintext-zero)
		}
		encStart := 0
		if p == 0 {
			encStart = scSaltSize // page 1's first 16 bytes are the (plaintext) salt
		}
		iv := pg[ctEnd : ctEnd+scIVSize]
		storedMAC := pg[ctEnd+scIVSize : ctEnd+scIVSize+scHMACSize]

		mac := hmac.New(sha512.New, macKey)
		mac.Write(pg[encStart : ctEnd+scIVSize]) // ciphertext + IV
		var pno [4]byte
		binary.LittleEndian.PutUint32(pno[:], uint32(p+1))
		mac.Write(pno[:])
		if !hmac.Equal(mac.Sum(nil), storedMAC) {
			if p == 0 {
				return nil, fmt.Errorf("rekordboxdb: page-1 HMAC mismatch - wrong key or unsupported Rekordbox version (set RAVE_REKORDBOX_KEY)")
			}
			return nil, fmt.Errorf("rekordboxdb: HMAC mismatch on page %d", p+1)
		}

		ct := pg[encStart:ctEnd]
		dec := make([]byte, len(ct))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(dec, ct)
		if p == 0 {
			copy(dst[:scSaltSize], sqliteMagic)
			copy(dst[scSaltSize:ctEnd], dec)
			dst[20] = scReserve // SQLite header: reserved bytes per page
		} else {
			copy(dst[:ctEnd], dec)
		}
		// reserve area [ctEnd:scPageSize] stays zero
	}
	return out, nil
}

// encryptSQLCipher is the inverse of decryptSQLCipher: turns a plaintext SQLite image (with
// reserved-bytes-per-page == scReserve, as decryptSQLCipher produces) back into a SQLCipher-4
// image under passphrase, with a fresh random salt. Page 1's first 16 bytes become the salt
// (the SQLite magic header is dropped, exactly as SQLCipher stores it). All-zero plaintext pages
// pass through as zero (symmetric with decrypt). len(plain) must be a multiple of scPageSize.
func encryptSQLCipher(plain []byte, passphrase string) ([]byte, error) {
	if len(plain) == 0 || len(plain)%scPageSize != 0 {
		return nil, fmt.Errorf("rekordboxdb: plaintext not a page multiple (%d bytes)", len(plain))
	}
	salt := make([]byte, scSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	modes, err := deriveModes(passphrase, salt)
	if err != nil {
		return nil, err
	}
	// Encrypt in passphrase mode (modes[0]); decrypt is what tries the raw-key fallback.
	return encryptPages(plain, modes[0].enc, modes[0].mac, salt)
}

// encryptPages runs the SQLCipher-4 page encrypt+MAC loop with the given AES + HMAC keys,
// writing salt into page 1's first 16 bytes. Symmetric with decryptPages (test-shared).
func encryptPages(plain, encKey, macKey, salt []byte) ([]byte, error) {
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	nPages := len(plain) / scPageSize
	out := make([]byte, len(plain))
	ctEnd := scPageSize - scReserve
	for p := 0; p < nPages; p++ {
		src := plain[p*scPageSize : (p+1)*scPageSize]
		if isAllZero(src) {
			continue // never-written page stays zero (symmetric with decrypt)
		}
		dst := out[p*scPageSize : (p+1)*scPageSize]
		encStart := 0
		if p == 0 {
			encStart = scSaltSize
			copy(dst[:scSaltSize], salt) // page 1: salt replaces the plaintext magic
		}
		iv := make([]byte, scIVSize)
		if _, err := rand.Read(iv); err != nil {
			return nil, err
		}
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(dst[encStart:ctEnd], src[encStart:ctEnd])
		copy(dst[ctEnd:ctEnd+scIVSize], iv)
		mac := hmac.New(sha512.New, macKey)
		mac.Write(dst[encStart : ctEnd+scIVSize]) // ciphertext + IV
		var pno [4]byte
		binary.LittleEndian.PutUint32(pno[:], uint32(p+1))
		mac.Write(pno[:])
		copy(dst[ctEnd+scIVSize:ctEnd+scIVSize+scHMACSize], mac.Sum(nil))
	}
	return out, nil
}

func isAllZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
