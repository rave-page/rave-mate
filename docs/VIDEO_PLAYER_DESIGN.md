# Native video player + cue-aware nav + trim/cut editor - design

Status: PROPOSAL (for review before code). Backend decision (confirmed): **ffmpeg frame-decode**,
no new heavy dep. Build order (confirmed): plan whole thing first, then phase the implementation.

## Goal

A native (Fyne) player in rave-mate that plays our recorded sets - **audio AND video** (OBS
captures) - enriched with the recording's tracklist so the scrubber shows where each track starts
and you can jump track-to-track. Extendable into a **trim/cut editor**: set in/out points on the
player timeline, queue the cuts as an ffmpeg encode job (reusing + unifying the transcode worker).
Easy for noobs, powerful for pros.

## Constraints (rave-mate hard rules)

- Native Fyne, no webview, no embedded browser. Video frames render to a Fyne canvas.
- Stdlib-first, no heavy deps, 7-day soak. **ffmpeg is already managed** (`mediatools`) → we shell
  out to it; no libmpv/libav cgo. The only "decoder" is ffmpeg as a subprocess.
- Crash isolation: ffmpeg is already a separate process (the crash boundary). The Go side only
  reads pipes + draws. oto audio is the one in-proc cgo bit (already used by audioengine).
- gofmt/vet clean, no `any` at boundaries, commit per phase.

## Architecture overview

```
                ┌─────────────────────────── UI process (Fyne) ───────────────────────────┐
                │                                                                           │
recording  ──►  │  RecordingPlayerView                                                      │
(file+tracks)   │   ├─ VideoCanvas (canvas.Image)  ◄── frames ── mediaplayer.Player         │
                │   ├─ Transport (play/pause/seek)  ──cmds──►        │                       │
                │   ├─ Timeline+scrubber (waveform.go-style)         │  owns:                │
                │   │    └─ track markers (from Recording.Tracks)    │   • ffmpeg video pipe │
                │   └─ TrimBar (in/out points, segments)             │   • ffmpeg audio→oto  │
                │            │                                        │   • master clock      │
                │            └─ "Export cut" ──► trimjob.Plan ──► jobs.Hub ──► worker(ffmpeg) │
                └───────────────────────────────────────────────────────────────────────────┘
```

Three new packages + UI; everything else reuses existing infra.

---

## Phase 1 - Video playback (`internal/mediaplayer`)

New package `internal/mediaplayer` - an ffmpeg-backed player that handles audio-only AND video
files (superset of audioengine for the player surface; audioengine stays as-is for the lightweight
Library/now-playing audio).

### Decode pipeline

One ffmpeg invocation per play/seek, decoding to two raw pipes the Go side consumes:

- **Video:** `ffmpeg -ss <t> -i <file> -f rawvideo -pix_fmt rgba -s <WxH> -r <fps> pipe:` →
  fixed-size RGBA frames read with `io.ReadFull(w*h*4)` into a reused `image.NRGBA`. Display size
  is the canvas size (decode-scaled by ffmpeg, cheap, crisp). fps target ~30 (cap to source).
- **Audio:** `ffmpeg -ss <t> -i <file> -f s16le -ar 48000 -ac 2 pipe:` → PCM into oto (the same
  audio stack beep/oto already pulls in; we feed oto a raw PCM stream).
- Realistically **one ffmpeg process, two outputs** (`-map 0:v -f rawvideo … pipe:3` + `-map 0:a
  -f s16le … pipe:4`) via extra `cmd.ExtraFiles`, OR two ffmpeg procs sharing the same `-ss`.
  Start with **two procs** (simpler, independently seekable, robust); revisit one-proc if needed.

### A/V sync - audio is the master clock

- The oto stream's played-sample count is the authoritative position (`samplesPlayed / sampleRate`).
- The video reader keeps the latest decoded frame + its PTS (frame index / fps). The UI ticker
  (~60 Hz `fyne.Do`) shows the frame whose PTS ≤ audio clock; drop/hold frames to track audio.
- Audio-only file → no video pipe; the canvas shows cover art / a waveform (reuse `waveform.go`).
- Video-without-audio (rare) → video frame index drives a wall-clock master.

### Seeking

Seek = cancel the current ffmpeg pair, relaunch both at `-ss <t>` (fast input seek before `-i`,
plus a short accurate seek after `-i` for frame accuracy). Sub-200ms on keyframey H.264. Scrubbing
(drag) shows **frame previews** via the single-frame path below, committing a real seek on release.

### Frame-accurate preview (powers scrub + trim)

`mediaplayer.FrameAt(ctx, file, t, w, h) (image.Image, error)`:
`ffmpeg -ss <t> -i <file> -frames:v 1 -f rawvideo -pix_fmt rgba -s WxH pipe:` → one frame.
Cheap, frame-accurate; the backbone of scrubbing and cut-point setting (no audio).

