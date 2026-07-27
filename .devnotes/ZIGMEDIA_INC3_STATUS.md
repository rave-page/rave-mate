# zigmedia increment 3 - status

Branch `feat/zigmedia-inc3` off `development` @ed2f6de. Spec: `ZIGMEDIA_DESIGN.md` §11 (+ §6, §8.3,
R7, R12). Flag **OFF by default** (`config.MediaLink.zigAffinity`, env
`RAVE_MATE_ZIGMEDIA_AFFINITY`).

§11 is unusual: increment 3 is **"measurement-driven (do NOT pre-build)"** and two of its three
items exist only if a number says so. So this increment is one measurement suite + one build:

| # | item | verdict | why |
|---|---|---|---|
| 1 | one shared `CopyResource` per (adapter, sender) | **NOT built** | contention is measurably ZERO (M2); the 4K fan-out problem it would "fix" is encoder saturation, and building it re-adds 33 MB VRAM per group |
| 2 | frame-new gating | **NOT built** | mechanism unavailable (M1) *and* not worth it (M3: 49 bytes/frame) |
| 3 | adapter-affinity resolution | **BUILT + live-verified** | R7 reproduces on this rig (M4): one adapter refuses a sender the other accepts |

## Measurements (the numbers the decisions cite)

`go test -tags spout ./internal/mfenc -run TestInc3Measure -v` (needs `SpoutLibrary.dll` beside the
compiled test exe). Rig: 2× NVIDIA RTX 3060 (two adapter LUIDs), real Spout sender in a second
process.

