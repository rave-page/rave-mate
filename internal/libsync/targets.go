package libsync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/rekordboxdb"
	"rave.page/mate/internal/sysactivity"
)

// Target modes + apps (mirror config.SyncTarget).
const (
	ModeFile      = "file"      // write an importable collection (NML/XML)
	ModeWriteback = "writeback" // live in-place into the app's library
	ModeTags      = "tags"      // embed metadata into the audio files

	AppTraktor   = "traktor"
	AppRekordbox = "rekordbox"
	AppVirtualDJ = "virtualdj"
)

// TargetOutcome reports what writing one target did.
type TargetOutcome struct {
	App, Mode, Path, Note string
	Updated, Added        int
}

// applyTarget writes the canonical tracks to one target per its mode. Returns a human-readable
// outcome. Tags mode is handled by the engine (target-independent), so it's a no-op here.
func applyTarget(t config.SyncTarget, tracks []musiclib.Track) (TargetOutcome, error) {
	out := TargetOutcome{App: t.App, Mode: t.Mode}
	switch t.Mode {
	case ModeFile:
		return writeImportableFile(t, tracks)
	case ModeWriteback:
		return writeBack(t, tracks)
	case ModeTags:
		return out, nil // engine applies tags
	}
	return out, fmt.Errorf("unknown target mode %q", t.Mode)
}

// writeImportableFile writes a collection file the target app can import (atomic temp+rename).
func writeImportableFile(t config.SyncTarget, tracks []musiclib.Track) (TargetOutcome, error) {
	out := TargetOutcome{App: t.App, Mode: ModeFile}
	path, err := resolveFileOutput(t)
	if err != nil {
		return out, err
	}
	out.Path = path
	switch t.App {
	case AppTraktor:
		err = atomicWriteFile(path, func(w io.Writer) error {
			return musiclib.ExportTraktorNML(musiclib.Library{Tracks: tracks}, w)
		})
	case AppRekordbox:
		err = atomicWriteFile(path, func(w io.Writer) error {
			return musiclib.ExportRekordboxXML(tracks, w)
		})
	default:
		return out, fmt.Errorf("importable-file export not supported for %q", t.App)
	}
	if err != nil {
		return out, err
	}
	out.Added = len(tracks)
	out.Note = fmt.Sprintf("wrote %d tracks → %s (Import Collection in %s)", len(tracks), filepath.Base(path), t.App)
	return out, nil
}

// writeBack writes the canonical tracks into the app's live library. Traktor = in-place
// collection.nml merge (backed up first). Rekordbox master.db can't be re-encrypted, so it falls
// back to dropping an importable XML next to the user's library + a note to import it.
func writeBack(t config.SyncTarget, tracks []musiclib.Track) (TargetOutcome, error) {
	out := TargetOutcome{App: t.App, Mode: ModeWriteback}
	switch t.App {
	case AppTraktor:
		path := t.OutputPath
		if path == "" {
			path = discoverTraktorCollection()
		}
		if path == "" {
			return out, fmt.Errorf("no Traktor collection.nml found (set an output path)")
		}
		out.Path = path
		if err := backupBeforeWrite(path); err != nil {
			return out, fmt.Errorf("backup: %w", err)
		}
		res, err := musiclib.MergeIntoCollectionFile(path, tracks)
		if err != nil {
			return out, err
		}
		out.Updated, out.Added = res.Updated, res.Added
		out.Note = fmt.Sprintf("collection.nml: %d updated, %d added (backed up first)", res.Updated, res.Added)
		return out, nil
	case AppRekordbox:
		// Native master.db insert: tracks appear directly in the Rekordbox Collection.
		path := t.OutputPath
		if path == "" {
			dbs := rekordboxdb.DiscoverRekordboxMasterDB()
			if len(dbs) == 0 {
				return out, fmt.Errorf("no Rekordbox master.db found (set an output path)")
			}
			path = dbs[0]
		}
		out.Path = path
		if rekordboxRunning() {
			return out, fmt.Errorf("Rekordbox is open - close it before live write-back")
		}
		if err := rekordboxdb.Probe(path); err != nil {
			return out, fmt.Errorf("can't unlock master.db (set the key in Settings): %w", err)
		}
		if err := backupBeforeWrite(path); err != nil {
			return out, fmt.Errorf("backup: %w", err)
		}
		res, err := rekordboxdb.InsertTracks(path, "", tracks)
		if err != nil {
			return out, err
		}
		out.Added = res.Inserted
		out.Note = fmt.Sprintf("master.db: %d added, %d already present (backed up first)", res.Inserted, res.Skipped)
		return out, nil
	}
	return out, fmt.Errorf("live write-back not supported for %q", t.App)
}

// rekordboxRunning reports whether Rekordbox appears to be running (refuse a live write then).
// Fails closed-enough: if processes can't be enumerated we still rely on the atomic file replace,
// which Windows refuses while Rekordbox holds the DB open.
func rekordboxRunning() bool {
	if set, ok := sysactivity.New().RunningProcesses(); ok {
		return sysactivity.Running(set, "rekordbox")
	}
	return false
}

// resolveFileOutput returns the target's output path or a default under <config>/sync.
func resolveFileOutput(t config.SyncTarget) (string, error) {
	if t.OutputPath != "" {
		return t.OutputPath, nil
	}
	dir, err := config.DataPath("sync")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := "rave-sync.xml"
	if t.App == AppTraktor {
		name = "rave-sync-collection.nml"
	} else if t.App == AppRekordbox {
		name = "rave-sync-rekordbox.xml"
	}
	return filepath.Join(dir, name), nil
}

// discoverTraktorCollection returns the newest Traktor install's collection.nml ("" if none).
func discoverTraktorCollection() string {
	installs, err := musiclib.DiscoverTraktor()
	if err != nil {
		return ""
	}
	for _, in := range installs {
		if in.Collection != "" {
			return in.Collection
		}
	}
	return ""
}

// backupBeforeWrite copies path into <config>/library-backups/<base>-<n>.bak before an in-place
// write. Uses a monotonic suffix so concurrent/repeat runs don't clobber a prior backup. The
// backup is best-effort durable (synced).
func backupBeforeWrite(path string) error {
	dir, err := config.DataPath("library-backups")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := filepath.Base(path)
	var dst string
	for n := 0; ; n++ {
		dst = filepath.Join(dir, fmt.Sprintf("%s.%d.bak", base, n))
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
	}
	return copyFile(path, dst)
}

// atomicWriteFile writes via a temp file in the same dir, fsync, then rename over the target.
func atomicWriteFile(path string, write func(io.Writer) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	werr := write(tmp)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return werr
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// copyFile streams src→dst with a final sync.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
