# Traktor write damage: forensics + fixes (2026-08-08)

User report: "cues not set in the right order (pads), lots of beatgrids
misaligned, weird Traktor behavior." Root-caused against the real library
(21,557-entry collection, backup timeline Jun 15 → Jul 24) + NML semantics
research over real TP4 collections (8,211-entry sample).

## Damage classes found (all in the live collection)

| class | count | origin |
|---|---|---|
| pads out of track-time order | 161 entries | pre-Jul-18 pattern applies numbered slots per-drop (pads 0-2 landed on drop 2, 3-5 on drop 1) |
| stacked duplicate cues | ~19 | same cue written onto two slots (slot 7 repeating slot 3's position) |
| grid markers 3-20 ms off the pre-gridfix lattice | 260 | uncalibrated detector bias: BiasS=0 + default 12 ms threshold sits ON the bias; BPM-snap (`!bpmChanged`) bypassed the threshold entirely. Bias flipped sign between checkpoints → markers ping-ponged (1370.9 → 1384.0 → 1371.5) |
| padded TYPE-4 anchors | 0 live (Traktor strips them) | old GridAnchor single-cue form; real TP4 data: 9196/9196 TYPE-4 cues are pad-less — grid + hotcue are always TWO separate CUE_V2s |

Latent (code-only, librarySync disabled for this user): `nmlCues` emitted grid
markers from BOTH `t.Beatgrid` and the `CueGrid` cues in `t.Cues` → 2^n grid
duplication per sync round-trip; importer ignored the per-marker `GRID` child
BPM → TP4 flexible grids flattened to the entry TEMPO; merge writeback dropped
TEMPO (losing `BPM_QUALITY`) and wiped grids of cue-less tracks.

## NML semantics (verified at scale, worth keeping)

- `HOTCUE` 0-indexed slot, −1 = no pad; pads follow HOTCUE only. `DISPL_ORDER`
  always 0 (carries nothing). File order = ascending `START`.
- TYPE-4 is never on a pad in any Traktor-written file. Native anchor-on-pad-1
  = TYPE-4 grid cue + white TYPE-0 companion at the same position (~64% of
  entries). Never dedupe across kinds.
- Flexible grids (TP4) = several TYPE-4s, each with own `<GRID BPM>` child; the
  GRID child is the only reliable v20 discriminator. `TEMPO@BPM` == first
  `GRID@BPM` exactly (8193/8193).
- Traktor never reloads a live collection.nml edit and overwrites from memory
  on save — the running-Traktor refusal is load-bearing.

## Fixes (commits ddcf02c, c8f8fba, 62e4275 on development)

1. gridfix: `PlanInput.PreservePhase` (BPM-only snap keeps the marker) +
   `Batch.RunAutoBias` per-run self-calibration (median offset of
   tempo-agreeing tracks, cache-served replan). Golden Python parity kept via
   the flag.
2. cue writeback: GridAnchor → native two-cue form, anchor placed on the
   EXISTING lattice's point nearest the earliest hotcue (phase preserved),
   multi-grid entries never re-anchor, passthrough grid cues lose stale pads.
3. importer reads `GRID` child BPM; `nmlCues` emits grids from Beatgrid only
   (native attrs + GRID children, ascending START); merge writeback edits
   TEMPO in place and skips cue management for cue-less tracks.
4. `cuepattern.DedupeCues` at the `cuewriteback.ApplyCues` choke point.
5. NEW `rave-mate repair-collection [-ref backup] [-dry]` — token-surgical
   repair (docs/BEATGRID_FIXER.md).

## Repair executed on the user's library (2026-08-08)

`repair-collection -ref collection.nml.bak.20260710-175556` on Traktor 4.5.1's
collection (Traktor closed, backup → `library-backups/traktor-repair-…`):
21,557 scanned, 478 repaired — 244 pad reorders, 14 dupes dropped, 260 grids
restored. Post-verify: 0 tracks off the pre-gridfix lattice, out-of-order
pads 161 → 11 (all = skipped no-LOCATION remix-set entries), entry count
unchanged, factory flexible grids intact, COLOR attrs preserved. Idempotent
second pass = 0 changes (tested). rave-mate was not running; its library DB
converges on next launch (nmlsrc re-import).

Open: user hardware pass in Traktor (pads + grid feel); Rekordbox/Serato cue
writers unchanged (unaffected by these classes); the 32 BPM-changed + 11
>60 ms grid moves were left alone as deliberate.
