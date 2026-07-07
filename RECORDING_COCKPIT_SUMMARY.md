# Recording pipeline + cockpit

How live track metadata, Icecast set captures and finished OBS recordings become one
linked "set" - and the Recordings tab that drives it.

## Capture sources → one model

`libdb.set_recordings` rows carry `kind`:

| kind | source | events |
|---|---|---|
| `icecast` | local Icecast receiver (Traktor broadcast → audio file) | start + end (child `capture` events) |
| `obs` | OBS bridge - `featurehost` child holds the obs-websocket conn, watches `RecordStateChanged` | end only (`rec` event: path, window, bytes) |

`app.linkCaptures` (daemon-side, libdb single-writer) persists both feeds and time-links
each finished capture to the recorder tracklist with max window overlap
(`rec.FindByWindow`). Track offset into the media = `track.StartedAt − capture.StartedAt`.

Orphan re-link: captures that finish with no matching recording (or saved before the set
finalized) are retried at startup + whenever a recording finalizes
(`libdb.UnlinkedSetRecordings` → `RelinkSetRecording`).

## OBS bridge (`featurehost` "obs" module)

- obs client gained op:5 event fan-out (`SubscribeEvents`/`Done`) + `GetRecordStatus`.
- Child: connect loop (5s retry - closed OBS is normal, not an error), back-dates an
  already-running recording from `outputDuration`, emits `state`
  (connected/recording/started) + `rec` on stop. Settings toggle = live module
  start/stop; host/port/password edits apply on toggle.
- e2e test: fake obs-websocket server + real child process (`obs_e2e_test.go`).

## Live-metadata hardening

- **Stale ghost tracks**: `DeriveNowPlaying` skips a "playing" deck whose newest field TS
  is older than `session.NowPlayingStaleAfter` (2 min) - a source dying mid-play
  (app killed, conn lost) can no longer keep the recorder/now-playing/now_playing.txt
  live forever. Zero TS (synthetic states/tests) never stale; recorder uses the
  clock-injected `DeriveNowPlayingAt`.
- **Candidate fill**: metadata arriving during the confirm window (NML album/key a beat
  behind deck ingest) lands on the candidate before confirm commits it
  (`recorder.fillCandidate`).
- **Pending surface**: `recorder.Pending()` exposes the unconfirmed candidate
  (track, firstSeen, confirmAt) → cockpit "confirming…" countdown.
- **Stale-recording sweep**: on recorder start, persisted recordings with no end
  (unclean exit) are finalized from their last track (empty ones discarded) - no more
  eternally-"live" zombie sets.

## Cockpit (view_recorder.go)

- **Hero**: REC / CAPTURE / OBS status badges (colored dots: brand-base = recording,
  mint = connected/capturing, amber = degraded, muted = off) + Finish set.
- **Now playing**: track + deck/BPM/key/provenance; confirm countdown progress bar while
  pending, "✓ track N in the tracklist · playing …" once committed. 1s tick + recorder
  subscription drive it.
- **Sets list + detail** (`adaptiveSplit 0.40`): newest-first list (live set pinned,
  meta shows audio/video capture + matched flags); detail = actions
  (Export / Match history / Delete-with-files), tracklist with per-track meta, captures
  (audio: in-app transport + per-track jump + fingerprint; video: open / reveal),
  unlinked captures section. Newest set auto-selected.
- `RefreshRecordings` does an in-place repaint (`u.recorderRefresh`), full tab rebuild
  only as fallback.
