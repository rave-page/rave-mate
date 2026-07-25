# Folder import: add a folder to the collection + beatgrid, no DJ software

Prepare NEW music entirely inside rave-mate: pick a folder → tracks land in the
collection (synthetic source) + a folder-bound playlist → optionally beatgrid right away,
grids saved into libdb. Traktor only needed if the user explicitly exports.

## Flow

1. **Entry points**: Browse folder-menu "Add folder to collection…", folder right-click
   modal, Import-library modal ("Add a folder of audio files…"), all → `fiOpen`
   (`webui/library_folderimport.go`).
2. **Modal**: folder field + pick-dir, "Include subfolders" toggle, async scan
   (`fiScanDir`, non-recursive mirrors libMarkDirPlaylist; recursive WalkDir skips
   dot-dirs; bounded `fiMaxFiles`=25000, scan stops + modal says so). Action-bound
   buttons state the outcome: "Import N tracks" / "Import N tracks + beatgrid" (second
   only when GridFix enabled).
3. **Import** (`fiRun`): libJob in the queue view; per-file `probe.tags` via the
   out-of-process probe worker (ffprobe + dhowden/tag; 2 min/file timeout, cancellable);
   fallback `fiSplitName` "Artist - Title" filename split (leading track number stripped
   only when a `./-/_` separator follows, so "2 Unlimited" survives). Persist =
   `EnsureSource("folder", dir)` (NO imported_at → never becomes FirstSource for
   remotectl/Fyne) + `BeginTrackSync` (re-import of the same dir refresh-syncs: vanished
   files drop) + folder playlist create-or-top-up. Collection view uses LoadAllTracks →
   folder tracks appear everywhere.
4. **Beatgrid**: `gfRunTracksHook` (new onDone param on the cockpit runner, nil for old
   callers) runs the normal batch (force=false) → `fiSaveGrids` writes FIX plans straight
   into libdb via new `libdb.UpdateTrackGrid` (bpm + single marker, journals both to
   change_log origin "gridfix", bumps tracksVer). Skipped when the run was cancelled.
5. **Folder-playlist refresh** (`library_plrefresh.go`): new files picked up by
   manual/auto refresh also get collection rows — `fiPersistLoose`: only paths in NO
   source, probed, additive `BeginTrackUpsert` (Commit never deletes). No auto-beatgrid
   on refresh (no hidden side effects; startup auto-refresh must not load the model).
6. **Send to Traktor** (`libPlSendTraktor`, menu item on manual folder-bound playlists):
   Traktor-running guard → full collection backup → `MergeIntoCollectionFile` (new
   ENTRYs incl. TEMPO + CUE_V2 from libdb grids/cues; existing entries' managed fields
   refreshed) → `UpsertNMLPlaylist` (after the merge — it skips paths without a
   COLLECTION entry) → toast added/updated/playlist counts. Divider rows filtered.

## Plumbing

- `libdb/tracks.go`: `TrackSync.additive` + `BeginTrackUpsert` (no delete on Commit).
- `libdb/gridsave.go`: `HasTrackPath`, `UpdateTrackGrid`.
- `libsync/merge.go`: `defaultAppOrder` += "folder" (last — tag metadata never beats a
  real DJ software in field merges, still fills gaps).
- `webui/library_djsync.go`: `djAppLabel("folder")` → localized "Folder".
- Source.App doc comment: + "folder".

## i18n

`library.fi.*` + `library.pl.sendTraktor*` (+ menu.sendTraktorSub) in all 7 locales
(en/de/es/fr/ja/ru/uk), CLDR one/few/many/other plurals.

## Tests

`libdb/gridsave_test.go` (additive upsert, FirstSource non-promotion, HasTrackPath,
UpdateTrackGrid), `libsync/merge_test.go` (folder ranks last), `webui/
library_folderimport_test.go` (fiSplitName, fiScanDir, send-to-Traktor gating).
`GOWORK=off go build/vet/test ./...` clean.

## Leftovers

- Live ctl verification (modal → import → beatgrid → send-to-Traktor) pending post-merge.
- Refresh top-up probes serially in the refresh goroutine; fine for the "few new files"
  case it targets.
- Beatgrid-on-refresh intentionally omitted (see 5) — revisit if users want a stored
  per-playlist choice.
