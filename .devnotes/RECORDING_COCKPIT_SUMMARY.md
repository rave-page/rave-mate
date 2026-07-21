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

## Tracklist export v2 (2026-07-21, webui)

- **Editable start offsets** (Tracklist subtab, finished sets): each `[h:]m:ss` offset is an
  inline input (`pub-toff:<rec>\x1f<idx>`, ctl label `offset-<n>`). `recorder.SetTrackStart`
  moves the absolute start, drags a neighbour end stamped at the old start (±2s) along, and
  refreshes the play-log row. Direct store writer #5 - storeMu + drainPersist discipline
  (see recorder.mutate).
- **Fix start times** (`pub-fixtimes:`): silence-probes the linked capture
  (Automations.ProbeSilence, KindSilence cache) → `recorder.PlanTimeFix`: audible start =
  capture start + leading silence; set start + track 1 move there, earlier "starts"
  (pre-set loop/cueing) clamp up; track 2's start bounds the probe (garbage → capture
  start; still past t2 → no plan). Preview modal shows the set-start move + every offset
  that changes; Apply = `ApplyTimeFix`.
- **Text-export styles** (`pub-exportfmt:txt` → dialog): presets classic/youtube/numbered/
  plain/detail + custom line template ({n} {nn} {offset} {artist} {title} {track} {album}
  {key} {bpm} {deck}; header {name} {date} {count}), header toggle, live preview + Copy.
  Persisted in `Features.Recorder.Export*`. Remote export ships the controller's style via
  `RecExportParams.Line/NoHeader` (old peers → classic). `Recording.ExportText`; default
  template stays byte-identical to the classic export.
- **Export progress stages** (`transcode.run`): emits `stage` prepare/measure/encode on a
  shared 0-100 scale - loudness measure streams its decode position into 0-25%
  (MeasureArgs keeps ffmpeg stats on), encode folds into the rest, ffprobe fallback total
  covers headerless captures. Player export bar captions queued/preparing/measuring -
  the "over a minute at silent 0%" (loudness pass on a 2h set) is gone.

## Time-fix v2: opener choice + phantom removal + fader-true starts (2026-07-21)

- **Root cause of "already fixed" on real sets**: deck-play/history times for early tracks
  can predate the CAPTURE itself (cueing/looping while prepping); the v1 track-2-start
  bound treated those phantoms as trustworthy and refused. v2 trusts probed silence.
- **Opener choice**: the file can't order pre-audio tracks, so the fix modal picks the
  deck-timeline default (last track started before the audible moment) with a smart-select
  to overrule. Tracks before the opener that were over before the capture rolled →
  removed (preview-labeled); pre-audio tracks after a hand-picked opener clamp to 0:00.
  `PlanTimeFix(rec, capStart, capEnd, leading, opener)`; TimeFix.RemoveTracks/Opener.
- **Row removal**: finished-set rows get a context menu (compat + "Remove from tracklist",
  `pub-trm:`); `Recorder.RemoveTrack`. Removals rewrite the slot-keyed play-log wholesale
  (`libdb.ReplacePlayedTracks`, tx delete+insert).
- **Fader-true starts (future sets)**: DeriveNowPlaying picks a playing deck even at fader
  0 - that's how phantoms confirmed. The recorder now (a) gates on
  `Score <= OnAirFaderThreshold` (fail-open without fader data), so cue/loop prep never
  starts a set or confirms, and (b) stamps per-deck first-fader-up marks
  (markOnAirLocked); confirm uses the MEASURED fader-up as StartedAt when it precedes the
  loudest-deck switch (blend starts at the fader, not the crossover).

## Time-fix v3: fader-history reconstruction (2026-07-21)

- **Exact mechanism (user spec)**: audio anchors 0:00 (capStart+silence); each pre-audio
  track starts at ITS deck's first fader-up from the previous one, searched up to the
  first post-audio track's recorded start; no on-air moment = cue preview → removed.
  `PlanFaderFix(rec, capStart, capEnd, leading, evs)` in faderfix.go.
- **Sources, layered**: (1) `Recording.OnAirLog` - measured per-deck on-air crossings
  logged live by markOnAirLocked (cap 4000, stop-appending; rides existing persist
  points); (2) `ParseTraktorPayloadLog` over traktor-payloads.jsonl
  (Features.Traktor.LogPayloads, DEFAULT ON; /updateDeck isPlaying + /updateChannel
  onAirLevel=FieldFader post-fader level, ch 1-4 → decks A-D); (3) silence+opener
  heuristic fallback (PlanTimeFix; auto opener now = FIRST pre-audio track - the
  prep-then-record workflow's intended opener; all other pre-audio removed).
- Fader-history plans render without the opener select (authoritative);
  publish.fix.descFader. Deckless histories refuse → heuristic fallback.
- Verified on UMC_Neuro_Oblivion + the real payload log: Sindicate & Anizo → 0:00
  (opener), Hated replay → 2:02 (its real fader-up), zero manual input.
