# zigmedia increment 5 - status: zero-copy is the default

Branch `feat/zigmedia-inc5` off `origin/development` @08b144f. Spec: `ZIGMEDIA_DESIGN.md` §11
("retire the Go readback path") + the promotion gates set by the coordinator.

User directive, verbatim: *"yes, i agreee zero copy should be the default"*.

## Verdict: capture + decode promoted, affinity held back

| flag | before | after | why |
|---|---|---|---|
| `mediaLink.zigCapture` | OFF | **ON** | all promotion gates satisfiable on this hardware are green, by execution |
| `mediaLink.zigDecode` | OFF | **ON** | the path it replaces is MEASURABLY the ceiling: ~13.5 distinct frames/s republished at 4K against 37 encoded |
| `mediaLink.zigAffinity` | OFF | OFF | the re-place is live-verified only between two IDENTICAL GPUs |

Migration is safe by construction: the key is `omitempty` on a `*bool`, so a pre-flip config
carries either `true` (someone opted in) or no key at all, and only an EXPLICIT `false` opts out.
Same argument `MediaSubprocess` recorded for its own flip. `TestZeroCopyOptOutRoundTrips` pins that
an opt-out actually persists - a plain `bool` with `omitempty` would have dropped it and silently
handed the user the default back.

**The readback was NOT retired.** §11 says "delete `pool.go`, `pixRef`/`captureHub` fan-out, the
four duplicated newest-wins loops". None of that is deleted, and it should not be: the readback is
the fallback for every source that has no shared texture (webcam / DirectShow), it is the parity
oracle every zero-copy gate is measured against, and it is now (post-P0) the only Spout receive
path we have. Retiring it would remove the instrument that proves the default correct.

## Promotion gates, one by one

### 1. Per-source selection policy — GREEN (deterministic)

`zeroCopyOpts` now returns a `zcVerdict{request, applicable, reason}`. The half that matters once
the flag is default-on is `applicable`, which separates the two kinds of "no" that must not be
logged or counted alike:

- a webcam / DirectShow / non-Spout source has no GPU shared texture AT ALL, so the readback is
  not a downgrade - it is the only path that ever existed. **Silent, uncounted.**
- a Spout source that COULD have qualified and did not (DX9 or CPU-memoryshare sender, a sender
  that resized between advert and open, a sender pinned to the readback) is a real downgrade: ONE
  warn naming the reason, and counted.

Before this, all of those returned "no" silently and three did not even count a downgrade.

`TestZeroCopyPolicyIsPerSource` states the gate as one assertion: with the flag ON, a Spout source
and a webcam source resolved by the SAME process get different verdicts - so the decision cannot
have come from the flag, the config or the peer's advert.

### 2. Clean degrade, never a black frame — GREEN

Two halves. The ladder already existed (open-side refusal → readback; mid-route `srcgone`/staleness
→ recycle, then pin; mid-route native failure → in-place ffmpeg substitution). What was missing was
the second half - *"surface it in the rendered stats"*:

**`PipelineStats.DegradeReason` was rendered NOWHERE, in either panel.** The field whose own doc
comment says *"EMPTY is the only healthy value - a degraded route that reports nothing is the worst
failure shape there is"*. Nor were `AUBytes`/`AUCount`, the CONTENT oracle - the only numbers that
can tell a live picture from a black or frozen one. Both were collected from increment 1 onward and
died in the struct, which is exactly how a black route kept a healthy-looking panel for 12 minutes.

Both panels now render: bytes/frame, "no picture content" at the noise floor, published volume,
encoder saturation, encode failures, software tier, poisoned hardware, degrade reason.

`AUNoiseFloorBytes = 1000` is MEASURED, not guessed: inc-3 M3 has a static 720p sender at 49 B/AU
and moving content at 184; the field's black 4K30 route sat at **255 B/frame** where real content
would be ~83,000; a healthy webcam route carried 3,169. It deliberately does **not** claim "black" -
black, frozen and genuinely static are indistinguishable in a bitstream (that is what an encoder is
for), so the wording names all three and NOTHING marks the route degraded on it.
`TestNoPictureContentThreshold` pins the verdict against those five measured numbers.

### 3. The deferred inc-1/inc-2 verifications — 4 of 6 now GREEN by execution

