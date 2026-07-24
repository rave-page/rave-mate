# BPM target ranges (2026-07-24)

User pain: set played with tracks analyzed at 87 that are 174 DnB — half-time octave
error, propagated into DJ software on sync even with locked tags. Fix: per-playlist +
per-genre BPM target bands enforced at analysis, in the collection/files, and on sync.

## Model

- **Fold = ×2/÷2 only** (`musiclib.FoldBPM`/`FoldTrack`, bpmrange.go): double while
  below band, halve while above; unfoldable when band narrower than an octave and no
  power of 2 lands inside. Doubling keeps every beat (new ones between), halving keeps
  the anchor beat → grid marker positions + time-based cues NEVER move (true for NML
  CUE_V2 ms, RB XML sec, VDJ Poi sec, Serato marker).
- **Rules in libdb** (bpmrules.go): `playlists.bpm_min/bpm_max` columns (additive
  migration) + `bpm_genre_rules` table (key = lower(genre) or lower(GenreFamily)).
  `LoadBPMRules()` → `Resolve(path, genre)`: playlist (narrowest wins) > exact genre >
  family. Smart playlists excluded (no membership rows; they filter by BPM anyway).

## Enforcement points

1. **Analysis** (gridfix): `BatchTrack.RangeLo/Hi` + `PlanInput.RangeLo/Hi`;
   Batch.Run folds the stored-BPM prior before `FitConstantGrid` (candidate seeding),
   `ChooseOctaveRange` folds the legacy `ChooseOctave` result into the band
   (replaces the hardcoded 90–180 normalization when a band is set). PlanFix compares
   snap-trust vs the folded prior but bpmChanged vs the RAW stored BPM → forces a FIX
   write even when the marker is already aligned. webui `gfRunTracks` resolves per
   track. Regression test asserts legacy path still keeps the stored octave.
2. **Collection + files** (webui library_bpmrange.go): Maintenance → "BPM target
   ranges…" modal = genre rules CRUD + scan + apply. Apply: `UpdateTrackBPMFold`
   (bpm+beatgrid, change_log origin=bpmrange, revertible) + `tagsync.Apply` (TBPM) +
   `gfApplyTo` per detected target for single-marker grids (multi-marker manual grids
   = library/tags only, never collapsed). Locks written through (writers stamp LOCK,
   never guard on it). Playlist band via playlist ⋯ menu → "BPM range…".
3. **Sync** (libsync engine.go): post-`MergeCanonical` fold via `resolveBPMRange`
   (any cand path, then canonical genre); `Result.BPMFolded` in summary.

## Verified live (isolated rig)

RAVE_MATE_CONFIG_DIR + RAVE_MATE_CTL_ADDR=127.0.0.1:47629, synthetic 87-BPM FLAC
(genre "Drum & Bass", grid marker @250ms, hotcue @250ms) seeded into
rave-mate-library.db. Genre rule 160–190 via modal → scan "1 out of range · 87 → 174"
→ apply → DB bpm=174, marker {250ms, 174} (position UNCHANGED), cues untouched,
change_log 87→174, FLAC tag BPM=174 + Serato beatgrid GEOB. Playlist modal saved
160–190 → menu label "BPM range… (160–190)".

## Gotchas

- `gfTargets()`/`DetectTargets` AUTO-DISCOVERS real DJ installs — the rig's apply
  rewrote the REAL collection.nml (0 matches = semantic no-op, backup taken,
  parses clean, 21474 entries intact). Isolated rigs that must not touch real
  targets need `Features.NML.CollectionPath` pointed at a copy — detection has no
  other off switch.
- Hand-seeded tracks rows: text columns must be '' not NULL — `loadAllTracksUncached`
  scan errors on NULL album and LoadAllTracks returns 0 silently (UI shows empty
  collection).
- ctl `set` matches the FIRST `[data-label]` in DOM order (page before modal) and
  the query is single-word (splits on first space) — drive modal fields by a unique
  word of their label ("family", "min", "max").
- webui remote-host teardown test (`TestHostCloseTearsDown`) is flaky under full
  parallel `go test ./internal/...` load; passes isolated ×3.

## Follow-ups

- Automations could run the enforcement scan on schedule.
- Rekordbox master.db grids live in ANLZ binaries — RB writeback stays XML-level.
- Verified-grid store entries keep pre-fold BPM; batch skips Verified regardless.
