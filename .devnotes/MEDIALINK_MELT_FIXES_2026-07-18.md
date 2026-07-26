# Medialink spout-over-peerlink melt - fixes (2026-07-18)

Incident: source PC unresponsive → hard reset mid-set while sharing a Spout source over
peerlink. Spout→NDI with the same source was fine.

## Root causes → fixes (shipped a57243c)

| Cause | Fix |
|---|---|
| Raw NRGBA route (no codec overlap / ffmpeg missing / probe unfinished): AES-GCM + TCP on every full frame, 0.5-2 GB/s | raw video > 320×240 refused BOTH sides (`rawVideoOK`); requester errors at the click, sender refuses with reason; toast in `mediaReceive` |
| libx264 at native 4K60 pins all cores | `NegotiateCodecFor(pixelRate)`: tier-4 SW encoders only ≤1080p60; above → mjpeg tier (NDI-class SIMD intra) at native res |
| Per-frame 8-33 MB alloc+copy in capture | zero-copy readback handoff + `videoshare` pix pool; release via `Frame.Release` (encode feed + runSend; rebuf retains → no release) |
| No fps/res governance | `MaxFPS` (0=60) dropped at source pre-encode; `MaxHeight` policy 0=auto → native on HW tiers, 1080p ONLY on tier-4 SW; -1=never; Settings → Streaming |

Product rule (user directive): hardware encoders take native 4K - never downscale by
default; the cap exists only for the x264 fallback + explicit bandwidth saving.

## Still open

- **mfenc (in progress)**: native MF hardware encoder replaces the ffmpeg child+pipe;
  Phase B = D3D11 shared-texture direct source (share handle already available via
  `GetSenderInfo`) - true zero-copy, no CPU readback at all.
- Per-subscriber duplication: N peers on one source = N capture+encode stacks (cap 8).
  Needs capture fan-out sharing.
- ~~`MediaLink.Subprocess` isolation child: still default-off~~ → default ON since 2026-07-26 (see below); cross-PC rig pass still outstanding.
- `mediaroute.scan` builds/releases a Spout runtime object per `ListSenders`/`SenderSize`
  call every 2 s - cacheable.

## 2026-07-26 - QoS wave (branch `feature/medialink-qos`, WP-4 + WP-6)

User demand restated: *"the spout over network stream is killing my sending pc's performance
still … make this performant, not kill my whole system and other media encoding streams!"*

### Job/priority discipline (WP-4.1)

`sysexec.AssignToJob(p, bool)` gains a class API (`AssignToJobClass` / `JobClass`); the bool
keeps its old meaning (false→realtime, true→batch).

| Class | Cap | Children |
|---|---|---|
| `JobRealtime` | none | medialink encode + decode (`mediapipe`) |
| `JobBatch` | 10% aggregate hard | codec-probe test-encodes, gridfix engine+trainer, worker background pool |
| `JobMedia` | 70% aggregate hard | webcam capture, vrslstream, mocap ffmpeg |

Why: encode/decode sat in the shared 10% bucket. Capping a REALTIME encoder saves nothing -
the sender still pays full Spout readback + pipe writes for frames the starved child never
drains, which is the melt. Bounding cost belongs upstream (fps/pixel-rate gates). Precedent:
`mediaplayer/player.go` spawns realtime children uncapped. Live captures also no longer share
one 10% bucket with a gridfix sweep (they were throttled into dropping frames), but stay
bounded so a runaway ffmpeg can't take the box.

### Probe gating (WP-4.2)

The §3.2 probe test-encoded every HW encoder 4-concurrently at launch, ungoverned - each test
encode takes a real NVENC/AMF session, so starting rave-mate mid-stream could fail OBS's own
encoder. Now (`internal/app/mediacaps.go`):

- governor forbids background work → `mediapipe.ProbeListing` (listing only: `-encoders/
  -decoders/-hwaccels`, no GPU work, no session) is advertised with `Caps.Validated=false`,
  then the goroutine parks on `governor.WaitWhileBusy` and upgrades to the validated set when
  the stream ends. Deferring the ADVERTISEMENT itself would be worse: a sender with no
  encoders makes the far end refuse the route, or fall back to raw video.
