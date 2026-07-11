# Cue Editor (drops & cue patterns)

Beat-precise hotcue / memory-cue editing on the library waveform, built around **cue
patterns**: DJs usually cue the same way on every track — one cue at the drop, then
cues at fixed beat distances around it. Mark the drops once, save your pattern once,
apply it to any number of prepared tracks.

## Opening

Library → select a track with a beatgrid → **Prepare cues** (the button shows in
Collection, Browse and Playlists once the track is in the collection). Whole-set entry:
**Prepare cues (playlist)** in a playlist's action row, or **Prepare cues (folder)** in
the Browse toolbar - eligible tracks (in collection + beatgrid) become the mass-apply
selection, the collection focuses on that playlist, and the editor opens on the first
track; tracks without a grid are skipped with a count. The waveform expands to full tab
width above the browser with the beatgrid, cue flags, drop markers and beat cursor
rendered on it. The strip above the wave shows track identity, cursor position (time +
bar.beat), beat-jump size, each drop (click = jump) and the cue count; the right rail
holds the actions.

## Marking

| Input | Action |
|---|---|
| Left-click wave | move beat cursor (snaps to nearest beat) |
| Right-click | add **memory cue** at that beat |
| Shift+right-click | add **drop marker** |
| Ctrl+right-click | remove cue + drop markers at that beat |
| Drag | rubber-band-select cues (for saving a pattern) |
| ← / → | walk beats · Shift = beat-jump (size: Shift+↑/↓, 1–64) |
| T / Enter | add drop at cursor · +Shift removes |
| Hold Space | audition from cursor (release stops) |
| Ctrl+← / → | nudge the whole beatgrid 10 ms (Ctrl+Shift: 1 ms) — **cues and drops move with the grid** |

Keys work only while the Library tab has focus and no input field is active.

Numbers along the top of the waveform show the **beat distance between neighbouring
markers** (cues + drops); flags at the bottom carry the hotcue pad number (or `M` for
memory cues) — hover for name + time.

## Drops: stored twice

Drop markers are a rave-mate enrichment: they live in the library database (keyed by
file path — they survive collection re-imports) **and** in the file itself
(`RAVEMATE_DROPS` ID3 TXXX frame / FLAC Vorbis comment), so they travel with your
music. Formats without tag support show a ⚠ in the strip (database-only). Every
change is journaled in the change log.

The **NO DROPS** filter chip (collection toolbar) lists unprepared tracks; the CUES
column shows each track's census — ◆n drop markers, ⚑n cues.

## Patterns

1. On a track that already has your cues: drag-select them, name the pattern, **Save
   as pattern**. Offsets are stored in beats relative to the nearest drop (or the
   cursor).
2. On a prepared track: assign a pattern per drop (they can differ), then **Apply
   patterns (hotcues)** or **Apply as memory cues**. Cues that don't fit the span
   (track start ↔ drop 1, previous drop ↔ drop N) are cut — the drop is always the
   anchor. Occupied pad slots are reallocated (or the cue is demoted to a memory cue
   when none is free); duplicates are skipped.
3. **Mass apply**: tick tracks in the list (rows highlight amber) and use the same
   two buttons in the selection bar — every checked track's own drops anchor the
   assigned patterns; tracks without drops or grid are skipped and counted.

**Convert all hotcues → memory cues** demotes a track's pads in one step (positions +
names kept) for software that supports memory cues.

## Write cues to your DJ software

Cue edits persist to the rave-mate library (and drops to the file tag) immediately.
Pushing them into your DJ software is a separate, explicit step: the rail's **Write cues
to DJ software** section detects installed libraries and shows one **Write cues to
{app} (n)** button per target — the same backup-first router the beatgrid fixer uses.

Scope: the checked collection rows (the mass-apply selection), or just the open track
when nothing is checked. Only tracks with at least one musical cue are written; each
write **replaces** that software's hotcues/memory cues/loops for those tracks with the
cues shown in rave-mate. Beatgrids are never touched (that's the [beatgrid
fixer](BEATGRID_FIXER.md)'s job), and drop markers are **not** exported — they only
become cues via an applied pattern.

Per software:

| Target | Written to | Notes |
|---|---|---|
| **Traktor** | `collection.nml` (backup first) | non-grid `CUE_V2` replaced; grid cues + TEMPO untouched. Restart Traktor to pick it up. |
| **Rekordbox** | exported collection XML (backup first) | `POSITION_MARK` hotcues (Num ≥ 0) + memory cues (Num = -1) + loops; import via **File → Import Collection**. The live `master.db` is deliberately not written — cues there are tied to ANLZ analysis data and a partial write would desync the library. |
| **VirtualDJ** | `database.xml` (backup first) | hotcues → `<Poi Type="cue" Num="1..8">`, memory cues → remix points (`Type="remix"`), loops carry `Size` in beats. Refused while VirtualDJ runs (it rewrites the database on exit). |
| **Serato** | the audio files ("Serato Markers2" tag, MP3 GEOB / FLAC vorbis) | verified temp-write per file, refused while Serato runs; a stale legacy `Serato Markers_` tag is removed (it would shadow the new cues). Memory cues have no Serato equivalent and are skipped — pad cues + saved loops only. MP4/AIFF/Ogg/WAV are left alone. |

Library-file targets are backed up into the standard backup folder before every write.
After a write the button turns into a "written" note; changing any cue re-arms it.
