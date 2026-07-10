# Library

The Library tab is a native browser + relational store (SQLite) for your DJ media.

## What you get

- **Browse**: fast file browser with metadata, cover art, waveform preview, in-app player
  (mpv-backed on Windows; trims/preview without opening a DAW).
- **Collection**: tracks with play counts, ratings, last-played - fed live by the recorder.
- **Playlists**: build/edit, sync to backends.
- **Transcode**: ffmpeg worker pool with presets (custom presets supported); heavy jobs run in
  isolated subprocesses.
- **Tag write-back**: fix metadata and write it into the files.
- **Change log**: every mutation (plays/ratings/metadata) is append-only recorded - the basis
  for cross-machine merge and rollback.

## Collection filters

Collection combines free-text search (title/artist) with facet dropdowns - **Genre**,
**Label**, **Playlist** (membership in any picked playlist, smart playlists evaluate
live), plus the key wheel and the NO DROPS chip. Facets are filterable dropdowns; active
picks show as removable chips and combine (search AND genre AND playlist …).

Playlists are integrated here: filter to exactly one playlist and its full action row
(prepare cues, export M3U, re-encode, rename, sync, delete) appears inline - Collection
*is* the playlist view. A track's playlist chips in the detail rail jump straight to
that filtered view; a Browse folder bound to a playlist shows the same actions in place.
New playlists live under **Maintenance**. The Playlists tab remains for ordering/reorder.

## Layout

Collection and Browse use a DJ-software tri-pane layout: a narrow **left rail**
(Collection = All tracks + playlist tree; Browse = places, pinned, drives, folders),
the track/file list, and the detail inspector. Drag the dividers to resize; widths
persist across restarts.

## Folder-playlist refresh

DJ software never re-scans a "folder as playlist" - files added later stay missing.
Any file-backed playlist's action row has **Refresh from folder** (uses the stored
folder binding, or the members' dominant directory for Traktor imports) and an
**Auto-refresh** toggle (applied once per app run). **Maintenance → Refresh folder
playlists…** sweeps them all.

## Multi-select

Both Collection and Browse: header **select-all** checkbox (applies to the filtered
set), **Shift+click** selects a consecutive range (on a checkbox, the range follows the
checkbox's new state - so Shift also deselects), **Ctrl+click** adds/removes individual
rows.

## Cross-DJ-software library sync

Hub-merge model: rave-mate reads Traktor / Serato / VirtualDJ / Rekordbox collections, merges
into its own library, and writes back to configured targets - keep cue data and playlists
consistent across software. Configure sync jobs in Settings; an auto-sync scheduler re-runs
them. **Back up your collections before first sync** (the app also keeps its own backups of
touched files).

## Batch re-encode (duplicate folder / playlist)

The everyday "FLAC library → CDJ-safe USB copy" move: **Browse toolbar → Re-encode
folder…** (recursive, subfolders mirrored) or **playlist view → Re-encode playlist…**.
Pick an audio preset + destination folder (defaults to `<source>-<preset>`); file names
are kept, already-present outputs skipped, originals never touched. Jobs land in the
Queue section.

Because of this flow, the per-file encoder panel folds away when a selected audio file
sits in a collection/playlist context (or a folder marked as a playlist — **Browse
toolbar → Mark folder as playlist**); *Show encoder for this file* brings it back for
one-offs. Set recordings and video keep the full panel up front.

## Tag fixing & editing

**Collection → Maintenance → Fix tags…** scans files for broken/incomplete tags
(ID3v1-only, mojibake, missing/mismatched fields vs the library) with selectable
per-field repairs; the detail rail's **Edit tags…** edits a single file's tag
directly. Both write atomically, journaled + revertible. See `docs/TAG_TOOLS.md`.

## Caveats

- Rekordbox database formats are version-sensitive; sync is read-heavy and conservative on
  write.
- ffmpeg/fpcalc are auto-detected from PATH or installable via Settings → Media tools.