### Player API

```go
type Player struct { … }
func New(ffmpegPath string, log *logbus.Bus) *Player
func (p *Player) Open(file string, vidW, vidH int) error // probe (ffprobe) dur/has-video/size
func (p *Player) Play() ; func (p *Player) Pause() ; func (p *Player) TogglePause() bool
func (p *Player) Seek(sec float64)
func (p *Player) State() State    // {Path, Playing, Cur, Total, HasVideo, W, H}
func (p *Player) Frame() image.Image           // latest decoded frame (nil if audio-only)
func (p *Player) OnTick(func(cur, total float64))  // ~position updates for the transport
func (p *Player) Close()
```

ffmpeg/ffprobe via `mediatools.Resolve`; spawn via `exec.CommandContext` + `sysexec.Hide`; reuse
the stderr `parseHMS` progress style from `worker/transcode.go`.

### UI (Phase 1)

- `internal/ui/view_player.go`: `VideoCanvas` (`canvas.Image`, refreshed from `Player.Frame()` on
  a `fyne.Do` ticker) + a transport bar (play/pause, time, scrubber). The scrubber reuses the
  `waveform.go` `canvas.Raster`+seek interaction (already has click-to-seek).
- Entry point: a **"Play" action on a capture row** in the Recordings tab (`view_recorder.go`)
  opens the player. Audio captures keep using the existing lightweight audio path; video captures
  (Kind=="obs" or a video extension) open the new player. (Inline vs. detached: detached
  `dialog.NewCustom`/dedicated view - decided in "Open questions".)

**Phase 1 deliverable:** play a recorded video set in-app with working transport + scrub. Commit.

---

## Phase 2 - Cue/tracklist-aware navigation

### Source of markers

