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
(default 12 ms), calibrated detector bias (below).

## Detector calibration (Library → Collection)

The neural detector carries a small systematic phase offset against your DJ software's
grids that **differs per file format** (decoder delay: an MP3 decoder pads the start,
FLAC doesn't, etc. — typically tens of ms). Uncalibrated, every "fix" would shift
correct grids by that bias.

1. Mark tracks whose grid you KNOW is right as **grid verified** (a handful per file
   type you use — the same marks feed model training).
2. Press **Calibrate detector** in the Beatgrid health card. rave-mate samples the
   verified tracks evenly per extension, measures each track's detector-vs-grid offset
   (only trusts tracks with a stable-tempo fit whose BPM matches the stored one), and
   stores the per-format median.
3. The measured bias (shown in the health card and the settings card, e.g.
   `.mp3 +42.7 ms · .flac −6.8 ms · * −2.9 ms`) is subtracted from every planned fix;
   `*` covers formats you didn't calibrate.

Re-calibrate after switching the active model — a fine-tuned checkpoint can shift the
detector's phase. Analysis results are cached, so re-runs are quick.

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

## BPM target ranges (Library → Collection → Maintenance)

Half/double-time detections (a 174 DnB track analyzed and stored as 87) are fixed
systemically with per-playlist and per-genre target bands:

- **Rules.** Maintenance → "BPM target ranges…" stores per-genre bands (exact genre
  or genre family, e.g. `drum & bass` 160–190). Any non-smart playlist gets its own
  band via its ⋯ menu → "BPM range…" — playlist rules beat genre rules; the
  narrowest band wins when several apply.
- **Fold, never re-grid.** Out-of-band BPMs fold in by ×2/÷2 only: doubling adds
  beats between existing ones, halving keeps the anchor beat — grid markers and
  time-based cues never move, in any DJ software.
- **Analysis.** The fixer's octave choice folds both the stored-BPM prior and the
  fitted grid into the band, so a run on a ruled track lands at 174 even when the
  tag says 87 (a band outside 90–180 also unlocks tempos the default normalization
  would never pick).
- **Enforcement.** "Scan collection" lists out-of-band tracks (with the unfixable
  remainder where no power of 2 lands inside a too-narrow band); "Fix n tracks"
  folds them in the library (journaled in change_log, revertible), writes every
  detected DJ-software collection (backup first; grid locks are written through) and
  the files' own BPM tags. Multi-marker manual grids fold library+tags only — their
  marker lists are never collapsed.
- **Sync.** Cross-software library sync folds canonical tracks through the same
  rules, so every export/writeback target receives in-band tempos.
