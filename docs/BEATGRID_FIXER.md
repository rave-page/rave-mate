# Beatgrid Fixer (AI)

Neural beatgrid correction for your DJ collection: rave-mate detects the true beats of
each track with the [Beat This!](https://github.com/CPJKU/beat_this) model, fits a
constant grid, and snaps (or creates) the grid marker in your Traktor collection.
Tracks it can't fix confidently go to a `MANUAL_GRIDDING_PREP` playlist instead of
being guessed at.

## Setup (Settings → Library & media → Beatgrid Fixer)

1. Install Python 3.10–3.14 (python.org or Microsoft Store) if you don't have it.
2. **Install beat engine** — creates rave-mate's own Python environment (pinned
   `beat-this` + CPU PyTorch). Your system Python is not touched.
3. NVIDIA GPU? **Install CUDA acceleration** appears — a several-GB PyTorch build that
   makes analysis many times faster. The **Use GPU** toggle shows once it's verified
   installed.
4. Enable the feature toggle.

## Fixing grids (Library → Collection)

**Fix beatgrids** in the toolbar (or the Beatgrid health card in the right rail) →
pick scope (whole collection / filtered / selected) → watch the live FIX / OK /
MANUAL / ERROR counters. Analysis is read-only and cached (unchanged files are never
re-analyzed); the tray tooltip mirrors progress.

When done: **Apply** backs up your collection, then rewrites only the grid marker,
BPM and (optionally, see settings) the lock flag of FIX tracks — hotcues and loops
are never touched. **Send to prep playlist** collects MANUAL tracks for hand-gridding.
Close Traktor before applying; Traktor re-reads the collection on start.

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
- Rekordbox / VirtualDJ grid writeback: planned (analysis already works for any files).
