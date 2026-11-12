package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestDirIsOwnerOnly: the app config dir (identity keypair + remembered peers; on Linux the raw
// Ed25519 seed lives in the 0600 bbolt file here) is created owner-only (0700). Unix perms are a
// no-op on Windows, so this asserts only there.
func TestDirIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	target := filepath.Join(t.TempDir(), "cfg-perm") // must not exist yet, so MkdirAll sets the mode
	t.Setenv("RAVE_MATE_CONFIG_DIR", target)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o700 {
		t.Fatalf("config dir perm = %o, want 0700", mode)
	}
}
