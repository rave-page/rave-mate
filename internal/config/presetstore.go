package config

// User-data durability. Born 2026-08-11: the user's custom encode presets died
// with a config that never hit disk - user-authored content must never live in
// ONE file with a silent save path.
//
// Custom transcode presets: config.json stays the in-memory source every
// consumer reads (Features.Transcode.Presets), but the durable AUTHORITY is
// transcode-presets.json beside it - own atomic save + .bak, merged into the
// config at Load (file wins by ID). A config save cycle that drops the presets
// field - old build, zero-bug reset, save that failed before quit - self-heals
// on the next start.
//
// Config history: every successful Save also drops a timestamped copy into
// <configDir>/config-backups/ (newest backupKeep kept), so no future save
// cycle can destroy both config.json and .bak.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/transcode"
)

const (
	presetsFileName = "transcode-presets.json"
	backupDirName   = "config-backups"
	backupKeep      = 14
)

type presetsFile struct {
	Presets []transcode.Preset `json:"presets"`
}

// SavePresetsFile durably mirrors the custom presets (atomic + fsync + .bak).
// Call on every preset mutation - an empty slice is a legit "all deleted".
func SavePresetsFile(ps []transcode.Preset) error {
	path, err := DataPath(presetsFileName)
	if err != nil {
		return err
	}
	if ps == nil {
		ps = []transcode.Preset{}
	}
	raw, err := json.MarshalIndent(presetsFile{Presets: ps}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if _, serr := os.Stat(path); serr == nil {
		_ = os.Remove(path + ".bak")
		_ = os.Rename(path, path+".bak")
	}
	return os.Rename(tmp, path)
}

// loadPresetsFile returns the mirrored presets (.bak fallback; nil when absent).
func loadPresetsFile() []transcode.Preset {
	path, err := DataPath(presetsFileName)
	if err != nil {
		return nil
	}
	for _, p := range []string{path, path + ".bak"} {
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		var pf presetsFile
		if json.Unmarshal(raw, &pf) == nil {
			return pf.Presets
		}
	}
	return nil
}

// mergePresets unions durable-file + config presets by ID (file wins, file
// order first). Union on purpose: in a conflicted edge a resurrected preset
// beats a lost one.
func mergePresets(file, cfg []transcode.Preset) []transcode.Preset {
	if len(file) == 0 {
		return cfg
	}
	seen := make(map[string]bool, len(file))
	out := make([]transcode.Preset, 0, len(file)+len(cfg))
	for _, p := range file {
		seen[p.ID] = true
		out = append(out, p)
	}
	for _, p := range cfg {
		if !seen[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// rotateBackup drops raw into the rotating config history (best-effort).
func rotateBackup(raw []byte) {
	dir, err := Dir()
	if err != nil {
		return
	}
	bdir := filepath.Join(dir, backupDirName)
	if os.MkdirAll(bdir, 0o700) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(bdir, time.Now().Format("config-20060102-150405.json")), raw, 0o600)
	ents, err := os.ReadDir(bdir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "config-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamped names sort chronologically
	for len(names) > backupKeep {
		_ = os.Remove(filepath.Join(bdir, names[0]))
		names = names[1:]
	}
}
