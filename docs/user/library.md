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

## Remote library (paired instance)

The whole tab works against a paired instance: Controlling switcher → pick the peer. Its own
rendered Library streams here and every action executes over there - see
[multi-pc.md](multi-pc.md#remote-library).

## Collection filters

Collection combines free-text search (title/artist) with facet dropdowns - **Genre**,
**Label**, **Playlist** (membership in any picked playlist, smart playlists evaluate
live), plus the key wheel and the NO DROPS chip. Facets are filterable dropdowns; active
picks show as removable chips and combine (search AND genre AND playlist …).

Playlists are integrated here: filter to exactly one playlist and its full action row
(prepare cues + export M3U as buttons; rename, re-encode, refresh, sync, delete in the
**⋯ More** menu) appears inline - Collection *is* the playlist view. A track's playlist chips in the detail rail jump straight to
that filtered view; a Browse folder bound to a playlist shows the same actions in place.
New playlists live under **Maintenance**. The Playlists tab remains for ordering/reorder.

## Layout

Collection and Browse use a DJ-software tri-pane layout: a narrow **left rail**
(Collection = All tracks + playlist tree; Browse = places, pinned, drives, folders),
the track/file list, and the detail inspector. Drag the dividers to resize; widths
persist across restarts.

## Folder-playlist refresh

DJ software never re-scans a "folder as playlist" - files added later stay missing.
Any file-backed playlist's **⋯ More** menu has **Refresh from folder** (uses the stored
folder binding, or the members' dominant directory for Traktor imports) and an
**Auto-refresh** toggle (applied once per app run). **Maintenance → Refresh folder
playlists…** sweeps them all. One sweep runs at a time - a toast confirms the start,
another click while it runs is a no-op.

## DJ-software version upgrades

A version upgrade (e.g. Traktor 4.2 → 4.5) moves the collection file, so the next
import would land beside the old one and double every track + playlist. rave-mate
retires the superseded import automatically - after each import and once at startup -
whenever the new collection covers the old one's paths. A genuinely different library
(another PC's collection) is never touched.

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

## AI beatgrid fixer

**Collection → Fix beatgrids** (or the Beatgrid health card in the right rail): the
Beat This! neural model detects each track's true beats, a constant grid is fitted,
and off/missing grid markers are snapped or created — with backup-first Apply into
Traktor, Rekordbox (XML), VirtualDJ and Serato. Tracks it can't fix confidently go to
a `MANUAL_GRIDDING_PREP` playlist instead of being guessed at. Mark trusted grids as
**grid verified** to calibrate the detector's per-format bias and to fine-tune your
own model. Engine install (CPU or CUDA), calibration, training and per-software
caveats: see `docs/BEATGRID_FIXER.md`.

## Batch re-encode (duplicate folder / playlist)

The everyday "FLAC library → CDJ-safe USB copy" move: **Browse toolbar → 📁 Folder →
Re-encode folder…** (recursive, subfolders mirrored) or **playlist ⋯ More → Re-encode
playlist…**.
Pick an audio preset + destination folder (defaults to `<source>-<preset>`); file names
are kept, already-present outputs skipped, originals never touched. Jobs land in the
Queue section.

Because of this flow, the per-file encoder panel folds away when a selected audio file
sits in a collection/playlist context (or a folder marked as a playlist — **Browse
toolbar → 📁 Folder → Mark folder as playlist**); *Show encoder for this file* brings
it back for
one-offs. Set recordings and video keep the full panel up front.

## Works-together marks (track compatibility)

Mark tracks that work well together - three kinds: **Blend** (mix smoothly), **Double
drop** (drops align), **Energy match** (same energy level). Select 2+ tracks in
Collection or Browse, then right-click a selected row (or use **Works together…** in
the selection bar) and pick the kind - every pair among the selection is marked
(symmetric, stored once per pair; marking caps at 100 tracks per action).

A selected track's detail rail shows its **Works well together** partners;
**Find compatible…** (also on the right-click menu) opens discovery: direct partners
plus friends-of-friends (depth 2, "via <track>", capped at 200) - built for set
building. ✕ on a direct partner removes that pair's marks.

Marks can also be made straight from a recorded set's tracklist on the **Publish**
tab (see `recording-and-tracklists.md`) - same store, so Collection discovery, smart
playlists and the set builder see them immediately.

### Smart-playlist compat rule

The smart-playlist editor has a **Compatible with track** rule: pick an anchor track
(filterable picker) and the playlist matches everything marked works-together with it
(the anchor is included). **Include friends-of-friends** widens it to depth 2 - tracks
compatible with the direct partners. Combines (AND) with the other rules, so
"compatible with X, 138-150 BPM, ≥4★" is one playlist.

## Sorted-copy set builder

Any playlist's **⋯ More** menu has **Sorted copy…**: builds a NEW playlist (original never
modified) grouped by works-together clusters, energy (BPM bands), key (harmonic
Camelot order), last played, date added, or release date.

Optional **divider tracks** between groups: short quiet noise clips (generated once
per name style via ffmpeg, reused after) titled `............`, `---------------`, or
`________________` - group boundaries stay visible in any DJ software after
export/sync. If ffmpeg is unavailable the copy is created without dividers.

Dividers are flagged rows (`is_divider`) that exist **only inside playlists**: they
never appear in the Collection view, never upload to rave.page (library bulk sync,
media sync, cloud playlist push all skip them), and are never fingerprint/enrichment
or cross-software merge candidates. Playlist M3U export and folder-based flows keep
them - that visibility in your DJ software is their purpose.

## Tag fixing & editing

**Collection → Maintenance → Fix tags…** scans files for broken/incomplete tags
(ID3v1-only, mojibake, missing/mismatched fields vs the library) with selectable
per-field repairs; the detail rail's **Edit tags…** edits a single file's tag
directly. Both write atomically, journaled + revertible. See `docs/TAG_TOOLS.md`.

## Caveats

- Rekordbox database formats are version-sensitive; sync is read-heavy and conservative on
  write.
- ffmpeg/fpcalc are auto-detected from PATH or installable via Settings → Media tools.
