package app

import (
	"encoding/json"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/maintenance"
	"rave.page/mate/internal/musiclib"
)

// CleanupMissing removes library tracks whose local files are gone from the rave-mate DB AND prunes
// them from the source Traktor collection.nml (ctl CLEANUP-MISSING) - so a re-import won't re-add
// them. Auto-detects the newest Traktor collection (same as the UI/import). Backs up first. Returns
// the report as one-line JSON. Local; no token. dry==true reports the counts without changing anything.
func (c *appControl) CleanupMissing(dry bool) string {
	if c.lib == nil {
		return `{"error":"no library"}`
	}
	col := ""
	if installs, err := musiclib.DiscoverTraktor(); err == nil && len(installs) > 0 {
		col = installs[0].Collection
	}
	if dry {
		if col == "" {
			return `{"error":"no Traktor collection.nml found"}`
		}
		missing, pathless, err := maintenance.ScanMissingFromCollection(col)
		if err != nil {
			return `{"error":` + jsonString(err.Error()) + `}`
		}
		b, _ := json.Marshal(map[string]any{"dryRun": true, "missingTracks": len(missing), "pathless": pathless, "collectionPath": col})
		return string(b)
	}
	root, err := config.DataPath("library-backups")
	if err != nil {
		return `{"error":` + jsonString(err.Error()) + `}`
	}
	// Async: the op backs up the (multi-GB) DB + prunes the 266 MB collection.nml, far longer than
	// the ctl client read deadline. Return immediately; log the report when done (visible via LOGS).
	debuglog.Go(c.log, "cleanup", func() {
		rep, err := maintenance.CleanupMissing(c.lib, col, root)
		if err != nil {
			c.log.Warn("cleanup", "cleanup-missing failed", map[string]any{"err": err.Error()})
			return
		}
		c.log.Info("cleanup", "cleanup-missing done", map[string]any{
			"missing": rep.MissingTracks, "dbTracksDeleted": rep.TracksDeleted,
			"nmlTracksRemoved": rep.NMLTracksRemoved, "nmlRefsRemoved": rep.NMLPlaylistRefsRemvd,
			"nmlError": rep.NMLError, "backupDir": rep.BackupDir,
		})
	})
	b, _ := json.Marshal(map[string]any{"started": true, "collectionPath": col, "note": "runs async - see ctl logs for the report"})
	return string(b)
}

// jsonString quote-escapes s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
