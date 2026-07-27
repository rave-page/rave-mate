# zigmedia increment 4 (produce paths) - status

Branch `feat/zigmedia-inc4` off `development` @2b5babf. Spec: `ZIGMEDIA_DESIGN.md` §11 + §12.4.
Ground truth for as-built seams: the inc-1/2/3 STATUS notes.

Increment 4 as written has three parts. Both **parked audit findings are fixed**; the headline
"render straight into a D3D11 texture" target is **refused on measurement**, and the rename is
**deferred with a reason**.

| # | item | verdict |
|---|---|---|
| a | `webcam/framepipe.go` per-frame allocation (§12.4 item 1) | **fixed** - pooled + refcounted |
| b | `spout_shim.cpp` per-frame CPU transpose (§12.4 item 2) | **fixed** - row-wise, 5.2× faster at 4K |
| c | produce paths render/receive straight into a D3D11 texture | **NOT built** - unreachable as stated *and* slower than what it replaces |
| d | `rave-mate-enc.exe` → `rave-mate-media.exe` rename ("cosmetic") | **deferred** - unverifiable release plumbing, zero functional gain |

## (a) Capture buffers: pooled + refcounted

`framepipe` allocated a fresh full frame per capture and said so ("never reused") - ~250 MB/s of
garbage at 1080p30, the opposite policy from the rest of the media plane.

The reason it was not already a pool is **ownership**: one captured frame fans out to the local
preview sink AND N network taps, each of which can drop it independently, so the buffer's lifetime
is a refcount, not a scope.

- `videoshare.PixRef` - shared buffer, returned to the bounded pool exactly once by the last
  consumer; release-past-zero is reported, never acted on. `pooled=false` marks a buffer the
  ceiling refused, so releasing it just drops the reference.
- `framePipe` takes a caller-supplied buffer source and states the contract: **emit TAKES the
  buffer; anything not emitted goes to recycle.** Every shutdown path (torn tail, read error,
  emit-says-stop) is covered by a test that counts `alloc == emit + recycle`.
- Reference discipline: taken **before** queueing to a tap (after would let a fast tap release to
  zero mid-fanout and the pool would hand the buffer to the next capture); displaced frames are
  released; `closeTaps`/`removeTap`/`Close` drain and release what is still queued - an abandoned
  buffer pins the pool's live ceiling for the life of the process.
- **Fail OPEN at the ceiling**: allocate (counted as `PoolMiss`) rather than drop every frame. A
  leaked reference then degrades the optimisation instead of wedging a live camera.

Gates (all deterministic): 200 frames use ≤4 distinct buffers; 300 *dropped* frames all come back
(pool live bytes return to baseline); `refBugs` 0. Both fail on the old policy - **200/200 and
300/300 distinct buffers**.

## (b) Send-path flip: row-wise, not per-pixel

`RAVE_SPOUT_FLIP != 0` used one scalar 4-byte `memcpy` per **pixel** inside a doubly-nested loop:
8.3 M libc calls per 4K frame. Now the vertical component is a whole-row `memcpy` in reverse row
order and only a horizontal mirror touches pixels (32-bit row reverse). `flip == 0` (the default)
still passes the caller's buffer straight through with no host pass.

Measured per frame at 4K:

| mode | before | after |
|---|---|---|
| vertical | 11.33 ms | **2.16 ms** (2.9 → 15.3 GB/s) |
| horizontal | 11.28 ms | **2.91 ms** |
| both | 10.86 ms | **2.98 ms** |

At 4K60 a vertical flip was costing **68 % of one core's frame budget** for a configuration option.
(The "before" figure is a faithful Go port of the original algorithm benchmarked against the real C
implementation of the new one - the ratio is the algorithmic delta, not a C-vs-C measurement.)

Correctness is gated **deterministically**, not on the live rig: the shim's transform is exported as
`rave_spout_flip_rows` and compared byte-for-byte against that reference over 8 geometries × 4
modes, plus identity/involution and an undersized-buffer guard. Pixel math deserves a pixel-math
instrument.

`TestFlipLiveOrientation` additionally establishes, for the first time, what `RAVE_SPOUT_FLIP`
actually does end to end (publish → zero-copy capture → encode → decode → sample quadrants):
`none` as painted, `v` rows reversed, `h` columns mirrored, `hv` both.

