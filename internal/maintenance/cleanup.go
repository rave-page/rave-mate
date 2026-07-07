// Package maintenance holds user-triggered library housekeeping that spans the rave-mate DB AND
// the source DJ collection file - kept out of the UI so it can also run headless (ctl) and be tested.
package maintenance

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// CleanupReport summarises a missing-file cleanup across the rave-mate DB and the source collection.
type CleanupReport struct {
	MissingTracks        int    `json:"missingTracks"`        // tracks whose local file is gone
	Pathless             int    `json:"pathless"`             // entries with no local path (streaming/purged) - left alone
	TracksDeleted        int    `json:"tracksDeleted"`        // rave-mate DB track rows removed
	PlaylistEntriesDel   int    `json:"playlistEntriesDel"`   // DB playlist rows pointing at a removed path
	EmptyPlaylistsDel    int    `json:"emptyPlaylistsDel"`    // DB playlists left empty
	NMLTracksRemoved     int    `json:"nmlTracksRemoved"`     // collection.nml track ENTRYs dropped
	NMLPlaylistRefsRemvd int    `json:"nmlPlaylistRefsRemvd"` // collection.nml playlist refs dropped
	BackupDir            string `json:"backupDir"`
	CollectionPath       string `json:"collectionPath,omitempty"`
	NMLError             string `json:"nmlError,omitempty"` // prune failed (DB still cleaned) - non-fatal
}

// ScanMissingFromCollection parses collectionPath (the source Traktor collection.nml) and returns
// every distinct track path whose local file no longer exists, plus the count of path-less entries.
// The collection is the source of truth for what a re-import reads - scanning it (not the already-
// deduped/cleaned DB) is what makes the cleanup actually idempotent against re-import.
func ScanMissingFromCollection(collectionPath string) (missing []string, pathless int, err error) {
	f, oerr := os.Open(collectionPath)
	if oerr != nil {
		return nil, 0, oerr
	}
	defer func() { _ = f.Close() }()
	seen := make(map[string]bool)
	_, perr := musiclib.ParseCollection(f, func(t musiclib.Track) {
		p := strings.TrimSpace(t.Path)
		if p == "" {
			pathless++
			return
		}
		if seen[p] {
			return
		}
		seen[p] = true
		if _, e := os.Stat(p); e != nil {
			missing = append(missing, p)
		}
	})
	return missing, pathless, perr
}

// CleanupMissing removes every track whose local file no longer exists (plus its playlist
// references) from the source Traktor collection.nml AND the rave-mate DB, so a re-import doesn't
// re-add them. "Missing" is determined from the collection.nml (what a re-import reads), falling
// back to the DB only when no collection is given. Takes a full backup (collection.nml + DB) into
// backupRoot first. Path-less entries (streaming / privacy-purged) are never touched. The NML prune
// is the primary action; the DB delete strips any of the same paths still present there.
func CleanupMissing(lib *libdb.DB, collectionPath, backupRoot string) (CleanupReport, error) {
	var rep CleanupReport
	rep.CollectionPath = collectionPath
	if lib == nil {
		return rep, fmt.Errorf("maintenance: nil library")
	}
	var paths []string
	if collectionPath != "" {
		m, pl, err := ScanMissingFromCollection(collectionPath)
		if err != nil {
			return rep, err
		}
		paths, rep.Pathless = m, pl
	} else {
		tracks, err := lib.LoadAllTracks()
		if err != nil {
			return rep, err
		}
		_, missingAll := musiclib.ScanMissing(tracks)
		for _, t := range missingAll {
			if strings.TrimSpace(t.Path) == "" {
				rep.Pathless++
				continue
			}
			paths = append(paths, t.Path)
		}
	}
	rep.MissingTracks = len(paths)
	if len(paths) == 0 {
		return rep, nil // nothing to remove
	}

	if err := os.MkdirAll(backupRoot, 0o755); err != nil {
		return rep, fmt.Errorf("backup dir: %w", err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	rep.BackupDir = backupRoot
	// 1. source collection.nml (so the destructive prune is reversible).
	if collectionPath != "" {
		if err := copyFile(collectionPath, filepath.Join(backupRoot, "collection-"+stamp+".nml")); err != nil {
			return rep, fmt.Errorf("backup collection.nml: %w", err)
		}
	}
	// 2. rave-mate library DB snapshot.
	if err := lib.BackupTo(filepath.Join(backupRoot, "library-"+stamp+".db")); err != nil {
		return rep, fmt.Errorf("backup library db: %w", err)
	}

	// 3. delete from the rave-mate DB.
	res, err := lib.DeleteTracksByPaths(paths)
	if err != nil {
		return rep, err
	}
	rep.TracksDeleted = res.TracksDeleted
	rep.PlaylistEntriesDel = res.PlaylistEntriesDeleted
	rep.EmptyPlaylistsDel = res.EmptyPlaylistsDeleted

	// 4. prune the SAME tracks/refs from the source collection.nml (best-effort).
	if collectionPath != "" {
		removed := make(map[string]bool, len(paths))
		for _, p := range paths {
			removed[p] = true
		}
		pr, perr := musiclib.PruneCollectionFile(collectionPath, removed)
		if perr != nil {
			rep.NMLError = perr.Error()
		} else {
			rep.NMLTracksRemoved = pr.TracksRemoved
			rep.NMLPlaylistRefsRemvd = pr.RefsRemoved
		}
	}
	return rep, nil
}

// copyFile streams src→dst (overwriting dst).
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
	_, cerr := io.Copy(out, in)
	if e := out.Close(); cerr == nil {
		cerr = e
	}
	return cerr
}
