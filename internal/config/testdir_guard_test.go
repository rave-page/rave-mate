package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDirNeverRealInTests proves the 2026-07-26 wipe class is dead: without
// RAVE_MATE_CONFIG_DIR, tests resolve to a throwaway temp dir, never the user's
// real per-user config dir — so a fixture-driven Save can't clobber real settings.
func TestDirNeverRealInTests(t *testing.T) {
	t.Setenv("RAVE_MATE_CONFIG_DIR", "")
	os.Unsetenv("RAVE_MATE_CONFIG_DIR")
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if real, err := os.UserConfigDir(); err == nil {
		realApp := filepath.Join(real, appDirName)
		if dir == realApp || strings.HasPrefix(dir, realApp+string(os.PathSeparator)) {
			t.Fatalf("test Dir() resolved to the REAL config dir %s", dir)
		}
	}
	if !strings.Contains(filepath.Base(dir), "rave-mate-test-cfg-") {
		t.Fatalf("expected throwaway test dir, got %s", dir)
	}
	// Stable across calls (one dir per test process).
	dir2, err := Dir()
	if err != nil || dir2 != dir {
		t.Fatalf("Dir() not stable: %s vs %s (err %v)", dir, dir2, err)
	}
	// Explicit override still wins.
	want := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", want)
	got, err := Dir()
	if err != nil || got != want {
		t.Fatalf("override ignored: got %s want %s (err %v)", got, want, err)
	}
}
