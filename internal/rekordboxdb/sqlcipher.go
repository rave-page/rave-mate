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
)

// DefaultRekordboxKey is the widely-documented Rekordbox 6 master.db SQLCipher passphrase.
// Newer versions may differ; override via RAVE_REKORDBOX_KEY.
const DefaultRekordboxKey = "402fd482c38817c35ffa8ffb8c7d93143b749e7d315df7a81732a1ff43608497"

var sqliteMagic = []byte("SQLite format 3\x00")

// decryptSQLCipher turns a SQLCipher-4 image into a standard plaintext SQLite image. Page-1
// HMAC failure ⇒ wrong key / unsupported cipher profile.
func decryptSQLCipher(data []byte, passphrase string) ([]byte, error) {
	if len(data) < scPageSize {
		return nil, fmt.Errorf("rekordboxdb: file too small (%d bytes)", len(data))
	}
	salt := data[:scSaltSize]
	encKey, err := pbkdf2.Key(sha512.New, passphrase, salt, scKDFIter, scKeySize)
	if err != nil {
		return nil, err
	}
	hmacSalt := make([]byte, scSaltSize)
	for i := range salt {
		hmacSalt[i] = salt[i] ^ 0x3a
	}
	// SQLCipher derives the HMAC key at the CIPHER key length (32), not the digest length (64).
	hmacKey, err := pbkdf2.Key(sha512.New, string(encKey), hmacSalt, 2, scKeySize)
	if err != nil {
		return nil, err
	}
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

		mac := hmac.New(sha512.New, hmacKey)
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
	encKey, err := pbkdf2.Key(sha512.New, passphrase, salt, scKDFIter, scKeySize)
	if err != nil {
		return nil, err
	}
	hmacSalt := make([]byte, scSaltSize)
	for i := range salt {
		hmacSalt[i] = salt[i] ^ 0x3a
	}
	hmacKey, err := pbkdf2.Key(sha512.New, string(encKey), hmacSalt, 2, scKeySize)
	if err != nil {
		return nil, err
	}
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
		mac := hmac.New(sha512.New, hmacKey)
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
