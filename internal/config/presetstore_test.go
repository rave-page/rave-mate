package config

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/transcode"
)

func testPreset(id string) transcode.Preset {
	return transcode.Preset{ID: id, Label: id, Container: "mp4", VideoCodec: "h264", AudioCodec: "aac", CRF: 20}
}

func TestPresetsFileRoundTrip(t *testing.T) {
	t.Setenv("RAVE_MATE_CONFIG_DIR", t.TempDir())
	ps := []transcode.Preset{testPreset("a"), testPreset("b")}
	if err := SavePresetsFile(ps); err != nil {
		t.Fatal(err)
	}
	got := loadPresetsFile()
	if len(got) != 2 || got[0].ID != "a" || got[1].CRF != 20 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	// second save keeps a .bak; corrupting the main file falls back to it
	if err := SavePresetsFile(ps[:1]); err != nil {
		t.Fatal(err)
	}
	path, _ := DataPath(presetsFileName)
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got = loadPresetsFile(); len(got) != 2 {
		t.Fatalf("bak fallback failed: %+v", got)
	}
}

// TestLoadSelfHealsPresets: a config that lost its presets field gets them back
// from the durable mirror at Load - the 2026-08-11 loss scenario.
func TestLoadSelfHealsPresets(t *testing.T) {
	t.Setenv("RAVE_MATE_CONFIG_DIR", t.TempDir())
	cfg := Default()
	cfg.Features.Transcode.Presets = []transcode.Preset{testPreset("mine")}
	if err := SavePresetsFile(cfg.Features.Transcode.Presets); err != nil {
		t.Fatal(err)
	}
	cfg.Features.Transcode.Presets = nil // config cycle dropped the field
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	ps := got.Features.Transcode.Presets
	if len(ps) != 1 || ps[0].ID != "mine" {
		t.Fatalf("presets not restored from mirror: %+v", ps)
	}
}

func TestMergePresetsFileWins(t *testing.T) {
	file := []transcode.Preset{testPreset("x"), testPreset("y")}
	file[0].CRF = 11
	cfg := []transcode.Preset{testPreset("x"), testPreset("z")}
	out := mergePresets(file, cfg)
	if len(out) != 3 || out[0].CRF != 11 || out[2].ID != "z" {
		t.Fatalf("merge wrong: %+v", out)
	}
}

func TestSaveRotatesBackups(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)
	cfg := Default()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(dir, backupDirName))
	if err != nil || len(ents) == 0 {
		t.Fatalf("no rotating backup written: %v %d", err, len(ents))
	}
}