- Listing results are cached separately - they never poison `mediapipe.Cached()`, so
  "encoders working" in diagnostics still means test-encode-proven.

### PlanEncode wired into the real decision (WP-4.3)

`encoderscan.PlanAdvertise` (new, pure + table-tested) now shapes what we advertise, and
re-runs on every governor STREAMING edge (focus/drag edges ignored - they flap and a re-plan
costs a process+PDH scan). Unchanged lists are not re-pushed (`SetCodecCaps` re-advertises to
all peers).

- **WITHHOLD** an encoder whose silicon a CRITICAL consumer holds (OBS live / Parsec), but
  ONLY when a non-protected HARDWARE encoder remains. Never degrade to software-only or to an
  empty list: a CPU tier at 1080p60, or a refused route falling back to raw, hurts the machine
  more than sharing an encode engine. `ActionPause` never empties the advertisement either.
- **ORDER** = plan pick first, then descending headroom. NOTE: `medialink.NegotiateCodecFor`
  treats the advertised list as a SET (tier order is fixed in `codecTiers`), so ordering has
  no effect on today's negotiation - it is the seam the engine wave (sender-side preference)
  builds on. Only withholding changes the outcome right now.
- Real inputs added: per-adapter free VRAM from DXGI `IDXGIAdapter3::QueryVideoMemoryInfo`
  (Budget-CurrentUsage, LOCAL segment - already nets out what OBS/Parsec hold; pure syscall,
  no dep), and each encoder bound to a real adapter by VENDOR NAME (nvenc→NVIDIA, amf→AMD/
  Radeon, qsv→Intel) so LoadPct/VRAMFree are that device's numbers instead of "the busiest".
- **Stays unknown**: HW encode SESSION counts (`Device.Sessions = -1` always) - NVML/AMF/
  oneVPL only, and no new deps; the planner skips that ceiling. Ambiguous joins (two
  same-vendor GPUs) and signature-less families (MF wraps any MFT) fall back to busiest-adapter
  load + unknown VRAM. Exact per-encoder LUID pinning is the device-selection wave's job.
- `ctl encoder-scan` now reports the ADVERTISED set with per-candidate adapter/load/free-VRAM
  + the withheld list, instead of reading the in-proc router (which holds no caps at all once
  the plane is isolated). Its stale "VRAM+session probes next" note is corrected.

### Media plane isolated by default + child backstops (WP-6)

`features.mediaLink.subprocess` is tri-state (`*bool`): absent = ON, explicit `false` =
legacy in-proc, explicit `true` = ON. Migration is provable: the pre-flip schema was a plain
bool WITH `omitempty`, so an existing file can only carry `true` or nothing - no user is
silently pinned to the old path, and a hand-edited `false` now survives save/load (tested).

The reason the default had to move: the governor demotes the WHOLE daemon to below-normal
while a stream is live, which throttles in-proc routes exactly when it matters. `governor`'s
package doc claimed the media plane already ran in a child - now true, with the in-proc
escape hatch documented as the exception.

Also: `HeartbeatTimeout: 5s` on the media Host (the child beats from its 1 Hz telemetry loop;
a wedged cgo capture stops beating while still holding the camera), and the job-object RAM cap
rides the spawn snapshot so the child sets its own Go soft limit at 80% of it - the GC fights
a frame runaway before the OS kills the process.

### Needs the 2-PC rig (NOT verified here)

- Subprocess-by-default cross-PC routing: frames actually pulled through the child,
  caps/secrets propagation, SMPTE TC still in the media-clock domain. #44 Phase 3 verified
  spawn/respawn/telemetry single-PC only; this wave flips the default before that rig pass.
- That a realtime (uncapped) encode child measurably restores sender FPS/CPU headroom vs the
  10%-capped one, and that OBS's stream is unharmed with a route live.
- Withholding behaviour with a REAL live OBS stream: PDH `AdapterEncPct` + `ProtectedFamily`
  populated, nvenc withheld, route negotiated onto the other adapter.
- Listing-only advertisement path: start rave-mate mid-stream, confirm no test encode runs
  (`ctl encoder-scan` note), routes still negotiate, and the validated probe replaces it when
  the stream ends.