**Measured but deliberately NOT adopted:** Spout's own `bInvert` does the vertical flip inside the
GL/DX interop copy (free, no host pass) and decodes to *exactly* the same quadrants as the CPU path
- verified once the harness was reliable. It stays unused because the CPU transform can be **proven**
byte-identical by a unit test whereas `bInvert` can only be checked on the live rig, and this
SpoutLibrary pairing has already shown skewed late-vtable behaviour in three separate places
(`GetSenderWidth`/`GetSenderHeight`, `GetHandle`, `GetSenderFrame`). An upside-down output is
user-visible breakage; 2.16 ms/frame is not. The shim comment records the finding and names the gate
to re-run if anyone wants those milliseconds. **This is a cheap call to overrule.**

## (c) D3D11 produce path - refused, on two independent grounds

The design's rationale was that it "deletes both the GL upload and `spout_shim.cpp:81`'s per-frame
malloc". The malloc was already gone before this increment, and (b) has now made the transpose
row-wise - so the per-frame **GL upload** is the only cost left to remove.

**1. Not reachable as stated.** `SPOUTLIBRARY` can only create a sender's shared texture through
`SendImage`/`SendTexture`, both of which need a GL context - which is exactly why inc 2 had to
publish one zeroed frame to force the texture into existence before handing its handle to the child.
So GL cannot leave the produce path; only the *per-frame* upload could.

**2. The replacement is slower.** Measured cost of the per-frame GL upload (`TestSendCost`):

| geometry | per frame | throughput | share of a 60 fps budget |
|---|---|---|---|
| 1280×720 | 0.70 ms | 5.0 GB/s | 4.2 % |
| 1920×1080 | 0.98 ms | 8.1 GB/s | 5.9 % |
| 3840×2160 | 3.62 ms | 8.7 GB/s | 21.7 % |

8.7 GB/s is host→GPU transfer speed - the upload is already running at hardware rate. A `dir:"pub"`
session would replace it with **host→SHM memcpy + `UpdateSubresource` + `Blt`**: ~3.2 ms for the
extra host copy alone at 4K, plus an upload in the same class. Even with the producer writing
directly into the SHM frame slot (removing that copy) it is a wash - for the price of a third
protocol direction, a new failure ladder, and per-sender session management.

So the honest inc-4 content was the two parked defects, and both are fixed. Recorded here so the
next agent does not rebuild the case from scratch.

## (d) `rave-mate-media.exe` rename - deferred

The design calls it "cosmetic". Blast radius measured: **59 references across 25 files**, including
two GitHub workflows (`nightly.yml`, `release.yml`), the NSIS installer, the `native/zigenc/`
directory itself, `build-zig.sh`, embed staging, `RAVE_MATE_ENC_EXE`, and every test helper.

Left undone because: zero functional gain; the CI workflows and the installer cannot be verified
from here (the failure mode of getting it wrong is a release that ships no encoder child); and it is
cleanly separable - it is a mechanical rename that wants its own commit, done by someone who can
watch a CI run and an installer build. The one non-mechanical part, should anyone pick it up: an
already-installed sidecar is named `rave-mate-enc.exe`, and `encExePath()`'s beside-the-exe rung
must accept BOTH names or a self-updated install silently loses the native engine (self-update
replaces only `rave-mate.exe`).

Good news for whoever does it: `internal/mfenc/embedded/rave-mate-enc.exe` is **git-ignored** (only
its README is tracked), so no binary needs to move.

## Also fixed

- **`go vet -tags spout` was never being run** - the untagged sweep does not compile those files. It
  immediately found duplicate JSON tags in my own inc-2 grabber struct (`TopR, TopB int
  \`json:"tr"\`` gives BOTH fields the tag, so `TopB`/`BottomB` decoded from `tr`/`br`). Fixed, and
  that lane is now in the gate list.
- **Capture counters now reach a surface.** `capStats.Frames/Dropped/PoolMiss` existed and were
  rendered nowhere - the same blind spot the route panel had before inc 2. `webcam.Status` carries
  them and both renderers print them; a release-past-zero logs ERROR once (it means one buffer had
  two owners).

## Verified live on this box

