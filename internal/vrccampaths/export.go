package vrccampaths

// Backup-on-export: every camera path VRChat exports gets a world-keyed backup copy the
// moment it lands in CameraPaths - not only paths rave-mate itself imported. Motivated by
// a real live-set loss: VRChat crashed after a dolly export and the import-time-only
// backup had nothing. Poll-based sweep (the vrctools service already ticks); a persisted
// seen-set keeps it idempotent across restarts, and the first sweep after enabling
// backfills every existing export (ascending mtime, so the newest per world ends up as
// that world's restorable latest).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/vrcloc"
)

const (
	exportSeenFile = "exports_seen.json"
	// exportQuiesce: a file this young may still be mid-write - picked up next sweep.
	exportQuiesce = 2 * time.Second
	// exportCurrentFallback: an export younger than this with no timeline entry at its
	// mtime falls back to the CURRENT world (live export while the timeline lags).
	exportCurrentFallback = 10 * time.Minute
)

// LoadExportSeen reads the persisted seen-set (path → mtime already backed up).
func LoadExportSeen(backupDir string) map[string]time.Time {
	m := map[string]time.Time{}
	data, err := os.ReadFile(filepath.Join(backupDir, exportSeenFile))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	return m
}

// SaveExportSeen persists the seen-set beside the backups.
func SaveExportSeen(backupDir string, seen map[string]time.Time) {
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(seen, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(backupDir, exportSeenFile), data, 0o644)
}

// SweepExports backs up loose exports in camDir's ROOT (new exports land there; organized
// copies, sidecars and the backup dir itself are out of scope) that the seen-set doesn't
// know yet, keyed by the world at each file's mtime (timeline; young files fall back to
// the current world). Unknown-world files are marked seen (a timeline gap won't heal by
// retrying) and skipped. Mutates seen; reports backups made + whether seen changed.
func SweepExports(camDir, backupDir string, tl *vrcloc.Timeline, seen map[string]time.Time, now time.Time, log func(string)) (int, bool) {
	ents, err := os.ReadDir(camDir)
	if err != nil {
		return 0, false
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		low := strings.ToLower(e.Name())
		if !strings.HasSuffix(low, ".json") || strings.HasSuffix(low, ".meta.json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < exportQuiesce {
			continue // possibly mid-write - next sweep
		}
		p := filepath.Join(camDir, e.Name())
		if seen[p].Equal(info.ModTime()) {
			continue
		}
		cands = append(cands, cand{p, info.ModTime()})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.Before(cands[j].mod) })
	backed, changed := 0, false
	for _, c := range cands {
		seen[c.path] = c.mod
		changed = true
		loc, ok := tl.At(c.mod)
		if !ok && now.Sub(c.mod) < exportCurrentFallback {
			loc, ok = tl.Current()
		}
		if !ok || loc.WorldID == "" {
			if log != nil {
				log("export " + filepath.Base(c.path) + ": world unknown - not backed up")
			}
			continue
		}
		if _, err := Backup(backupDir, c.path, loc.WorldID, loc.WorldName); err != nil {
			if log != nil {
				log("export backup failed: " + err.Error())
			}
			delete(seen, c.path) // retry next sweep - transient fs errors heal
			continue
		}
		backed++
		if log != nil {
			log("export " + filepath.Base(c.path) + " backed up for " + loc.WorldName)
		}
	}
	// prune seen entries whose files are gone (organized/moved/deleted) - keeps the set
	// bounded by the dir's actual content
	for p := range seen {
		if filepath.Dir(p) != filepath.Clean(camDir) {
			continue // a prior camDir's entry; harmless, keep
		}
		if _, err := os.Stat(p); os.IsNotExist(err) {
			delete(seen, p)
			changed = true
		}
	}
	return backed, changed
}
