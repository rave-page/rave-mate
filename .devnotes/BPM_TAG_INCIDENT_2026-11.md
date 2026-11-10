# BPM tag / grid-lock incident (diagnosed 2026-11-10)

Symptom reported: "the beatgrid analyzer got tempos wrong, wrote them to files; Traktor
batch re-analysis doesn't fix them, single-track analysis does".

## What the data said (Heat DnB, 1,572 tracks; libdb tag_edits + change_log, NML backups,
rave-mate detection cache, original Beatport tags)

- Three-way vote on 1,602 cached tracks (fix_grids `fit_constant_grid` on cached
  Beat This! beats vs Traktor NML vs original Beatport integer tag): 1,229 all agree,
  250 NML=detector (no Beatport tag), 110 low coverage, **4 NML wrong** - and all 4 are
  Traktor 3:4 ratio errors (130.5 for 174, 229.3 for 172) produced by Traktor's own batch
  re-analysis on 2026-10-30, not by rave-mate. Detector verdict: keep Beat This!.
- rave-mate's 2026-09-02 apply changed 34 BPMs ≥0.3 (all 173→174-class truncation
  repairs, consistent with the vote) and moved 90 markers.
- rave-mate's real damage: **2026-06-17 tagsync wrote Traktor float grid BPMs
  (`173.999512`) into 1,355 FLAC `BPM` tags** over clean integers. Traktor truncates such
  tags on re-read → `173` → off-by-one grids that gridfix later had to repair.
- The off-by-one mechanism, proven on the 45 tracks that went 172→171 / 174→173 between
  the Aug-10 and Sep-2 collection backups: every cached detection of those tracks fits
  the TRUE tempo (172.00x) in Python, in the current Go planner and in the Aug-era Go
  planner (ddcf02c), with the true or a poisoned prior - 0 of 45 reproduce a 171. The
  entries that changed did so with the grid marker untouched and `MODIFIED_DATE` reset
  to `2026/6/27` (the file's mtime from the tag write): Traktor re-read the file tags of
  files newer than the entry and took the BPM from the tag. The AI beat tracker is not
  in that path at all.
- 2026-10-30 22:09:42: all 1,566 unlocked Heat DnB entries got `LOCK="1"` in one batch
  inside Traktor (no rave-mate NML backup/import after 09-02). Locked entries are skipped
  by Traktor's batch Analyze unless "Replace Locked Values" → "batch won't fix, single
  does".
- verified.json: 2 pins stale (173 pinned, collection 174 after Traktor re-analyzed).

## Fixes

- `traktor-grid-fix/clean_bpm_state.py` (workspace tool): revert tags from tag_edits
  snapshots (936 files reverted 2026-11-10, mtime preserved), unlock the 10-30 batch
  (1,566 entries, backup `collection.nml.bak.20261110-145034`), prune stale pins.
- rave-mate: `tagwrite.FormatBPMTag` + integer `TBPM` + numeric no-op guard in tagsync;
  verified pins go stale when the collection grid changed (`VerifiedGrid.Stale`,
  `gfPinState`), applying a FIX drops the pin.
