# Set capture (Icecast)

Capture a live set to disk by broadcasting it from Traktor to a **local Icecast-source
receiver** rave-mate hosts, time-linked to the recorder's tracklist. Unlocks **history
playback** (play a past set, seek to any track) and **better fingerprint matching** (a real
Chromaprint per played track, from the broadcast audio). Off by default (opt-in feature
`setCapture`).

## How it works

Traktor → Preferences → Broadcasting points at `127.0.0.1:<port>` (mount + source password).
On "start broadcasting" Traktor opens an Icecast2 source connection; rave-mate authenticates
it, streams the encoded body to a timestamped file in `setsDir`, and parses the broadcast
metadata for now-playing. Audio is **broadcast-quality lossy** (Ogg Vorbis / MP3) by design -
Icecast carries an encoded stream. (Traktor's prefs aren't safely file-patchable, so the
settings card shows the exact values + a live "connected" indicator instead of auto-writing.)

## What ships

### `internal/icecast` - the receiver
- Stdlib Icecast2 source endpoint (raw TCP + `textproto`): `SOURCE`/`PUT` upload, HTTP Basic
  source auth (constant-time), one capture at a time (a 2nd *streaming* source → 403).
- Streams the body to `<setsDir>/<ts>_<mount>.<ext>` (ext from Content-Type).
- **Single-file capture (opt-in `SingleFile` + `ReconnectGrace`, default 15s):** a source
  drop opens a *grace window* - the file stays open and a reconnect within it **resumes the
  same capture** (same id/path, bytes accumulate) instead of starting a new fragment, so a
  transient blip / encoder restart no longer chops a set. The capture finalizes only when the
  grace expires with no reconnect (or on shutdown). `Snapshot.Reconnecting` flags the gap for
  the UI. Off → one file per source connection (original behaviour).
- Now-playing two ways: **in-band Ogg Vorbis comments** (`oggmeta.go` - incremental
  page/packet scanner, chained-stream + mid-stream-resync resilient, also OpusTags) and the
  **`/admin/metadata`** side channel Traktor uses for MP3.
- `Subscribe{Meta,Capture}` + `Snapshot` for the source adapter, the linker, and the UI.
- `icecastsrc` is now real - adapts the meta feed into master title/artist observations
  (low confidence: master-only + delayed). Wired as the `setcapture` module + a live source.

### `libdb` `set_recordings` + `played_tracks` + timing link
- `set_recordings` table (id, recording_id, path, format, mount, started_at, ended_at,
  bytes). `SaveSetRecording` (upsert), `SetRecordingsFor`, `ListSetRecordings` (all nil-safe).
- **`played_tracks` table** - the durable, queryable consolidated play log: every track the
  recorder *confirms* as played (fused now-playing from all inputs) is upserted with absolute
  start/end, deck, and metadata, keyed by `recordingId#slot` and linked to its recorder
  recording. `SavePlayedTrack` (upsert), `PlayedTracksFor`, `ListPlayedTracks` (nil-safe).
  The recorder writes a row on confirm, then updates it on metadata-enrich and final end - so
  played history survives independent of the bbolt snapshot and can be mapped to captures.
- A linker (`app.linkSetCaptures`) subscribes to the receiver's capture stream: persists the
  row on start; on end links it to the recording with the most temporal overlap
  (`recorder.FindByWindow`). **Per-track offset into the audio = track.StartedAt −
  capture.StartedAt** (derived at playback; no stored offsets).

### UI
- Settings: a **Set capture (Icecast)** card - enable toggle (live module), port/mount/source
  user/password/setsDir fields, "Open sets folder", a guided Traktor-Broadcasting block that
  recomputes live from the fields, and a connection indicator (connected ✓ / listening / off)
  driven by capture events.
- Recordings: a recording with a linked capture gains **playback** - transport + seek (the
  beep player) and per-track jump buttons that seek to each track's offset. Unplayable
  formats (AAC) fall back to the OS player.

### `internal/setfp` - per-track fingerprinting
- `fingerprint.segment` worker: ffmpeg decodes `[offset, offset+len)` → temp mono 11025 Hz
  WAV → fpcalc (process-isolated; temp cleaned).
- `setfp.FingerprintSet` fingerprints each track span (length capped at 120 s) and records
  the print in the `change_log` keyed by portable `track_hash` (+ `track_fp`). A
  "Fingerprint tracks" button (gated on the Fingerprint feature) runs it off-thread.

## Verification
- Unit tests: Ogg scanner (extract/chained/byte-at-a-time/resync/truncated), SOURCE capture +
  admin metadata + auth/mount-in-use rejection + snapshot, **single-file reconnect coalescing
  (one file, bytes accumulate across the gap)**, `set_recordings` + **`played_tracks`**
  upsert/query + nil-safety, `FindByWindow` overlap, **recorder stop hold-off (no restart while
  the same track plays; clears on silence)**, `setfp` change_log writes + failure-skip.
- **Live (2026-06-05)**: a synthetic Icecast source against the running receiver authed →
  captured a real `.ogg` to setsDir → in-band Vorbis comment surfaced as now-playing → capture
  start/end events → `set_recordings` row persisted. Settings card + Recordings tab rendered
  with no overflow (verified via `rave-mate ctl`).

### Recorder stop semantics
- The recorder is always-on; **Stop** just closes the in/out window of the current set (all
  tracks within it are already assigned to that recording). It no longer immediately
  re-arms a duplicate set: a post-stop hold-off (`suppressKey`) blocks auto-restart while the
  *same* track is still on the deck, clearing on a track change or a silence gap.

## Notes / future
- Lossless capture would be Traktor's internal Audio Recorder (WAV), but that has no live
  metadata/timing feed - Icecast is the right tool for the linked-set goal.
- The receiver binds **loopback only** (Traktor is co-located); it never faces the LAN.
