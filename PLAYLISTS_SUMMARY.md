# Playlists / Collection Management

Rich playlist management for the imported DJ collection (Studio → Library → Playlists).
Three kinds:

- **Manual (★)** - created in-app. Add/remove/reorder (↑ ↓ ✕ per row), rename, delete.
- **Smart (⚡)** - rule-based, evaluated **live** against the loaded collection (no stored
  track rows). Rules: genres (OR, case-insensitive substring), BPM band, key contains,
  rating ≥ (source ratings normalized to 0–5 stars - Traktor RANKING 0–255 handled),
  play count ≥, free search across title/artist/album/label/comment. Editor shows a live
  "N of M tracks match" count; **feel presets** seed the BPM band (chill/groovy/driving/
  peak/hard) as the energy proxy.
- **Imported** - synced from the DJ software on every collection import (replaced
  wholesale; manual + smart untouched). Read-only; "Duplicate as manual" to edit.

## Pieces

- `internal/libdb/playlists.go` - `playlists` + `playlist_tracks` tables (UNIQUE
  playlist+path dedupe, ordered by position). CRUD, AddToPlaylist (dedupe append),
  ReplacePlaylistTracks (reorder = wholesale rewrite), PlaylistsForTrack (membership),
  SyncImportedPlaylists.
- `internal/musiclib/playlists.go` - `ParseNMLPlaylists`: streams the NML to the
  PLAYLISTS node, walks the FOLDER/PLAYLIST tree into flat playlists with `Folder`
  paths ("Sets/2026"); Traktor SMARTLISTs skipped. PRIMARYKEY TRACK keys resolved to
  OS paths (same resolveKey as history).
- `internal/musiclib/smart.go` - `SmartRules` (Match/FilterSmart/Describe/Empty),
  `StarRating` normalization, `FeelPresets`.
- `internal/ui/view_studio_playlists.go` - Playlists section (list + ordered track
  pane), smart rules editor, add-to-playlist dialog, Collection multi-select bar,
  detail-panel membership block, M3U export (`playlist-<name>.m3u8` in the data dir).

## Adding tracks

- Track detail (any section) → PLAYLISTS block: membership chips (click = jump to the
  playlist; ⚡ chips = live smart matches) + "Add to playlist…" dialog - Check per
  manual playlist (single track pre-checked = member, untick removes) + inline
  "Create + add".
- Collection → tick row checkboxes → bottom bar "N selected · Add to playlist…"
  (deduped against existing entries).

## Import

Traktor import runs a second (cheap) parse pass over collection.nml for PLAYLISTS and
`SyncImportedPlaylists`es them under the source. Rekordbox/VirtualDJ playlists persist
the same way when their importers emit `Library.Playlists`.

Bonus: `ChipMultiSelect` popovers now filter + scroll (genre taxonomies run to 400+
options; the popover used to overflow the screen).

Verified live via ctl against a 22,960-track / 117-playlist Traktor collection:
import, open, create manual + smart (genre+BPM live count), multi-add w/ dedupe,
reorder, remove, membership chips, M3U export, delete.
