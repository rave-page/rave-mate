// Package musiclib - backup.go
//
// Call BackupCollection before any feature that rewrites a collection (e.g. path-fixing).
// The backup is a plain filesystem copy - originals are NEVER modified.
package musiclib

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backup describes one completed library backup.
type Backup struct {
	Path      string    // absolute path to the backup dir
	When      time.Time // timestamp used when creating the dir
	SizeBytes int64     // total bytes copied into Path
	Source    string    // collection file that was backed up
}

// BackupCollection backs up in.Collection (and in.HistoryDir if present) into a
// timestamped subdir of backupRoot, using time.Now() as the timestamp.
// Convenience wrapper around BackupCollectionAt.
func BackupCollection(in TraktorInstall, backupRoot string) (Backup, error) {
	return BackupCollectionAt(in, backupRoot, time.Now())
}

// BackupCollectionAt is the deterministic form - accepts an explicit now timestamp.
// Creates <backupRoot>/traktor-<version>-<YYYYMMDD-HHMMSS>/, stream-copies collection.nml
// into it via io.Copy (never loads whole file), then copies each .nml in HistoryDir.
// NEVER touches in.Dir or any source file.
func BackupCollectionAt(in TraktorInstall, backupRoot string, now time.Time) (Backup, error) {
	if in.Collection == "" {
		return Backup{}, errors.New("backup: no collection path in TraktorInstall")
	}
	if backupRoot == "" {
		return Backup{}, errors.New("backup: backupRoot must not be empty")
	}

	stamp := now.Format("20060102-150405")
	// sanitise version for use in a dir name
	ver := strings.ReplaceAll(in.Version, string(os.PathSeparator), "_")
	destDir := filepath.Join(backupRoot, fmt.Sprintf("traktor-%s-%s", ver, stamp))

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return Backup{}, fmt.Errorf("backup: create dest dir: %w", err)
	}

	var total int64

	// copy collection.nml
	n, err := copyFile(in.Collection, filepath.Join(destDir, filepath.Base(in.Collection)))
	if err != nil {
		return Backup{}, fmt.Errorf("backup: copy collection: %w", err)
	}
	total += n

	// copy History/ .nml files if present
	if in.HistoryDir != "" {
		ents, err := os.ReadDir(in.HistoryDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Backup{}, fmt.Errorf("backup: read history dir: %w", err)
		}
		histDest := filepath.Join(destDir, "History")
		if len(ents) > 0 {
			if err := os.MkdirAll(histDest, 0o755); err != nil {
				return Backup{}, fmt.Errorf("backup: create history dest: %w", err)
			}
			for _, e := range ents {
				if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".nml") {
					continue
				}
				src := filepath.Join(in.HistoryDir, e.Name())
				m, err := copyFile(src, filepath.Join(histDest, e.Name()))
				if err != nil {
					return Backup{}, fmt.Errorf("backup: copy history file %s: %w", e.Name(), err)
				}
				total += m
			}
		}
	}

	return Backup{
		Path:      destDir,
		When:      now,
		SizeBytes: total,
		Source:    in.Collection,
	}, nil
}

// ListBackups returns all backup subdirs under backupRoot, sorted newest first.
// Each Backup.SizeBytes is the sum of all files in that dir tree.
func ListBackups(backupRoot string) ([]Backup, error) {
	if backupRoot == "" {
		return nil, errors.New("listbackups: backupRoot must not be empty")
	}
	ents, err := os.ReadDir(backupRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var backups []Backup
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "traktor-") {
			continue
		}
		dir := filepath.Join(backupRoot, e.Name())
		size, err := dirSize(dir)
		if err != nil {
			return nil, fmt.Errorf("listbackups: stat %s: %w", e.Name(), err)
		}
		// When = the YYYYMMDD-HHMMSS stamp baked into the dir name at creation, not
		// the filesystem mtime (all dirs written "now" → mtimes can't order them).
		when, ok := backupStamp(e.Name())
		if !ok {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			when = info.ModTime()
		}
		backups = append(backups, Backup{
			Path:      dir,
			When:      when,
			SizeBytes: size,
		})
	}

	// newest first
	sort.Slice(backups, func(i, j int) bool { return backups[i].When.After(backups[j].When) })
	return backups, nil
}

// PruneBackups deletes the oldest backup dirs beyond keep, operating ONLY under
// backupRoot. Guards: backupRoot must not be empty and must not look like a Traktor
// library dir (must not contain "Native Instruments").
func PruneBackups(backupRoot string, keep int) error {
	if backupRoot == "" {
		return errors.New("prunebackups: backupRoot must not be empty")
	}
	if strings.Contains(filepath.ToSlash(backupRoot), "Native Instruments") {
		return errors.New("prunebackups: refusing to operate on a path under Native Instruments")
	}
	if keep < 0 {
		keep = 0
	}

	backups, err := ListBackups(backupRoot)
	if err != nil {
		return err
	}
	// backups is sorted newest-first; drop the tail beyond keep
	if len(backups) <= keep {
		return nil
	}
	for _, b := range backups[keep:] {
		if err := os.RemoveAll(b.Path); err != nil {
			return fmt.Errorf("prunebackups: remove %s: %w", b.Path, err)
		}
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// backupStamp parses the trailing YYYYMMDD-HHMMSS stamp of a backup dir name
// (traktor-<ver>-YYYYMMDD-HHMMSS) into the time it was created. ok=false if absent.
func backupStamp(name string) (time.Time, bool) {
	const layout = "20060102-150405"
	if len(name) < len(layout) {
		return time.Time{}, false
	}
	t, err := time.Parse(layout, name[len(name)-len(layout):])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// copyFile stream-copies src → dst via io.Copy; returns bytes written.
func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.Copy(out, in)
	if err != nil {
		return n, err
	}
	return n, out.Close()
}

// dirSize sums file sizes under root (recursive).
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