```
go test -tags spout ./internal/mfenc     -run 'TestZeroCopyLive|TestDecodeLive|TestInc3|TestFlipLive'   PASS
go test -tags spout ./internal/videoshare                                                              PASS
TestFlipLiveOrientation: none → TL=red TR=green BL=blue BR=white
                         v    → TL=blue TR=white BL=red BR=green
                         h    → TL=green TR=red BL=white BR=blue
                         hv   → TL=white TR=blue BL=green BR=red
TestSendCost:            720p 0.70 ms · 1080p 0.98 ms · 4K 3.62 ms per frame
```

inc-1/2/3's live gates all still pass, so nothing in this increment regressed the capture, decode or
affinity paths.

## NOT verified

- **The webcam path was not driven with a real camera.** The buffer-ownership work is gated by
  deterministic tests over `deliver`/`fanout`/`framePipe`/`Close`, not by a live dshow capture (no
  camera on this box). The ffmpeg child, its argv and the supervision loop are untouched.
- **`PoolMiss` fallback under a genuinely exhausted pool**: the ceiling test logs that the pool still
  had room for the small geometry, so the fallback branch is covered by construction, not by
  execution.
- **deckcard / vrslgrid produce paths** were not touched at all. They publish through the same
  `FrameSender`, so they inherit (b); their rasterisers were left alone because (c) is refused.
- **A 4K60 webcam soak** (RSS flatness with the pool in place) - not run.

## Findings worth carrying

1. **A flaky rig can fabricate a clean-looking result.** The bInvert experiment first "proved"
   `SendImage(bInvert=true)` publishes black - twice. It was the stale-sender flake: a Spout sender
   name reused after its publisher died hands the next capture the DEAD texture, which reads as a
   blank frame with zero errors anywhere (risk R1, exactly as designed). Per-mode **and per-attempt**
   sender names removed it; with a reliable harness bInvert turned out to work perfectly. The
   inc-3 lesson (per-arm sender names) was necessary but not sufficient - attempts need it too.
2. **Pixel math wants a pixel-math instrument.** Two live runs disagreed about the same C code.
   A deterministic byte-for-byte comparison against the previous algorithm settled it in one run and
   is now the gate; the live test only checks the semantics the unit test cannot see.
3. **Check the build tags your linters actually compile.** `go vet ./...` had never looked at any
   `spout`-tagged file, and there was a real bug waiting in there.

## Neighbour defects (reported, not fixed)

1. `videoshare/sender_spout_test.go:29` `TestSpoutSenderRegisters` hard-fails without
   `SpoutLibrary.dll` where every other Spout test skips cleanly (reproducible on `development`).
2. `internal/dmx` `TestRouterIngestToGrid` binds a fixed UDP port (16454), flaky in parallel.
3. `mediapipe/decode.go` `decFreeRing` parks 4 raw frames = 33 MB at 1080p but **132 MB at 4K** per
   receive route, where the native decode path parks 4 MiB. Bounded and correct per its own comment.
   **Still left reported:** it is the ffmpeg *fallback's* cost and belongs with the readback-path
   retirement (increment 5), not with the produce paths.
4. **Rapid Spout sender churn hands consumers dead textures** (the flake above). Our own code is
   protected by the R1 oracle on a 2 s tick, so a *route* recovers; anything sampling faster than
   that can read a blank frame. Worth knowing before the 2-PC pass blames the wire.

## Gate results (verbatim)

```
gofmt -l .                                                                      (clean)
GOWORK=off go vet ./...                                                         (clean)
GOWORK=off go vet -tags spout ./...                                             (clean)   ← new lane
GOWORK=off go test ./...                                                        all ok
GOWORK=off go build -tags "zigdsp zigui zigvr encembed" ./...                    TAG1-OK
GOWORK=off go build -tags "spout vr zigdsp zigui zigvr encembed" ./...           TAG2-OK
bash scripts/build-zig.sh                     rave-mate-enc built + embed-staged (0.16.0)
zig fmt --check src/                                                            clean
zig build test --summary all                              3/3 steps; 9/9 tests passed
go test -tags spout ./internal/videoshare                                       PASS
go test -tags spout ./internal/mfenc -run 'TestZeroCopyLive|TestDecodeLive|TestInc3|TestFlipLive'  PASS
```
