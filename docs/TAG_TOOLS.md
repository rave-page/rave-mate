# Tag tools

Audio-file tag writing + fixing: `internal/tagwrite` (field-scoped atomic writes),
`internal/tagsync` (library-driven, revertible), `internal/tagfix` (scan + repair).
MP3 (ID3v2) + FLAC (Vorbis comments) only; other formats report unsupported.

## Write fields (tagwrite)

Canonical field → frames. Writes are atomic (temp + rename), touch only named fields;
value `""` clears the field.

| Field | MP3 | FLAC |
|---|---|---|
| title / artist / album / genre | TIT2 / TPE1 / TALB / TCON | TITLE / ARTIST / ALBUM / GENRE |
| comment | COMM | COMMENT |
| bpm | TBPM | BPM |
| key | TKEY | INITIALKEY + KEY (read prefers INITIALKEY) |
| year | TDRC (v2.4) / TYER (v2.3) | DATE |
| label | TPUB | LABEL + ORGANIZATION (read prefers LABEL) |
| rating | POPM email `rave-mate`, counter 0 | RATING |

Rating canonical value = `"0"`..`"255"` (Traktor scale). FLAC stores conventional 0–100
(converted on write); reads accept both scales (>100 = already 0–255). MP3 write/clear
preserves other writers' POPM frames; read prefers ours, falls back to the first.

## Library sync (tagsync)

`Apply(db, track)` writes the track's analysis (bpm/key/genre/comment/rating — rating
normalized stars×51 / raw 0–255) into the file with a before-snapshot in `tag_edits` +
`change_log` mirror; `Revert(db, path)` undoes the latest write. Title/artist/album are
never auto-synced — curation flows only through tagfix or an explicit editor.
`ApplyTags(db, track, tags)` = same machinery for an explicit tag set.

## Fix kinds (tagfix)

`Scan(tracks, opts)` proposes typed single-field repairs (`Problem{Path, Field, Kind,
Detail, Current, Proposed}`); `Apply(db, problems)` writes them — grouped into one atomic
write per file via `tagsync.ApplyTags`, so every repair is revertible. A problem whose
`Current` no longer matches the file (changed since scan) is skipped, never overwritten.

| Kind | Detects | Proposes |
|---|---|---|
| `v1_only` | ID3v1 trailer, no ID3v2 header (pure-stdlib 128-byte parse) | equivalent v2 frames (title/artist/album/year/genre) |
| `mojibake` | text frame is double-encoded UTF-8 (Ã©/â€™; latin1/cp1252 byte-recovery re-reads as valid multi-byte UTF-8, ≤3 rounds) or has C1 controls = cp1252 punctuation | repaired string; `Detail` carries the evidence |
| `missing` | library has BPM/key/genre, file tag empty | library value |
| `mismatch` | file BPM off library by >0.05, or key differs after case/space fold | library value |
| `no_basics` | file title/artist empty, library has one (DJ-software import) | library value |

Mojibake guards (conservative by design): genuine latin1 text (Störung, naïve) recovers
to invalid UTF-8 → never flagged; undefined cp1252 bytes → ambiguous → never flagged;
repairs containing control chars are rejected. cp1252 table is hand-rolled (32 entries) —
no charset dependency.

Unsupported/unreadable files are skipped silently (count via `Options.Skipped`);
`Options.Kinds` filters, `Options.Progress` reports per-file progress.