**M1 - is the frame counter readable metadata-only?** Added `videoshare.SenderFrame` (a second
process-wide handle: `SetReceiverName` + `GetSenderFrame`/`GetSenderFps`; no GL context, no
`ReceiveImage`, no readback — the design's "metadata-only receiver").

```
frame=-1140774352 fps=180.0   ← constant across every call, 8/8 samples identical
```

`180.0` is this box's **monitor** refresh rate, not the 60 fps sender. That is the same late-vtable
skew the shim already documents for `GetSenderWidth`/`GetSenderHeight` on this SpoutLibrary pairing
(*"GetSenderWidth() dispatched to GetSenderName() and returned a truncated POINTER"*). **Item 2's
stated mechanism is not available.** The only other route named in §11 is parsing Spout's
frame-count shared memory, which §2.1 rejects outright (undocumented layout, silent-ABI-skew on
every SDK bump) — and M3 says it would not be worth it anyway.

**M2 - what does the Nth zero-copy session on ONE sender cost?** (§8.3's open measurement.)

| | 1 session | 4 sessions on one sender |
|---|---|---|
| 720p60 mean encBusy | 0.22 ms | **0.19 ms** |
| 720p60 capFPS / skips / mutex timeouts | 60.0 / 0 / 0 | 60.1 / 0 / **0** |
| 4K60 mean encBusy | 0.26 ms | 25.63 ms |
| 4K60 capFPS / skips / mutex timeouts | 60.0 / 0 / 0 | 37.8 / 73 / **0** |

At 720p the per-session cost *falls* with N. At 4K four sessions saturate — but **mutex timeouts
stay 0**, so the sender's access mutex is not the bottleneck; the encoder is. A shared capture copy
addresses the mutex, not the encoder, so it would buy nothing here while re-adding a private 33 MB
texture per (adapter, sender) — undoing inc-1's main VRAM win. The 4K degradation is also
*graceful and already visible*: `capSkips` rises, the pacing resyncs (never bursts), no errors. Per
§12.3 bounding that load belongs upstream in the fps/pixel-rate gates and `maxRoutes`, not here.
**Item 1 stays unbuilt, as §8.3 instructs.**

**M3 - what does a duplicate frame actually cost?** (R12.)

| sender | bytes/AU | at 60 fps |
|---|---|---|
| STATIC | 49 | 0.024 Mbps |
| MOVING (sweeping bar) | 184 | 0.09 Mbps |

§6's prediction ("a few hundred bytes/frame") is confirmed and conservative: **0.12 % of a 20 Mbps
route**. Duplicates also keep the peer's jitter buffer fed. **Item 2 would not be worth building
even if it were feasible.**

**M4 - cross-adapter.** Two adapters present; adapter `0x10540` **accepts** the sender (capFrames=42,
capFlags=0x5), adapter `0x194ec` **refuses** it (`open_shared`). R7 is real on this rig, so item 3
has a concrete refusal to fix.

## Shipped (item 3)

| work | where |
|---|---|
| affinity cache + bounded probe + policy rules | `mfenc/affinity_windows.go` |
| open ladder: source refusal → re-place; `AdapterLUID`/`AdapterMoved` | `mfenc/procparent_windows.go`, `procstats.go`, `mfenc_other.go` |
| candidates only under the gate + an UNRESOLVED device | `mediapipe/mf_bridge.go` |
| `PipelineStats.AdapterMoved` + both renderers + `en.json` | `medialink/pipeline.go`, `ui/`, `webui/`, `i18n/` |
| flag | `config/config.go` (`zigAffinity`) |
| metadata-only frame counter (built for M1, kept) | `videoshare/frame.go`, `frame_{spout,noop}.go`, `spout_shim.{h,cpp}` |
| gates | `mfenc/affinity_windows_test.go`, `mfenc/inc3_measure_spout_windows_test.go`, `mediapipe/zerocopy_gate_test.go` |

**How it works.** Nothing in DXGI answers "which adapter owns this share handle", so resolution is a
bounded probe: on a *source-side* refusal, try the other adapters once, first success wins, cache
the answer per sender. R7's rules, kept literally:

- **Never silently move adapters.** A move requires the caller to pass candidates, and mediapipe
  passes them only when the gate is on AND `EncodeSpec.Device()` resolves nothing. A device the
  user pinned — or the governor chose via `avoid-busiest` — is policy and outranks the
  optimisation. Every move emits one WARN naming both adapters and shows as "adapter re-placed" on
  the route panel.
- **Bounded:** one attempt per candidate; the NEGATIVE is cached too, so a sender no adapter can
  open costs the sweep exactly once and goes straight to readback afterwards; a non-source failure
  (poisoned tuple, crash-looping child) breaks the loop immediately instead of spawning a child per
  adapter for a rig that is simply broken; a single-adapter host gets no candidates at all.
- The cached adapter is probed FIRST, so the second route on the same sender pays no probe.

## Verified live on this box

`go test -tags spout ./internal/mfenc -run 'TestInc3AffinityLiveReplace' -v`

```
R7 live: adapter 0x194ec refuses this sender, 0x10540 accepts it
control (no candidates): mfenc: zero-copy source refused (open_shared)
re-placed session: adapter 0x10540 capFrames=54 capFPS=60.0 capFlags=0x5 encBusy=0.25ms
                   adapterMoved=true moves=1
```

The **control arm** is the point: the same open without candidates still refuses, so the re-place —
not an incidental retry — is what turned a readback downgrade into a live zero-copy route. inc-1's
`TestZeroCopyLive` and inc-2's `TestDecodeLive` both still pass.

## NOT verified

- **A move between two DIFFERENT physical GPUs.** Both adapters here are RTX 3060s; the refusal and
  the re-place are real, but a heterogeneous rig (e.g. iGPU + dGPU, where the re-placed adapter has
  different encoder capabilities or no encoder at all) is unexercised. The `break` on a non-source
  failure is the guard for "the other adapter has no usable MFT", and it is unit-tested, not lived.
- **Interaction with the load governor.** Affinity never fires under `avoid-busiest` (device
  resolved), so the two cannot fight — asserted by test, not by running the governor.
- **7-day soak / 2-PC pass** — still open from inc 1, untouched here.
- **`videoshare.SenderFrame` has no working consumer.** It is kept as the recorded evidence for M1
  and as the ready-made seam if a future SDK fixes the skew; nothing calls it in production.

## Findings worth carrying

1. **Two measurement harnesses were wrong before they were right.** A flat full-frame value shift is
   a DC offset the encoder predicts away, so the "moving" control initially read *identically* to
   the static arm (49 vs 49 bytes/frame); and one sender name shared by both arms left the first
   publisher alive, so both arms measured the same static sender. Per-arm sender names + a sweeping
   spatial bar fixed both. A control that agrees with the treatment is a broken control, not a
   result.
2. **`fps=180.0` was the tell** that M1 was reading the wrong vtable slot — a plausible-looking
   number that happened to be the monitor's refresh rate. Sanity-check magnitudes against something
   physical before trusting an SDK read.
3. **4 × 4K60 zero-copy sessions on one adapter degrade gracefully** (capFPS 60 → 37.8, capSkips 73,
   zero errors, zero contention). The pacing resync behaves exactly as §6 designed it, and the
   saturation is already visible in the counters the panel renders.

## Neighbour defects (reported, not fixed)

Unchanged from the inc-2 note; none fall inside increment 3's scope:

1. `webcam/framepipe.go` fresh full frame per capture — increment 4 (produce paths).
2. `spout_shim.cpp` per-frame scalar transpose when `RAVE_SPOUT_FLIP != 0` — increment 4.
3. `videoshare/sender_spout_test.go:29` hard-fails without `SpoutLibrary.dll` where every other
   Spout test skips cleanly (reproducible on `development`).
4. `internal/dmx` `TestRouterIngestToGrid` binds a fixed UDP port (16454), flaky in parallel.
5. `mediapipe/decode.go` `decFreeRing` parks 4 raw frames = 33 MB at 1080p but **132 MB at 4K** per
   receive route, where the native decode path parks 4 MiB. Correct per its own comment and
   bounded, but it is the ffmpeg fallback's cost. **Left reported:** increment 3's scope is the
   three §11 items, and this is neither — it belongs with the readback-path retirement (increment 5)
   or a standalone fix.

## Gate results (verbatim)

```
gofmt -l .                                                                      (clean)
GOWORK=off go vet ./...                                                         (clean)
GOWORK=off go test ./...                                                        all ok
GOWORK=off go build -tags "zigdsp zigui zigvr encembed" ./...                    TAG1-OK
GOWORK=off go build -tags "spout vr zigdsp zigui zigvr encembed" ./...           TAG2-OK
bash scripts/build-zig.sh                     rave-mate-enc built + embed-staged (0.16.0)
zig fmt --check src/                                                            clean
zig build test --summary all                              3/3 steps; 9/9 tests passed
go test -tags spout ./internal/mfenc -run 'TestZeroCopyLive|TestDecodeLive|TestInc3'   PASS
```
