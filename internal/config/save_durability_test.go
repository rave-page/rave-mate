package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Crash-corruption chain (production incident 2026-07-26: hard crash zeroed
// config.json, app silently reset to defaults and overwrote it): Save keeps the
// previous config as .bak; Load on a corrupt file preserves the evidence and
// recovers from .bak instead of resetting.
func TestSaveKeepsBakAndLoadRecoversFromCorrupt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)

	c1 := Default()
	c1.Features.AudioRecord.Format = "flac"
	if err := c1.Save(); err != nil {
		t.Fatal(err)
	}
	c2 := Default()
	c2.Features.AudioRecord.Format = "mp3"
	if err := c2.Save(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, fileName)
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("no .bak after second save: %v", err)
	}
	if !strings.Contains(string(bak), `"flac"`) {
		t.Fatalf(".bak is not the previous config")
	}

	// Simulate the crash: current file zeroed, .bak intact.
	if err := os.WriteFile(path, make([]byte, 512), 0o600); err != nil {
		t.Fatal(err)
	}
	got, lerr := Load()
	if lerr == nil {
		t.Fatalf("corrupt config load returned nil error (silent reset)")
	}
	if got.Features.AudioRecord.Format != "flac" {
		t.Fatalf("recovered config = %q, want the .bak value flac", got.Features.AudioRecord.Format)
	}
	// Evidence preserved, corrupt file no longer in place.
	m, _ := filepath.Glob(path + ".corrupt-*")
	if len(m) != 1 {
		t.Fatalf("corrupt file not preserved: %v", m)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("corrupt config.json still in place")
	}
}

// No .bak: corrupt file still preserved, defaults returned WITH an error.
func TestLoadCorruptWithoutBakResetsLoudly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, lerr := Load()
	if lerr == nil {
		t.Fatalf("want loud error on unrecoverable corrupt config")
	}
	if m, _ := filepath.Glob(path + ".corrupt-*"); len(m) != 1 {
		t.Fatalf("corrupt file not preserved: %v", m)
	}
}
