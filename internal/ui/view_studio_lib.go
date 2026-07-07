package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagsync"
	"rave.page/mate/internal/tagwrite"
)

// persistTracks upserts a freshly-imported collection into the relational library DB and
// reports the refresh delta. Best-effort: a DB failure logs but never blocks the import.
func (sv *studioView) persistTracks(src musiclib.Source, tracks []musiclib.Track) {
	db := sv.u.svc.Lib
	if db == nil {
		return
	}
	row, err := db.UpsertSource(src, fileMtimeUnix(src.Path))
	if err != nil {
		sv.u.svc.Log.Warn("library", "persist source failed", map[string]any{"error": err.Error()})
		return
	}
	sy, err := db.BeginTrackSync(row.ID)
	if err != nil {
		sv.u.svc.Log.Warn("library", "begin sync failed", map[string]any{"error": err.Error()})
		return
	}
	for _, t := range tracks {
		if err := sy.Add(t); err != nil {
			sy.Rollback()
			sv.u.svc.Log.Warn("library", "sync add failed", map[string]any{"error": err.Error()})
			return
		}
	}
	res, err := sy.Commit()
	if err != nil {
		sv.u.svc.Log.Warn("library", "sync commit failed", map[string]any{"error": err.Error()})
		return
	}
	sv.u.svc.Log.Info("library", "collection synced", map[string]any{
		"added": res.Added, "updated": res.Updated, "removed": res.Removed, "total": res.Total})
	sv.extractArtwork(tracks)
}

// extractArtwork pre-populates the artwork DB for an imported collection in the background, so the
// overlay/Spout/PNG renderers read covers from the DB instead of probing files every tick. Skips
// tracks already analyzed (cheap DB hit). Single-flight + paced: a small per-track yield keeps the
// backfill's disk IO from starving on-demand extraction for a deck that just went on-air.
func (sv *studioView) extractArtwork(tracks []musiclib.Track) {
	res, db := sv.u.svc.OverlayArt, sv.u.svc.Lib
	if res == nil || db == nil || len(tracks) == 0 {
		return
	}
	if !sv.artExtractRun.CompareAndSwap(false, true) {
		return // a backfill is already running (import + load-from-db both call this)
	}
	debuglog.Go(sv.u.svc.Log, "art-extract", func() {
		defer sv.artExtractRun.Store(false)
		ctx := context.Background()
		stored, scanned := 0, 0
		for _, t := range tracks {
			if t.Path == "" {
				continue
			}
			scanned++
			if res.EnsurePath(ctx, t.Path, t.Artist, t.Title) {
				stored++
			}
			time.Sleep(15 * time.Millisecond) // yield disk IO so live deck-load extraction wins
		}
		withArt, total, _ := db.CountTrackArt()
		sv.u.svc.Log.Info("library", "artwork extracted", map[string]any{
			"scanned": scanned, "newlyStored": stored, "dbWithArt": withArt, "dbTotal": total})
	})
}

// loadFromDB hydrates the collection panes from the persisted library on launch, so the user
// sees their tracks immediately without re-importing. No-op if nothing's been imported yet.
func (sv *studioView) loadFromDB() {
	db := sv.u.svc.Lib
	if db == nil {
		return
	}
	src, ok, err := db.FirstSource()
	if err != nil || !ok {
		return
	}
	tracks, err := db.LoadTracks(src.ID)
	if err != nil || len(tracks) == 0 {
		return
	}
	byPath := make(map[string]musiclib.Track, len(tracks))
	for _, t := range tracks {
		byPath[t.Path] = t
	}
	sessions, _ := db.LoadSessions(src.ID)
	sums := make([]musiclib.SessionSummary, len(sessions))
	for i, s := range sessions {
		sums[i] = musiclib.Summarize(s)
	}
	fyne.Do(func() {
		sv.tracks, sv.byPath, sv.loaded = tracks, byPath, true
		if sv.collList != nil {
			sv.applyCollFilter()
			sv.collList.Refresh()
		}
		if len(sessions) > 0 {
			sv.sessions, sv.summaries = sessions, sums
			if sv.sessList != nil {
				sv.sessList.Refresh()
			}
		}
	})
	sv.u.svc.Log.Info("library", "loaded from db", map[string]any{
		"tracks": len(tracks), "sessions": len(sessions), "app": src.App, "version": src.Version})
	sv.extractArtwork(tracks) // backfill covers for any not-yet-analyzed tracks (skips analyzed)
}

// persistPlaylists replaces the source's imported playlists with a freshly parsed set
// (manual + smart untouched) and refreshes the Playlists pane. Best-effort, off-UI-thread.
func (sv *studioView) persistPlaylists(src musiclib.Source, pls []musiclib.Playlist) {
	db := sv.u.svc.Lib
	if db == nil {
		return
	}
	row, err := db.SourceByAppPath(src.App, src.Path)
	if err != nil || row.ID == 0 {
		return
	}
	if err := db.SyncImportedPlaylists(row.ID, pls); err != nil {
		sv.u.svc.Log.Warn("library", "persist playlists failed", map[string]any{"error": err.Error()})
		return
	}
	sv.u.svc.Log.Info("library", "playlists synced", map[string]any{"playlists": len(pls)})
	fyne.Do(sv.refreshPlaylists)
}