The instrument is new: `mf_testtex_create` (gate-only, documented as such, precedent =
`rave_spout_flip_rows`). The child resolves its handle from a Go callback, so a texture created
here - differing from a real Spout sender in exactly ONE property, the sync object or the format -
reaches branches no Spout sender on any rig can produce. No Spout, no registry, no
`SpoutLibrary.dll`, so these run in the plain `windows && cgo` lane.

| item | status | evidence |
|---|---|---|
| **R3 `IDXGIKeyedMutex`** | **executed, PASS** | `capFlags=0x3` - keyed bit SET, named bit CLEAR. `capFrames=120 capFPS=60.0 encBusy=0.38ms mtxTimeouts=0 srcErrors=0`, 121 AUs, decoded `top rgb(232,0,1) bottom rgb(2,0,243)`. R3's hazard (AcquireSync starving the session thread) does not reproduce. |
| **R4 TYPELESS / exotic** | **executed, PASS** | 4/4 refused with `(fmt_unsupported)` as an `ErrZeroCopyRefused` the caller downgrades on: `B8G8R8A8_TYPELESS`(90), `B8G8R8X8_TYPELESS`(92), `R8G8B8A8_SNORM`(31), `R16G16B16A16_UNORM`(11). Plus `dim_mismatch` for a 640×360 texture on a 1280×720 session. |
| **R1 sender restart (live)** | **executed, PASS** | texture swapped under a running session: `capFrames 72→235, downgrades 0→1`, 114 AUs from the first post-recycle keyframe, content `top rgb(19,255,8) bottom rgb(255,255,255)` = the NEW texture. |
| **R7 cross-adapter** | live on THIS rig | M4: adapter `0x19d93` **REFUSES** a sender `0x10371` **ACCEPTS**; the affinity re-place turns that into a live route (`capFrames=55 capFlags=0x5 adapterMoved=true`), with a control arm that still refuses without candidates. Heterogeneous GPUs still unexercised. |
| **2-PC wire pass, flag ON** | **NOT DONE** | only the user can drive two machines |
| **OBS restart / canvas resize** | **NOT DONE** | the R1 *mechanism* is now executed against a real texture swap; OBS specifically is not |
| **4K60 receive soak** | **NOT DONE** | deliberately not run - see "GPU contention" below |
| **7-day soak** | **NOT DONE** | wall-clock |

R1's non-vacuity is the finding worth keeping: with the changed-handle detector disabled, the same
run reports `capFrames 72→402` and `srcErrors=0` - **perfectly healthy counters** - while decoding
the DEAD texture's pixels. Both halves of the gate (the downgrade counter and the decoded content)
catch it independently.

### The decode flip, added on the coordinator's steer

The receive side was initially held back under the same "cannot prove it here" rule. The field
measurement reversed that: a 4K local republish delivers **~13.5 distinct frames/s while the source
encodes at 37**, because the CPU `SendImage` upload of 33 MB/frame is the capacity ceiling. The old
default was therefore not a neutral baseline - it was a measured 3x frame loss - and "do not ship an
unproven default" is not an argument for keeping a proven-degraded one.

What carries it: the published picture is verified end to end as of this branch (encode -> AU ->
native decode -> publish -> read back from a SECOND process with its own D3D11 device, correct row
AND channel order); every failure rung keeps real pixels (open refusal -> ffmpeg with one WARN;
mid-route `dstgone`/staleness -> recycle, bounded, then pin to the frame path); and the new
`route decode telemetry` line names which path is serving the route, with `publishedFps` beside
`outFps` so the ceiling - or a silent fallback - is readable from a log alone.

Open on the receive side, unchanged and recorded at the config field: no real end-to-end route
through `jitterbuf`, no 4K60 receive soak, no HEVC bitstream, no TRUE hardware decoder MFT here.

### 4. The readback stays correct as a fallback — GREEN

`go test -tags spout ./internal/videoshare` PASS with the DLL actually staged:
`TestRecvContentCarriesPixels` → `top(255,0,0) bottom(0,0,255) left(0,255,0)`,
`TestRecvContentStaysNonZero` → 30/30 frames with content, `TestSpout4KCaptureSoak` PASS.

### 5. Content gates, not metadata gates — GREEN

Every new live gate asserts decoded pixels or bytes/frame. The renderer gates assert the *rendered*
line, and the wiring is asserted through `fmtPipeLine` (not only through the formatter), so the
oracle cannot be "implemented" and left unplumbed.

## Also in scope, all done

