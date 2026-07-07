package vrccampaths

// Per-world camera-path backup: crash-resilience for live sets. VRChat exposes NO OSC readback for
// dolly paths, so a "backup" is a file copy of the exact JSON we import, keyed by the world it was
// played in. One latest copy per world (overwritten), tracked in index.json so restore is O(1).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"rave.page/mate/internal/vrcloc"
)

const backupIndexFile = "index.json"

var backupMu sync.Mutex // serializes index read/write across the backup + restore goroutines

// BackupEntry is the latest backed-up camera path for one world.
type BackupEntry struct {
	WorldID   string    `json:"worldId"`
	WorldName string    `json:"worldName,omitempty"`
	File      string    `json:"file"`   // backup copy (absolute)
	Source    string    `json:"source"` // original file it was copied from
	SavedAt   time.Time `json:"savedAt"`
}

// Backup copies srcFile into dir as this world's latest backup (overwriting any prior), writes a
// world sidecar, and updates the index. Requires worldID. Returns the recorded entry.
func Backup(dir, srcFile, worldID, worldName string) (BackupEntry, error) {
	if worldID == "" {
		return BackupEntry{}, errors.New("no world id")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return BackupEntry{}, err
	}
	dest := filepath.Join(dir, vrcloc.SanitizeName(worldID, "world")+".json")
	if err := copyFile(srcFile, dest); err != nil {
		return BackupEntry{}, err
	}
	now := time.Now()
	if data, err := json.MarshalIndent(Sidecar{WorldID: worldID, WorldName: worldName, SavedAt: now}, "", "  "); err == nil {
		_ = os.WriteFile(dest+".meta.json", data, 0o644)
	}
	e := BackupEntry{WorldID: worldID, WorldName: worldName, File: dest, Source: srcFile, SavedAt: now}
	backupMu.Lock()
	defer backupMu.Unlock()
	idx := loadBackupIndex(dir)
	idx[worldID] = e
	saveBackupIndex(dir, idx)
	return e, nil
}

// LatestBackup returns the recorded backup for worldID when its file still exists.
func LatestBackup(dir, worldID string) (BackupEntry, bool) {
	if worldID == "" {
		return BackupEntry{}, false
	}
	backupMu.Lock()
	e, ok := loadBackupIndex(dir)[worldID]
	backupMu.Unlock()
	if !ok {
		return BackupEntry{}, false
	}
	if _, err := os.Stat(e.File); err != nil {
		return BackupEntry{}, false // file gone
	}
	return e, true
}

func loadBackupIndex(dir string) map[string]BackupEntry {
	m := map[string]BackupEntry{}
	data, err := os.ReadFile(filepath.Join(dir, backupIndexFile))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	return m
}

func saveBackupIndex(dir string, m map[string]BackupEntry) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, backupIndexFile), data, 0o644)
}
