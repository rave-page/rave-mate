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
