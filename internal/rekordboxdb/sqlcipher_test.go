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