The recording's tracklist is already in memory: `recorder.Recording.Tracks []Track{StartedAt,
EndedAt,…}`, and `view_recorder.go` already computes `offset = track.StartedAt − capture.StartedAt`.
So for a capture we get `[]Marker{Offset, Title, Artist}` directly - **no cue parsing needed** for
our own recordings.

### Optional `.cue` parser (`internal/cuesheet`)

For standalone/imported files (a video whose tracklist isn't in the DB), add a small reader for the
`.cue` sidecar we already write (`audiorec.cueSheet`, `mm:ss:ff` @75fps). Pure + unit-tested
(mirror of the existing writer). Used as a fallback when no `Recording.Tracks` is available.

### Navigation UI

- Track **markers on the scrubber** (tick + label on hover) at each offset.
- **Prev/Next track** buttons → seek to the previous/next marker (quick-scroll to a track's start).
- Current-track **label overlay** on the video (which track is playing now), from the active marker.
- A compact **tracklist panel** beside the player; click a row → seek to that track.

**Phase 2 deliverable:** markers + jump-to-track + current-track readout. Commit.

---

## Phase 3 - Trim/cut editor → encode job

### Segment model (`internal/trimjob`)

```go
type Segment struct{ Start, End float64 } // keep-ranges, in source seconds
type Plan struct {
  Input  string
  Output string
  Keep   []Segment      // ordered, non-overlapping ranges to KEEP
  Reencode bool         // false = stream-copy (fast, lossless, keyframe-snapped)
  Preset transcode.Preset // when Reencode: codec/CRF/accel/loudness (reuse builtins)
}
func (p Plan) Validate() error
func BuildJobs(p Plan, tmpDir string) (steps []Step, err error) // trim each segment → concat
```

### Editor UX (on the Phase-1/2 player)

- A **TrimBar** under the scrubber. Set **in/out** at the playhead (keyboard `I`/`O` + buttons);
  this defines keep-ranges. Multiple segments supported (cut out the boring bits / keep N regions).
- Drag segment edges; nudge frame-by-frame using `FrameAt` preview for precise cut points.
- Snap-to-track-marker helper (set a cut exactly at a track boundary - the DJ use case).
- Live "result duration" readout; preview a cut point by seeking to it.
- **Two modes, plain-language:**
  - *"Fast (no re-encode)"* → stream-copy (`-c copy`), cuts snap to the nearest keyframe. Instant,
    lossless, but cut points land on keyframes (explained in the UI).
  - *"Precise (re-encode)"* → exact cuts, pick a preset (reuse transcode builtins + loudness).

### Encode = reuse the transcode pipeline

`transcode.Job` already has `TrimStart/TrimEnd` + `Job.Args()`. For multi-segment keep-lists:

1. For each `Keep` segment → one trim step (`transcode.Job` with TrimStart/End; `-c copy` for fast
   mode, preset encode for precise).
2. Concat the segment files via ffmpeg `concat` demuxer (`-f concat -safe 0 -i list.txt -c copy`).
3. Single keep-range with fast mode → collapse to one trim, no concat.

Run through the **existing `jobs.Hub` + worker supervisor** (crash-isolated, progress buffered).
Add a worker handler `trimconcat` (orchestrates the steps) OR sequence existing `transcode.run`
calls + a final concat in the hub. Progress surfaces in the **same jobs UI** as transcode →
unifies the encode surfaces (one place to watch encode jobs, per the "unify/wire together" ask).

### Output

Cut lands next to the source (`<name>-edit.mp4`) and is **registered as a new SetRecording**
(`libdb.SaveSetRecording`) linked to the same recording, so the edited cut shows in Recordings and
can itself be played/exported.

**Phase 3 deliverable:** mark cuts → queue → encoded edit appears in Recordings. Commit.

---

## Unified, easy-but-powerful UX

- **One player** used for playback, navigation, and trimming (progressive disclosure: transport
  always visible; the TrimBar/segment tools live behind an "Edit / Trim" toggle so casual playback
  stays uncluttered).
- **One encode surface:** trim jobs + transcode jobs share `jobs.Hub` and the existing jobs/progress
  UI. No second job system.
- Sensible defaults (fast stream-copy, output beside source, auto-named) so a noob clicks
  In → Out → Export; pros open the preset/precise controls.

## New code (summary)

| New | Purpose |
|---|---|
| `internal/mediaplayer/` | ffmpeg decode → frames + PCM, A/V-synced player, `FrameAt` |
| `internal/cuesheet/` | `.cue` reader (fallback markers) + unit test |
| `internal/trimjob/` | segment Plan → trim+concat step builder + unit test |
| `internal/ui/view_player.go` | VideoCanvas + transport + markers + TrimBar |
| `worker` handler `trimconcat` | crash-isolated multi-segment cut+concat (reuses ffmpeg arg builders) |

Reused as-is: `mediatools.Resolve`, `sysexec.Hide`, `transcode.Job/Preset/Args`, `jobs.Hub`,
`worker.Supervisor`, `recorder.Recording/Track`, `libdb.SetRecording`, `waveform.go` scrubber,
`oto` (audio out).

## Risks & mitigations

- **A/V sync drift** → audio-as-master clock + frame-drop/hold; never block the UI on decode.
- **Seek latency** on long files → fast pre-`-i` `-ss` + short post-seek; show a preview frame
  immediately (FrameAt) while the stream re-spins.
- **CPU** of rawvideo decode → ffmpeg scales to canvas size (not source res); cap fps; pause decode
  when hidden/paused.
- **Stream-copy cut accuracy** → clearly label "fast = keyframe-snapped"; offer precise re-encode.
- **Codec coverage** → ffmpeg handles everything OBS writes (H.264/AAC/mp4/mkv); ffprobe gates.
- **oto PCM streaming** → need a custom PCM source feeding oto (beep can wrap a raw PCM reader);
  validate buffering/underrun handling early in Phase 1.

## Verification (per phase, via the rave-mate ctl + a real OBS recording)

- P1: open a recorded set, play/pause/seek/scrub; check A/V sync + clean teardown (no orphan
  ffmpeg). `snapshot`/`screenshot` the player.
- P2: confirm markers land on track boundaries; prev/next jumps to track starts.
- P3: mark 2 keep-segments → export fast + precise; verify output plays, duration matches, and the
  edit registers as a new SetRecording.

## Resolved decisions

1. **Placement: BOTH** a dedicated "Player" main tab AND a detachable modal/pop-out (open the
   current player in its own window). The view body is shared; the tab embeds it, the pop-out hosts
   the same widget in a `fyne.Window`.
2. **Cut mode:** the **editing view** defaults to **fast** (stream-copy semantics) for marking +
   previewing cuts. The **actual export re-encode reuses the existing transcode system** with the
   builtin + user-saved presets - i.e. Phase 3 hands the keep-segments to the transcode preset
   picker; no separate encode path. (So "fast vs precise" is just: copy-export vs. encode-with-a-
   chosen-preset, and the preset side is the existing system verbatim.)
3. **Scope:** Recordings **+ any file** the user opens. Recordings get tracklist markers from the
   DB; an arbitrary file falls back to its `.cue` sidecar (if any) for markers.

## Added requirement - mouse back/forward navigation

Support the mouse **back/forward (X1/X2) buttons** for navigating rave-mate like a browser
(history of visited tabs/views; back returns to the previous view, forward re-advances). Maintain a
small in-app nav stack (tab + sub-view), push on navigation, and bind X1→back / X2→forward.
NOTE/feasibility: Fyne's public mouse API exposes Primary/Secondary/Tertiary only; the X1/X2
buttons come through GLFW (buttons 4/5) but may not be surfaced by Fyne's driver - needs a probe.
Fallbacks if unexposed: a small driver shim / `glfw` mouse-button callback, or `Alt+←/→` keyboard
equivalents. Tracked as a separate small feature (not gated on the player phases).
