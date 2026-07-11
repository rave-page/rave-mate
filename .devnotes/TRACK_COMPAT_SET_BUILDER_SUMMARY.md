# Track-compat marks + sorted-copy set builder (#79)

## Shipped

- **libdb `track_compat`** (`internal/libdb/trackcompat.go`): symmetric path-keyed pairs,
  `NormPair` (a<b) + UNIQUE(a,b,kind); kinds `blend|double_drop|energy`.
  `AddCompatPairs` (C(N,2), one tx) / `RemoveCompat` / `CompatFor` / `CompatForMany`
  (chunked IN, whole-playlist safe). Path identity = same currency as `playlist_tracks`
  (survives re-imports, source-independent) - deliberate deviation from the spec's
  `a_id,b_id`.
- **webui marking + discovery** (`internal/webui/library_compat.go`): selection-bar
  button + right-click ctx-menu entry (Collection `collSel` / Browse `batch`, 2..100
  tracks), kind-picker modal; detail-rail "Works well together" section; Find-compatible
  modal = direct + depth-2 BFS (`compatDiscover`, cap 200, "via X" rows), per-pair ✕.
- **Sorted-copy set builder** (`internal/webui/library_setbuilder.go`): `lib-plsort:` on
  every playlist action row → NEW manual playlist; group-bys: compat clusters
  (union-find), energy (FeelPresets BPM bands, next-min thresholds), key (Camelot
  1A..12B, A before B), lastplayed/added/released (year-month desc, `parseDateLoose`).
  Dividers: `transcode.gendivider` worker (ffmpeg lavfi anoisesrc 2s, -24dB, fades) +
  byte-copies per boundary (`playlist_tracks` dedups by path → numbered files);
  degrade = toast + no dividers.
- **`is_divider` containment** (user hard requirement): additive ALTER + name+title
  backfill; `LoadAllTracks`/`AllSourcedTracks` filter `is_divider=0` at query level →
  collection view, `ctl sync-library` bulk upload, media sync, cleanup, libsync
  hub-merge all inherit; `track_art` meta-match subqueries filtered; playsync playlist
  push skips divider rows (`plResolver.dividers`). Dividers stay in playlists + M3U
  export (their purpose). Playlist views resolve divider titles via a byPath-only
  hydrate (`DividerTracks`).
- i18n `library.compat.*` + `library.plsort.*` in all 7 locales; `docs/user/library.md`
  + index.

## Verified live (isolated instance, real 12.8k library copy)

mark 3 → 3 normalized rows in db; rail partners; find modal depth-2 "via"; ✕ removal
re-evals graph; sorted copy by key + dots → 30 tracks + 17 dividers interleaved, original
untouched; dividers absent from collection (12835) + present in playlist + in M3U (17);
`is_divider` backfill on existing db; right-click ctx menu via `ctl tap2`.

## Deferred

- **Publish-tab tracklist selection**: recorder tracks carry artist/title only, no
  library path identity → marking needs fuzzy resolution first; not cheap, deferred.
- **Smart-rule `compat` term**: `musiclib.FilterSmart` is pure (no DB); a compat rule
  needs `track_compat` access → doesn't extend cleanly. Playlist facet + sorted-copy
  cover the discovery use today.

## Gotchas

- `playlist_tracks UNIQUE(playlist_id,path)` → one divider file per boundary
  (`divider-<style>-<n>.mp3`), generated once + copied.
- `EnsureSource` inserts WITHOUT `imported_at` (NULL sorts last in `FirstSource` DESC) -
  the synthetic `rave-mate` divider source must never become the launch-load source.
- `LoadAllTracks` now also selects `import_date` (date-added grouping was silently empty).
- smartSelect options only exist in DOM while open: ctl drive = `ss-tgl:<id>` then
  `ss-pick:<id>=<val>`.
