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

## Caveats

- Rekordbox database formats are version-sensitive; sync is read-heavy and conservative on
  write.
- ffmpeg/fpcalc are auto-detected from PATH or installable via Settings → Media tools.
