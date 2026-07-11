# Beatgrid Fixer (AI)

Neural beatgrid correction for your DJ collection: rave-mate detects the true beats of
each track with the [Beat This!](https://github.com/CPJKU/beat_this) model, fits a
constant grid, and snaps (or creates) the grid marker in your DJ library — Traktor,
Rekordbox, VirtualDJ and Serato are all supported write targets. Tracks it can't fix
confidently go to a `MANUAL_GRIDDING_PREP` playlist (Traktor) instead of being guessed at.

## Setup (Settings → Library & media → Beatgrid Fixer)

1. Install Python 3.10–3.14 (python.org or Microsoft Store) if you don't have it.
2. Install an engine — CPU and CUDA are independent installs, in any order, and can
   coexist. Each creates rave-mate's own Python environment (pinned `beat-this` +
   PyTorch); your system Python is not touched.
   - **Install CPU engine** — works everywhere.
   - **Install CUDA engine** — a several-GB PyTorch build, many times faster analysis
     and training. Needs an NVIDIA GPU (the button is greyed out with a hint when no
     NVIDIA driver is detected).
3. **Engine used for analysis & training** picks which one runs: Auto (CUDA when
   installed & working, else CPU), CPU, or CUDA.
4. Enable the feature toggle.

## Fixing grids (Library → Collection)

**Fix beatgrids** in the toolbar (or the Beatgrid health card in the right rail) →
pick scope (whole collection / filtered / selected) → watch the live FIX / OK /
MANUAL / ERROR counters. Analysis is read-only and cached (unchanged files are never
re-analyzed); the tray tooltip mirrors progress.

When done: one **Apply** button appears per DJ library detected on your machine —
each backs up its target first (except Serato, whose per-file writes verify themselves),
then rewrites only the grid marker, BPM and (optionally, see settings) the lock flag of
FIX tracks. Hotcues and loops are never touched. Close the DJ software before applying.
Per software:

- **Traktor** — writes `collection.nml` in place; Traktor re-reads it on start.
  **Send to prep playlist** collects MANUAL tracks for hand-gridding.
- **Rekordbox** — writes the exported collection XML; load it back via
  *File → Import Collection*. The native `master.db` is deliberately not touched:
  Rekordbox keeps grids in binary per-track analysis files, and a partial write
  would desync them.
- **VirtualDJ** — writes `database.xml` in place (refused while VirtualDJ runs,
  since VDJ rewrites the file from memory on exit).
- **Serato** — writes the grid into each audio file's Serato tag (see Notes).

The import dialog auto-detects all four libraries too (Serato import also reads each
file's grid), so you can import, fix and write back whichever software you use.

Quality knobs (settings): min grid coverage (default 0.85), minimum correction size
(default 12 ms), calibrated detector bias.

## Training your own model (Settings → Beatgrid Model)

Mark tracks whose grid you KNOW is right as **grid verified** (track detail button or
the selection bar in Collection). **Train model** fine-tunes the beat tracker on them
in rave-mate's environment; a held-out validation split reports beat accuracy before
vs after, so a bad run is simply discarded. The active model only changes when you
pick a checkpoint. Models learn most from tracks the fixer got wrong — hand-grid
tracks from `MANUAL_GRIDDING_PREP`, verify them, then train (20+ recommended).

## Notes

- Detection results live in `gridfix/analysis_cache.json` in the config dir (keyed by
  file path+size+mtime; capped at 100k entries).
- The write path normalizes XML self-closing tags (`<TEMPO/>` → `<TEMPO></TEMPO>`);
  Traktor reads both and re-canonicalizes the file on its next save.
- **Serato** grid writeback is supported: Serato stores beatgrids in the audio files
  themselves (MP3 GEOB tag / FLAC comment), so fixes are written per file — atomic
  temp+verify+rename, all other tags preserved byte-exact. Close Serato before applying.
  MP4/M4A, AIFF and Ogg files are skipped (unsafe to rewrite). Serato also appears as a
  library-sync writeback target (constant grids only; variable grids are never collapsed).
- Library sync (Settings → Library sync) can also write grids: VirtualDJ has file +
  live writeback modes, Serato a per-file writeback target, Rekordbox XML export.