// persistSessions stores play-history under the named source (creating a bare source if the
// library wasn't imported). app is the DJ software ("traktor"/"rekordbox"); srcPath is the
// collection/db/export file the sessions came from. Best-effort.
func (sv *studioView) persistSessions(app, srcPath string, sessions []musiclib.Session) {
	db := sv.u.svc.Lib
	if db == nil || len(sessions) == 0 || srcPath == "" {
		return
	}
	row, err := db.SourceByAppPath(app, srcPath)
	if err != nil || row.ID == 0 {
		if row, err = db.UpsertSource(musiclib.Source{App: app, Path: srcPath}, fileMtimeUnix(srcPath)); err != nil {
			sv.u.svc.Log.Warn("library", "persist sessions: source", map[string]any{"error": err.Error()})
			return
		}
	}
	if err := db.SyncSessions(row.ID, sessions); err != nil {
		sv.u.svc.Log.Warn("library", "persist sessions failed", map[string]any{"error": err.Error()})
		return
	}
	sv.u.svc.Log.Info("library", "history synced", map[string]any{"sessions": len(sessions)})
}

// tagActionBar renders the "write DJ analysis into the file" + revert controls for a
// collection track. Write is gated on a supported format + the file being present; Revert
// shows only when a prior write is pending. Requires the library DB (revert history lives there).
func (sv *studioView) tagActionBar(t musiclib.Track, onDisk bool) fyne.CanvasObject {
	db := sv.u.svc.Lib
	if db == nil {
		return container.NewVBox()
	}
	supported := tagwrite.Supported(t.Path)
	write := newKitButtonWithIcon("Write tags → file", theme.DocumentSaveIcon(), func() { sv.doWriteTags(t) })
	write.SetVariant(kitBtnBrand)
	revert := newKitButtonWithIcon("Revert", theme.ContentUndoIcon(), func() { sv.doRevertTags(t) })
	if !onDisk || !supported {
		write.Disable()
	}
	if _, ok, _ := db.LatestTagEdit(t.Path); !ok {
		revert.Disable()
	}
	note := "Writes BPM / key / genre / comment into the file - revertible (snapshot kept in the library)."
	if !supported {
		note = "Tag-write supports MP3 + FLAC only (this is " + strings.ToLower(filepath.Ext(t.Path)) + ")."
	}
	return container.NewVBox(container.NewGridWithColumns(2, write, revert), mutedLabel(note))
}

// doWriteTags writes the track's analysis into its file (off the UI thread) + refreshes detail.
func (sv *studioView) doWriteTags(t musiclib.Track) {
	debuglog.Go(sv.u.svc.Log, "tag-write", func() {
		written, err := tagsync.Apply(sv.u.svc.Lib, t)
		if err != nil {
			if err == tagsync.ErrUnsupported {
				sv.u.Notify("rave-mate", "Tag-write supports MP3 + FLAC only.")
				return
			}
			sv.u.svc.Log.Warn("library", "tag write failed", map[string]any{"path": t.Path, "error": err.Error()})
			sv.u.Notify("rave-mate", "Tag write failed: "+err.Error())
			return
		}
		if len(written) == 0 {
			sv.u.Notify("rave-mate", "No analysis (BPM/key/genre) to write for this track.")
			return
		}
		sv.u.svc.Log.Info("library", "tags written", map[string]any{"path": t.Path, "fields": len(written)})
		sv.u.Notify("rave-mate", fmt.Sprintf("Wrote %d tags → %s (revertible).", len(written), filepath.Base(t.Path)))
		fyne.Do(func() { sv.selectTrack(t, true) }) // rebuild detail so Revert enables
	})
}

// doRevertTags restores the last tag write for the track's file.
func (sv *studioView) doRevertTags(t musiclib.Track) {
	debuglog.Go(sv.u.svc.Log, "tag-revert", func() {
		if err := tagsync.Revert(sv.u.svc.Lib, t.Path); err != nil {
			sv.u.Notify("rave-mate", "Revert failed: "+err.Error())
			return
		}
		sv.u.svc.Log.Info("library", "tags reverted", map[string]any{"path": t.Path})
		sv.u.Notify("rave-mate", "Reverted tag changes for "+filepath.Base(t.Path)+".")
		fyne.Do(func() { sv.selectTrack(t, true) })
	})
}

// fileMtimeUnix returns the file's mtime as unix seconds (0 if unstattable) - the refresh key.
func fileMtimeUnix(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().Unix()
}
