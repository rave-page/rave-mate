# ID Marks - unreleased-track leak prevention

Problem: playing an unreleased promo ("ID") leaks its identity through every output -
overlays, live-stream ingest, now-playing file, recorder tracklist, Publish UI, VR overlays.

## Design

- `internal/idmark`: JSON store (`idmarks.json` in config dir - works with libdb disabled).
  Entry = path (file or dir; dir matches recursively) + `showArtist`/`showLabel`.
  `Match(path)`: longest-prefix wins (file mark overrides its dir's), Windows path semantics
  (case-insensitive, `/`≡`\`).
- **Central redaction** in `internal/session` (`redact.go`): `Merger.SetRedactor(RedactFunc)`;
  applied at the merger's only two output boundaries - `Snapshot()` and emitted `Update`s -
  on deck/master/nowPlaying scopes. Marked track ⇒ title `"ID"`, artist blanked unless
  `showArtist`, label+album blanked unless `showLabel`, **path blanked** (filenames embed
  "Artist - Title"; path rides the stream ingest wire). Raw stays merger-internal.
- Every consumer inherits (no per-sink code): filesink, pngsink, overlayserver (browser/SSE),
  overlayobs, videoshare (Spout), stream publisher (in-proc + featurehost child via
  `StreamProxy.forward`), recorder (tracklist/Publish), peerbridge, Live/Session/dashboard UI.
  VR overlays render pngsink/overlayserver output → inherit; `internal/vroverlay` reads no
  session state (verified, untouched).
- Wire-in: `app.go` after `session.NewMerger()`.

## Limits

- Master-scope metadata with no path (Icecast in-band comments) can't be matched → passes
  through. Mark-by-path needs a path on the wire (Traktor/collection-derived have it).
- Recorder dedupes by title - consecutive distinct IDs may merge into one tracklist row.
- Marks are per-computer; a paired instance keeps its own list (remote Library shows an
  explicit degraded panel).

## UI

Library → ID Marks (add file/folder, per-mark toggles, remove, `?`-help); row context menu
"Mark as ID"/"Unmark ID" (local rows; disabled on remote rows).

## Tests

`idmark`: nested dirs, file-overrides-dir, case/separator folding, passthrough, dedupe,
persistence. `session/redact_test.go`: snapshot + emitted-update redaction, flag
combinations, held-raw-path fallback (title-only refresh), master scope, unmarked
passthrough, `BuildOverlay` inheritance.
