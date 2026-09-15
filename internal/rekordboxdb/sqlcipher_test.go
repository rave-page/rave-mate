package rekordboxdb

import (
	"bytes"
	"crypto/rand"
	"os"
	"testing"
)

// mustEncrypt wraps the production encryptSQLCipher for tests (fatal on error).
func mustEncrypt(t *testing.T, plain []byte, passphrase string) []byte {
	t.Helper()
	enc, err := encryptSQLCipher(plain, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

// mustEncryptRawKey encrypts plain in SQLCipher RAW-KEY mode: the 64-hex value is the AES key
// directly (no outer PBKDF2). Shares the production page loop via deriveModes+encryptPages, so
// the image it produces is only decryptable through decryptSQLCipher's raw-key fallback.
func mustEncryptRawKey(t *testing.T, plain []byte, hexKey string) []byte {
	t.Helper()
	salt := make([]byte, scSaltSize)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	modes, err := deriveModes(hexKey, salt)
	if err != nil {
		t.Fatal(err)
	}
	if len(modes) < 2 || modes[1].name != "raw-key" {
		t.Fatalf("expected a raw-key mode for a 64-hex key, got %d modes", len(modes))
	}
	img, err := encryptPages(plain, modes[1].enc, modes[1].mac, salt)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// plainImage builds an n-page plaintext SQLite image with the reserve region zeroed and page-1
// magic + reserved-byte set, exactly as decryptSQLCipher reconstructs it (for byte-exact
// round-trip assertions). Pages 2..n carry random bytes.
func plainImage(t *testing.T, n int) []byte {
	t.Helper()
	plain := make([]byte, n*scPageSize)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	ctEnd := scPageSize - scReserve
	for p := 0; p < n; p++ {
		for i := p*scPageSize + ctEnd; i < (p+1)*scPageSize; i++ {
			plain[i] = 0 // decrypt zeroes the reserve region
		}
	}
	copy(plain[:scSaltSize], sqliteMagic) // decrypt rewrites page-1 magic
	plain[20] = scReserve                 // ...and the reserved-bytes header field
	return plain
}

// TestSQLCipherPageCrypto round-trips a multi-page image (reserve region kept zero, page-1
// magic + reserved-byte set) and asserts byte-exact recovery + wrong-key rejection.
func TestSQLCipherPageCrypto(t *testing.T) {
	const n = 3
	plain := make([]byte, n*scPageSize)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	ctEnd := scPageSize - scReserve
	for p := 0; p < n; p++ {
		// decrypt zeroes the reserve region, so the fixture must too for a byte-exact match.
		for i := p*scPageSize + ctEnd; i < (p+1)*scPageSize; i++ {
			plain[i] = 0
		}
	}
	copy(plain[:scSaltSize], sqliteMagic) // decrypt rewrites page-1 magic
	plain[20] = scReserve                 // ...and the reserved-bytes header field

	const pass = "round-trip-secret"
	enc := mustEncrypt(t, plain, pass)
	dec, err := decryptSQLCipher(enc, pass)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatal("round-trip mismatch")
	}
	if !bytes.Equal(dec[:scSaltSize], sqliteMagic) || dec[20] != scReserve {
		t.Errorf("page-1 reconstruction: magic/reserved wrong")
	}
	if _, err := decryptSQLCipher(enc, "wrong-key"); err == nil {
		t.Error("expected HMAC failure with wrong key")
	}
}

// TestZeroPagePassthrough: an all-zero (never-written) page decrypts to all zero, no HMAC check.
func TestZeroPagePassthrough(t *testing.T) {
	plain := make([]byte, 2*scPageSize)
	copy(plain[:scSaltSize], sqliteMagic)
	plain[20] = scReserve
	enc := mustEncrypt(t, plain[:scPageSize], "k")  // encrypt only page 1
	img := append(enc, make([]byte, scPageSize)...) // page 2 left all-zero
	dec, err := decryptSQLCipher(img, "k")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !isAllZero(dec[scPageSize:]) {
		t.Error("zero page should pass through as zero")
	}
}

// TestSQLCipherRawKeyFallback proves the raw-key fallback: an image encrypted with a raw AES
// key (SQLCipher PRAGMA key="x'…'" mode) round-trips through decryptSQLCipher(image, hexKey).
// The image is auth-verified to be UNdecryptable in passphrase mode first, so the round-trip
// below can only succeed via the raw-key retry branch - not passphrase mode.
func TestSQLCipherRawKeyFallback(t *testing.T) {
	const n = 3
	plain := plainImage(t, n)
	// 64 hex chars = a 32-byte raw AES key (distinct from the DefaultRekordboxKey).
	const hexKey = "00112233445566778899aabbccddeeff102132435465768798a9bacbdcedfe0f"
	img := mustEncryptRawKey(t, plain, hexKey)

	// Prove passphrase mode CANNOT decrypt this raw-key image (page-1 HMAC mismatch), so the
	// success below is attributable to the raw-key fallback alone.
	modes, err := deriveModes(hexKey, img[:scSaltSize])
	if err != nil {
		t.Fatal(err)
	}
	if modes[0].name != "passphrase" {
		t.Fatalf("modes[0]=%q, want passphrase", modes[0].name)
	}
	if _, err := decryptPages(img, modes[0].enc, modes[0].mac); err == nil {
		t.Fatal("passphrase mode unexpectedly decrypted a raw-key image; test can't prove fallback")
	}

	dec, err := decryptSQLCipher(img, hexKey) // must traverse the raw-key fallback
	if err != nil {
		t.Fatalf("raw-key decrypt: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatal("raw-key round-trip mismatch")
	}
	if !bytes.Equal(dec[:scSaltSize], sqliteMagic) || dec[20] != scReserve {
		t.Errorf("page-1 reconstruction: magic/reserved wrong")
	}

	// A different 64-hex key fails in BOTH modes (no false accept).
	const wrongHex = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, err := decryptSQLCipher(img, wrongHex); err == nil {
		t.Error("expected failure with a wrong raw key")
	}
}

// TestSQLCipherPassphrasePrimary confirms passphrase mode stays primary: a passphrase-encrypted
// image (even one whose passphrase is 64 hex chars, like the real default key) decrypts without
// the raw-key branch producing a false positive.
func TestSQLCipherPassphrasePrimary(t *testing.T) {
	plain := plainImage(t, 2)
	enc := mustEncrypt(t, plain, DefaultRekordboxKey) // 64-hex passphrase, PBKDF2 path
	dec, err := decryptSQLCipher(enc, DefaultRekordboxKey)
	if err != nil {
		t.Fatalf("passphrase decrypt: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatal("passphrase round-trip mismatch")
	}
}

// TestOpenRealMasterDB decrypts + reads an actual Rekordbox master.db when RAVE_REKORDBOX_MASTER
// points at one (skipped otherwise). Key from RAVE_REKORDBOX_KEY or the default.
func TestOpenRealMasterDB(t *testing.T) {
	path := os.Getenv("RAVE_REKORDBOX_MASTER")
	if path == "" {
		t.Skip("set RAVE_REKORDBOX_MASTER to a real master.db to run")
	}
	lib, err := Open(path, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Logf("tracks=%d playlists=%d sessions=%d", len(lib.Tracks), len(lib.Playlists), len(lib.Sessions))
}