1. **`decFreeRing` bounded in BYTES.** It was a FRAME-shaped cap on a geometry-dependent buffer:
   fixed depth 4, with a comment claiming "32 MB at 1080p". A 1080p frame is 8.3 MB, so 4 of them
   are 33 MB - and at 4K the same ring parked **132 MB per receive route**, where the native decode
   path parks 4 MiB. Now `decFreeRingBytes` (32 MiB) with the depth derived from the geometry: 4 at
   720p, 4 at 1080p, 1 at 4K, 1 at 8K. Never 0 - `getBuf` allocates on an empty ring (fail open,
   same policy as the capture pool's `PoolMiss`). `TestDecFreeRingIsBoundedInBytes` carries its own
   non-vacuity: it asserts the OLD policy exceeds the cap at 4K, so it cannot silently stop proving
   anything.

2. **`internal/ratewin`: one sliding window for four hand-rolled ones.** `mediapipe.rate` (OutFPS),
   `mfenc.counterRate` (CapFPS/DecFPS) and `mfenc.busyMean` (EncBusyMs/DecBusyMs) all closed their
   window ON READ over a 500 ms span. `Stats()` is polled by `emitTelemetry` (1 Hz),
   `mediaroute.cleanup` (0.5 Hz), the route-telemetry line (0.1 Hz) and both panels - so the value
   depended on which poller fired last, over a span short enough to fall between keyframe clumps.
   Bytes and frames now ride ONE ring, so bytes-per-frame cannot be two different spans divided by
   each other. **Non-vacuity, with the numbers:** restoring `Span=500ms` makes the clumped-volume
   gate report `12000 B/s against a true mean of 106523` - 1/8.9, the exact field signature that
   displayed 0.1 Mbps on a healthy route.

   *Behaviour change worth knowing:* these fields now read 0 for the first second of a session
   instead of reporting a 500 ms window someone else left behind. Gates that assert a RATE must give
   the window ≥ `ratewin.MinSpan` of OBSERVATION (`statsAfterRateWindow`), and a window must BRACKET
   a burst - priming after it measures an interval in which nothing happened and correctly reports 0.

3. **`spoutSink.Write`'s volume contract.** It returns `nil` for a frame it threw away - an
   error-shaped contract over a volume-shaped operation - so "route up, frames arriving, nothing
   published" was unsayable (a sink that drops nothing and publishes nothing reports the same zero
   as a healthy idle one). `PubFrames`/`PubBytes` now ride the wrapper chain via `InnerPublished`,
   and both panels render `published N`, or `published 0 - nothing reached the Spout sender` once
   frames really are leaving the decoder.

4. **NACK raw-video carve-out (§12.1) - RE-EVALUATED, verdict: it STAYS.** The design called it
   "an output-visible protocol feature switched off to relieve the allocator". It is not:
   - the feature was never off for the routes that matter. Every compressed route, zero-copy
     included, hands its AUs over with `Release == nil` and they are retained verbatim.
     `TestZeroCopyAUsEnterTheNACKWindow` pins it (8 AUs in, seq 3-6 retransmittable by identity).
   - what is excluded is raw video only, for reasons that survive the allocator's removal: one 4K
     frame evicts the whole 16 MB window, and raw frames are intra so the receiver resyncs on the
     next one - retransmitting a stale 33 MB frame is strictly worse than skipping it.
   - inc 2 had already re-keyed the test from ownership to RAW, which removed the actual bug.

   The oversized-pooled-AU rung's rationale is restated on protocol grounds so a future agent does
   not delete the rung along with the pool.

5. **`rave-mate-media.exe` rename: NOT done**, per instruction. Untouched.

6. **`route decode telemetry`** (new): one line per 10 s carrying `decode: native|ffmpeg`,
   `published`, `publishedFps`, `outFps`. Now that native decode is a default, a silent fallback to
   the frame path would make "default on" unfalsifiable on a box with no toolchain. Both paths report
   `PubFrames` through the same wrapper chain, so `publishedFps` beside `outFps` IS the before/after
   instrument for the 33 MB/frame ceiling - no probe tool required.

## The increment-2 §10 blocker, unblocked (and it was two instrument bugs)

Increment 2 recorded *"Spout's own receive side cannot see a foreign-device write on this rig"* and
abandoned its cross-process picture gate, filing a product question as an instrument failure. The
P0 vtable work later explained one half (`ReceiveImage` really dispatched to `ReceiveTexture`). With
that fixed the gate was STILL blank, and there were two more instrument bugs - both found by adding
controls rather than by reasoning:

1. the harness reused a **FIXED Spout sender name**, which hands the reader the previous publisher's
   DEAD texture (this repo's own R1 / inc-4 lesson, never applied here);
2. the harness published **ONCE**. A single `SendImage` is not visible to another process's D3D11
   device - the GL/DX interop write is not flushed until further GL work is submitted. This was the
   actual cause: 40 sends at 10 ms and both arms read back. **Every real sender publishes
   continuously, so the PRODUCT was never affected; only the instrument was.**

The oracle test now has two arms (plain `FrameSender` vs the eagerly-created `SharedSender`) so
"blank" can no longer be attributed to the wrong component - the mistake inc 2 made.

Result: `read-back oracle validated on the frame path: top r=255 b=0, bottom r=0 b=255` and
`published bands: top r=255 b=0, bottom r=1 b=255` - a SECOND PROCESS with its own D3D11 device
reading the natively-decoded picture with correct row and channel order. That is §10's requirement
discharged at the independent-consumer level (the mechanism OBS's Spout input uses). Real OBS and
Resolume still need a human, which is why `zigDecode` did not flip.

## Findings worth carrying

1. **A gate that is only *usually* right is worse than no gate.** My own R1 gate passed twice and
   then reported the OLD texture's colours: the AU tail was sliced by INDEX after a fixed 5.5 s
   sleep, so a late recycle put the slice across the boundary and ffmpeg decoded a pre-recycle
   frame. A decoder handed a stream starting mid-GOP shows the PREVIOUS picture - that is the whole
   mechanism. Fixed by keying on the recycle EVENT and taking the boundary from the forced IDR. The
   flaky version was also a data race (reading `len()` while the drain goroutine appended).
2. **"The instrument cannot see it" needs its own control before you believe it.** Twice now this
   epic has attributed an instrument bug to the product (inc 2) or the reverse (the P0 canary), and
   both times the deciding move was a positive control that took minutes to write.
3. **A default flip is mostly a VISIBILITY problem.** The code that carries the pixels was already
   good; what was missing was that a rig which silently downgrades, or ships a still frame, or ships
   black, looked identical to a healthy one in the rendered panel. Two collected-but-unrendered
   fields were the whole difference.
4. **Match the gate's fixture to the product in exactly one property.** `mf_testtex_create` had to
   copy Spout's `BIND_SHADER_RESOURCE | BIND_RENDER_TARGET` before the VP would accept the view
   (measured: `view_failed`); differing in two properties would have made a refusal ambiguous.

## GPU contention: what was NOT run, deliberately

The user was driving a live 2-PC 4K Spout route on this box throughout. Long/heavy GPU work was
therefore avoided: **no 4K60 soak, no receive soak, no multi-session saturation run.** A measurement
taken against someone else's live route is not a measurement, and stealing the GPU would have
corrupted their verification too.

## Neighbour defects found, NOT fixed (reported)

1. ~~`TestFlipLiveOrientation` is flaky under GPU load~~ **FIXED here, and it was the same root
   cause as the decode oracle.** Inc 4 diagnosed the blank captures as the R1 stale-texture symptom
   and added a 4-attempt retry loop. They were not stale textures: the harness opened the capture
   the instant `SenderShare` resolved - which happens on the publisher's FIRST `SendImage`, before
   the GL/DX interop write is flushed to a foreign device - and then decoded the FIRST captured
   frame, i.e. the one frame guaranteed to be racing the flush. Under GPU load the race is lost.
   Fixed by letting the publisher get several frames out and sampling the LAST decoded frame instead
   of the first: 3/3 runs, all four modes exact, and the retry loop now never fires. One mechanism,
   misdiagnosed in three separate increments (inc 2 as "Spout cannot see foreign writes", inc 4 as
   "stale sender texture", and it very nearly ate this increment's decode gate too).
2. `native/zigenc/src/cap.zig:273` comments `86` as `B8G8R8A8_TYPELESS`. It is `90`; 86 is a
   different format. Both are outside the allowlist so the test still passes - the label is wrong,
   not the logic.
3. `videoshare/sender_spout_test.go:29` `TestSpoutSenderRegisters` still hard-fails without
   `SpoutLibrary.dll` where every other Spout test skips cleanly (pre-existing, reproducible on
   `development`).
4. `internal/dmx` `TestRouterIngestToGrid` binds a fixed UDP port (16454), flaky in parallel.
5. `zig fmt --check native/zigui/src/` fails on `root.zig` and `wire_gen.zig` - pre-existing on
   `origin/development`, and neither file is touched by this branch (`git diff --stat
   origin/development..HEAD -- native/zigui/` is empty).
6. `mediapipe.rate`'s `perFrame`/`totals` are only consumed by `mfBridge`; the ffmpeg `encoder` and
   `decoder` still call plain `tick()`, so their routes render no bytes/frame. Not wrong (they have
   no AU byte counter), but the content oracle is therefore native-engine-only.

## Gate results (verbatim)

```
gofmt -l .                                                                      (clean)
GOWORK=off go vet ./...                                                         (clean)
GOWORK=off go vet -tags spout ./...                                             (clean)
GOWORK=off go build ./...                                                       OK
GOWORK=off go test ./...                                                        EXIT=0, 162 ok, 0 FAIL
GOWORK=off go build -tags "zigdsp zigui zigvr encembed" ./...                    TAG1-OK
GOWORK=off go build -tags "spout vr zigdsp zigui zigvr encembed" ./...           TAG2-OK
bash scripts/build-zig.sh              ravezig+rave-probe / raveui+rave-shell / ravevr /
                                       rave-mate-enc built + embed-staged (0.16.0)
zig fmt --check src/  (native/zigenc)                                           clean
zig build test        (native/zigenc)               3/3 steps; 13/13 tests passed
zig fmt --check src/  (native/zigui)   src/root.zig, src/wire_gen.zig  EXIT=1  ← PRE-EXISTING
zig build test        (native/zigui)                232/232 tests passed
GOWORK=off go test -tags zigui ./internal/webui -run TestZig                     ok
go test ./internal/mfenc -run TestZeroCopyRisk                 PASS (3/3 consecutive runs)
go test -tags spout ./internal/videoshare                      PASS  (DLL staged - not a skip)
go test -tags spout ./internal/mfenc
  -run 'TestZeroCopyLiveSession|TestDecodeLive|TestZeroCopyRisk|TestInc3'        PASS
```

The tagged Spout runs used a compiled test exe with `third_party/spout/bin/SpoutLibrary.dll` AND
`rave-mate-enc.exe` copied beside it. Without both, those gates SKIP silently - a green from them is
otherwise a skip, not a pass.

## What I need from the user (only they can do it)

Two machines, `zigCapture` now default-ON so nothing needs enabling. Isolated ctl ports, **never
47620/47622**.

1. **The 2-PC wire pass** (design §13.1). PC-A: OBS Spout out at 3840×2160@60, route opened from
   PC-B. 30 min. On A: `ctl perf` must show `zeroCopy=1` and `capFrames` rising - a downgrade
   warning now names its reason, and if it says `open_shared` on a multi-GPU box it will tell you to
   set `mediaLink.zigAffinity`. RSS of `rave-mate.exe` + media child + `rave-mate-enc*.exe` flat.
2. **The regression only a real rig shows:** while the route runs, is OBS's preview smooth and does
   the mouse pointer stay responsive on the SENDER PC? That is the documented symptom of hammering
   the sender's shared-texture mutex, and it is the one user-visible risk of this default. Reasoning
   says it should be fine (pacing never polls faster than the negotiated fps, acquires are bounded
   1..4 ms, `mtxTimeouts=0` even with 4×4K60 sessions, and the readback path acquires the SAME mutex
   at the shared capture's rate) - but reasoning is not a rig.
3. **OBS restart + canvas resize mid-route.** Kill OBS: the route should idle, then recover within
   one 2 s scan. Resize its canvas: the route should recycle and come back. Watch for a FROZEN
   picture with healthy counters - that is R1, and the panel now shows `downgrades N` and
   bytes/frame so you can see which happened.
4. **Read the new panel line and tell me if it lies.** A live route should show a plausible
   `N B/frame` (tens of kB at 4K); `no picture content (black, frozen or static)` on a route you can
   see moving would be a false positive worth knowing about.
5. If you have an **iGPU + dGPU** box, `RAVE_MATE_ZIGMEDIA_AFFINITY=1` there is the one measurement
   that would let affinity be promoted too.
