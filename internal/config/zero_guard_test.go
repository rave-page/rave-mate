package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// zeroWavJSON marshals the exact production artifact (2026-07-26): a zero-value Config with
// only features.audioRecord.format="wav" set - byte-identical to the clobber files.
func zeroWavJSON(t *testing.T) []byte {
	t.Helper()
	var c Config
	c.Features.AudioRecord.Format = "wav"
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Save must refuse a zero-value Config (Version 0) - the 2026-07-26 data-loss writer's
// signature - leave the on-disk config untouched, and drop a stack tripwire naming the caller.
func TestSaveRefusesZeroConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)

	good := Default()
	good.Features.AudioRecord.Format = "wav"
	if err := good.Save(); err != nil {
		t.Fatal(err)
	}

	var zero Config
	zero.Features.AudioRecord.Format = "wav"
	err := zero.Save()
	if !errors.Is(err, ErrZeroConfig) {
		t.Fatalf("zero save err = %v, want ErrZeroConfig", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if json.Unmarshal(raw, &got) != nil || got.Version != configVersion {
		t.Fatalf("on-disk config clobbered by refused zero save: version=%d", got.Version)
	}
	stacks, _ := filepath.Glob(filepath.Join(dir, "zero-config-save-*.stack"))
	if len(stacks) != 1 {
		t.Fatalf("tripwire stack not written: %v", stacks)
	}
	if b, _ := os.ReadFile(stacks[0]); len(b) == 0 {
		t.Fatalf("tripwire stack empty")
	}
}

// Load must treat the zero artifact as corruption: preserve evidence, recover from .bak -
// never boot with every feature silently disabled and then re-persist the zeros.
func TestLoadQuarantinesZeroFileAndRecoversFromBak(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)
	path := filepath.Join(dir, fileName)

	good := Default()
	good.Features.AudioRecord.Format = "wav"
	good.Features.VRChat.Enabled = true
	gb, err := json.MarshalIndent(good, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", gb, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, zeroWavJSON(t), 0o600); err != nil {
		t.Fatal(err)
	}

	got, lerr := Load()
	if lerr == nil {
		t.Fatalf("zero-artifact load returned nil error (silent zero boot)")
	}
	if got.Version != configVersion || !got.Features.VRChat.Enabled {
		t.Fatalf("did not recover .bak: version=%d vrchat=%v", got.Version, got.Features.VRChat.Enabled)
	}
	m, _ := filepath.Glob(path + ".zero-*")
	if len(m) != 1 {
		t.Fatalf("zero artifact not preserved: %v", m)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("zero config.json still in place")
	}
}

// No usable .bak (absent or itself a zero artifact): still quarantine + loud default reset.
func TestLoadZeroFileWithoutBakResetsLoudly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, zeroWavJSON(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", zeroWavJSON(t), 0o600); err != nil {
		t.Fatal(err)
	}
	got, lerr := Load()
	if lerr == nil {
		t.Fatalf("zero-artifact load returned nil error")
	}
	if got.Version != configVersion || !got.Features.Traktor.Enabled {
		t.Fatalf("expected loud Default() reset, got version=%d", got.Version)
	}
}

// A legit pre-v1 legacy flat file (version 0, no "features" key) must keep migrating -
// the zero-artifact quarantine must not eat it.
func TestLoadLegacyFlatConfigStillMigrates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)
	path := filepath.Join(dir, fileName)
	legacy := []byte(`{"version":0,"traktorEnable":false,"traktorLog":true,"notifyEnable":false}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	got, lerr := Load()
	if lerr != nil {
		t.Fatalf("legacy load err = %v", lerr)
	}
	if got.Version != configVersion {
		t.Fatalf("legacy file not migrated: version=%d", got.Version)
	}
	if got.Features.Traktor.Enabled || !got.Features.Traktor.LogPayloads || got.Features.Notifications.Enabled {
		t.Fatalf("legacy flat fields not applied: %+v", got.Features.Traktor)
	}
}

// The exact production bytes (zero + audioRecord.format=wav) are recognized as the artifact.
func TestIsZeroBugFileMatchesProductionArtifact(t *testing.T) {
	raw := zeroWavJSON(t)
	cfg := Default()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if !isZeroBugFile(cfg, raw) {
		t.Fatalf("production artifact not detected")
	}
	// A healthy saved config is NOT flagged.
	hb, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	h := Default()
	if err := json.Unmarshal(hb, &h); err != nil {
		t.Fatal(err)
	}
	if isZeroBugFile(h, hb) {
		t.Fatalf("healthy config flagged as zero artifact")
	}
}
