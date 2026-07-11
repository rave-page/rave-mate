# Drops in library sync (#77 rave-mate half)

Bulk library sync (`POST /library/tracks/bulk`, `playsync.SyncLibrary`) now carries drop
markers per track.

## Wire contract (agreed cross-repo)

- Field `drops_ms`: `[]int64` ms from track start, sorted asc, deduped. Struct tag
  `omitzero` (Go ≥1.24): nil = omitted, non-nil empty = `[]`.
- Absent = server keeps stored drops. Explicit `[]` = clear server-side drops.
- Server stores opaque; no derivation. BE half deployed in parallel (separate agent).

## Send rule

`drops_ms` goes on the wire **iff the track has a `track_drops` row** — including a
cleared `[]` tombstone; no row = field omitted. Implemented in
`playsync.libraryPayload` (+ `wireDrops`: round → drop negatives → sort → compact).

To make clears propagate, `libdb.SetDrops` no longer deletes on empty: an existing row
flips to a `[]` tombstone (`UPDATE … SET drops='[]'`); clearing a never-marked track
stays a no-op (no tombstone, no journal). New `libdb.DropRows()` returns all rows incl.
tombstones (sync); `AllDrops()` keeps its `len>0` filter (collection NO DROPS chip,
player markers).

## Change detection

Nothing extra needed: sync is a payload-hash ledger (`library_sync`), not change_log
driven. Drops are part of the payload → any drop edit/clear flips the hash → track
re-uploads on next sync. change_log `drops` events (cue editor) stay journal-only.

## Tests

- `playsync/librarysync_test.go`: golden wire JSON (with drops / tombstone `[]` / no
  row), payload-hash flips on add/move/clear, end-to-end SyncLibrary drops + clear
  re-upload.
- `libdb/drops_test.go`: tombstone semantics, DropRows vs AllDrops, no-op clear.

Not live-verified against the API (isolated instances must not sign in); payload-level
tests only.
