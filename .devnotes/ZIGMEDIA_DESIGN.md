# zigmedia — realtime pixel plane in Zig

Port target: the ENTIRE hot pixel path of a medialink video route into the existing Zig
child, so no frame pixels ever touch the Go heap or a process boundary. Rationale is the
standing rule in ZIG_MIGRATION.md "Why Zig": Go's GC + scheduler is the wrong runtime for
hard-realtime video. Field proof (2026-07-26): a 4K60 Spout route OOM-killed the media
child through allocation churn over 33 MB frame buffers.

**This document is committed in two parts.** Part 1 (below) is the INCREMENT-1 SPEC —
frozen, implementable today. Part 2 (receive side, webcam/VRSL produce paths, full risk
register, increments 2-5) follows in a second commit; §9 lists what it will cover so
nobody implements into that gap.

---

## 1. Where the cost is today (sender path, 4K60)

```
Spout sender (DX11 shared texture, GPU)
  │  ReceiveImage  ── GPU→CPU readback, 2.0 GB/s ─────────── videoshare (cgo + GL ctx)
  ▼
Go pooled []byte 33.2 MB   getPix/PutPix (pool.go)          ← GC-dodge pool, OOM origin
  │  captureHub fanout + pixRef refcount + newest-wins chans
  ▼
spoutSource.Next → medialink.Frame{Payload: pix}
  │  mfBridge.feed
  ▼
ProcSession.Encode: memcpy 33.2 MB → SHM frame slot, 2.0 GB/s   ← second full-rate copy
  │  SetEvent(-f)
  ▼
rave-mate-enc.exe: UpdateSubresource → VideoProcessorBlt (NV12) → MFT → annex-B AU
  │  AU ring (SHM)
  ▼
parent pump: make([]byte, ln) per AU  ← ~6 MB/s garbage → medialink wire
```

Per 4K60 route the host moves **4 GB/s of pixels** it does not need to see, and the Go heap
churns 33 MB × in-flight buffers. The pixels are already on the GPU, in a texture the
encoder's own device can open.

## 2. Increment-1 architecture: the frame plane disappears

The child opens the sender's shared texture on ITS device and blits it straight into the VP
input. Consequences, in order of importance:

1. **No GPU→CPU readback.** 2.0 GB/s gone.
2. **No SHM frame slot.** The other 2.0 GB/s gone, and 33.2 MB of shared VA per session
   gone (SHM shrinks 66.4 MB → 4.0 MB, -94%).
3. **No Go pixel buffers for that route.** `pixPool`, `pixRef`, `sharedFrame`, the
   newest-wins hop chain and the per-route fps gate all become unreachable — not ported,
   deleted from the path (they stay as the fallback path's code, untouched).
4. **No OpenGL anywhere.** The current receiver needs `CreateOpenGL` + `LockOSThread` only
   because `ReceiveImage` is a GL readback. D3D11 zero-copy needs no GL context, so the
   whole GL-thread-ownership problem is not ported, it is dissolved.
5. Ring size becomes **geometry-independent** (bitrate-derived), so a sender resize costs
   zero SHM realloc.

```
Spout sender shared texture (GPU) ───┐   handle+format resolved ONCE by Go (registry)
                                     ▼
rave-mate-enc.exe, session thread (COM MTA, owns the D3D11 device):
   pace(fps) → mutex acquire → CopyResource → VideoProcessorBlt (CSC+scale, NV12 pool)
             → MFT submit → annex-B AU → AU ring (SHM) → SetEvent(-a)
                                     │
Go (media featurehost child): pump AUs (~100 KB/frame) → wire crypto → socket
```

Go keeps: sender discovery, negotiation, wire framing/crypto, QoS/router, supervision,
telemetry, UI. Go never sees a pixel on a zero-copy route.

### 2.1 Refinements vs. the brief (with reasons)

- **Do NOT call SpoutLibrary.dll from Zig.** `SPOUTLIBRARY` is a C++ abstract class — a
  ~150-slot virtual table with `std::string`/`std::vector` returns. Hand-rolling that
  vtable in Zig (the B6 WebView2 technique) buys nothing here and adds a silent-ABI-skew
  failure mode on every SDK bump. Everything the child needs is TWO scalars —
  `dxShareHandle` + `dwFormat` — which `GetSenderInfo` already retrieves and
  `spout_shim.cpp` currently discards (`spout_shim.cpp:132-137`, `:158-160`). Go resolves
  them from the registry (no GL, already cached in `videoshare/scan.go`) and passes them in
  `open`. The child then uses only DXGI/D3D11 — interfaces `mf.zig` ALREADY declares.
- **Do NOT parse Spout's sender-name shared memory in Zig either.** It would remove the
  handle-passing round trip at the cost of replicating an undocumented on-disk layout.
  Rejected for increment 1; revisit only if handle-passing proves racy.
- **Keep the exe name `rave-mate-enc.exe` and the `native/zigenc/` directory.** The name is
  load-bearing in five places (embed staging + content hash, NSIS sidecar, `RAVE_MATE_ENC_EXE`,
  repo zig-out dev lookup, CI). Renaming to `rave-mate-media.exe` is cosmetic and is its own
  increment. "media child" here names the ROLE, not a new binary.
- **Keep one child per ADAPTER, N sessions** (unchanged). The D3D11 device, VP enumerator and
  encoder MFT are per-adapter and expensive; the supervisor already re-places every session of
  a dead child and forces IDR, proven by execution. Capture faults are HRESULT-level
  (`OpenSharedResource`/`CopyResource` return errors), so capture does not add AV-class blast
  radius — the AV risk stays where it already is, in the vendor MFT. Per-route children would
  multiply device creation (~200 ms) and VRAM for no containment gain. **DECIDED.**

## 3. Protocol deltas

Child `hello` gains `ver:2`. A parent seeing `ver:1` never requests zero-copy (version gate,
same shape as the `rz_abi_version` rule).

### 3.1 `open` — new fields (all optional; absent = today's behaviour)

| field | type | meaning |
|---|---|---|
| `src` | string | `"shm"` (default, = v1 frame slot) \| `"spout"` (zero-copy) |
| `sh` | u64 | sender share handle from `GetSenderInfo`, packed as u64 (0 = invalid) |
| `sfmt` | u32 | DXGI format of the shared texture (0 = probe B8G8R8A8 then R8G8B8A8) |
| `sname` | string | sender name, ASCII ≤256 — access-mutex name + logs |
| `cap_n`,`cap_d` | i32 | capture pacing rational; absent = `fps_n`/`fps_d` |
| `ring_kb` | u32 | AU ring size in KiB; 0 = legacy rule (`max(8 MiB, frame_bytes)`) |

`in_w`/`in_h` still carry the sender geometry (encoder input size). For `src:"spout"` the
parent allocates **no frame slot**: SHM total = `256 + ring_kb*1024`. The child must size the
mapping from `src` + `ring_kb`, never from `in_w*in_h*4` (this is the one place a v1 child
would silently map past the end — hence the `ver` gate).

Ring sizing (parent): `ring = clamp(kbps/8 * 1024 / 2, 4 MiB, 16 MiB)` — half a second of
bitstream, floor 4 MiB, ceiling 16 MiB. 4K60 @ 50 Mbps → 4 MiB. Geometry-independent.

### 3.2 SHM header v2 (256 B, additive; v1 used 0-63 only)

Existing: `0 magic 'RMF2' | 4 ver | 8 frameSeq | 16 framePTS | 24 consSeq | 32 auWrite |
40 auRead | 48 auDropped | 56 encBusyNs`. Parent writes `ver=2`; child requires `ver>=2`
for `src:"spout"`.

New, child-written, monotonic, read by the parent for telemetry (no JSON on the frame path):

| off | field | meaning |
|---|---|---|
| 64 | `capFrames` u64 | shared textures captured |
| 72 | `capSkips` u64 | pacing ticks skipped (previous encode still running) |
| 80 | `mtxTimeouts` u64 | access/keyed mutex acquire timeouts |
| 88 | `srcErrors` u64 | `CopyResource`/acquire hard failures |
| 96 | `lastCapNs` i64 | QPC ns of the last successful capture (staleness oracle) |
| 104 | `capFmt` u32 | DXGI format actually consumed |
| 108 | `capFlags` u32 | bit0 zero-copy live, bit1 keyed mutex, bit2 named access mutex, bit3 unsynchronized |

`-f`/`-c` events stay created by the parent and opened by the child for BOTH src modes (2
handles, keeps open/restart/teardown code paths uniform); on a spout session `-f` is reused
as the control-ping wake and `-c` never fires.

### 3.3 New stdout events

- `opened` gains `"src":"spout"|"shm"`, `"cap":"zerocopy"|"downgraded"`, `"err_src":"…"` —
  the downgrade verdict rides the SAME event, so the parent learns which path it got without
  a second round trip.
- `{"ev":"srcgone","sid":N,"reason":"open_shared|fmt_unsupported|copy_failed|acquire_dead"}` —
  capture source unusable or gone (sender closed/resized/moved adapter). The child STOPS
  capturing, keeps the session alive (encoder intact) and waits; the parent decides.

No other control ops change. `close`/`bitrate`/`idr`/`quit` are untouched.

## 4. Child implementation shape

New `native/zigenc/src/cap.zig`, ~350 LOC, no allocator use after open:

```
Cap.open(dev, vdev, vpe, sh, sfmt, sname):
  OpenSharedResource(sh) → ID3D11Texture2D          (legacy SHARED handle, not NT)
  QI IDXGIKeyedMutex → use it if present            (bit1)
  else OpenMutexW("<sname>_SpoutAccessMutex")       (bit2)  ← name PROVISIONAL, verify vs SDK
  else unsynchronized + one loud counter             (bit3)
  desc = GetDesc(tex); require desc.W/H == in_w/in_h
  CheckVideoProcessorFormat(desc.Format) → else error.FmtUnsupported
  CreateVideoProcessorInputView(tex)                ← the SHARED texture IS the VP input:
                                                      no own input texture (-33 MB VRAM)
Cap.grab(timeout_ms):
  acquire (timeout 1..4 ms) → CopyResource(stage?, tex) → release
  (CopyResource target = the VP input view's resource; when the sender's format needs a
   staging hop the copy is GPU→GPU, still zero host bytes)
```

`sessionMain` gains a branch. `src:"spout"` loop, allocation-free:

```
next = qpcNs()
while !closing:
  WaitForSingleObject(ev_frame, sleep_ms)   // control ping only (close/idr/bitrate promptness)
  drain mailbox (idr / kbps / closing)
  now = qpcNs()
  if now >= next:
     if cap.grab() ok: enc.feedTexture(pts=now); capFrames++
     next += period; if now - next > 2*period: next = now; capSkips++   // resync, never catch up
  else: enc.pump(sink)
```

`enc.feedTexture` is `Enc.feed` minus the `UpdateSubresource` step — same VP Blt, same NV12
pool, same async MFT drive, same `AuSink`. **The encode half is unmodified**, which is what
makes the pixel-parity gate meaningful.

Threading: capture + VP + MFT all on the existing per-session thread (COM MTA; the device
already has `ID3D10Multithread` enabled). **No thread is added and no GL context exists.**

## 5. Static allocation budget — per session, 3840×2160@60, 50 Mbps

| | today (shm src) | increment 1 (spout src) |
|---|---|---|
| SHM | 256 B + 33.2 MB frame + 33.2 MB ring = 66.4 MB | 256 B + 4.0 MB ring = **4.0 MB** |
| Go host pixel buffers | 33.2 MB × in-flight (pool) | **0** |
| VRAM: VP input tex | 33.2 MB (own R8G8B8A8) | **0** (view over the sender's texture) |
| VRAM: NV12 pool | 8 × 12.4 MB = 99.5 MB | 99.5 MB (unchanged) |
| child heap after open | 0 B/frame | 0 B/frame |
| host bytes/frame | 66.4 MB moved | **0** (AUs only, ~100 KB) |

Allocation events after `opened`: **none**. The only allocation event in a route's life is
open/reopen, which is route-serialized by the parent.

## 6. Backpressure, drop policy, counters

- **Capture: newest-wins by construction.** There is no queue — the pacing tick samples
  whatever the sender currently has. A late encode delays the next tick; the tick is then
  RESYNCED, never caught up (no burst). Counter `capSkips`.
- **Mutex contention:** acquire timeout 1..4 ms → skip this tick, `mtxTimeouts++`. Never
  spin: the whole reason the Go poller has a 4 ms/50 ms backoff is that hammering the
  sender's access mutex serializes against the sending app's and DWM's GPU submissions.
  Pacing at the negotiated fps is the honest bound; nothing polls faster.
- **AU ring full → drop + `auDropped`** (existing, unchanged).
- **Parent pump → `out` chan cap 8, 2 s bounded block then drop** (existing, unchanged).
- **Bound statement (required by CLAUDE.md):** per session, in-flight pixels = 1 shared-texture
  read + NVPOOL(8) NV12 textures, **0 host bytes**; AU ring 4-16 MiB, policy drop-newest with
  counter.
- **Surfacing:** `ProcStats`/`medialink.PipelineStats` gain `CapFPS`, `CapSkips`,
  `MtxTimeouts`, `SrcErrors`, `ZeroCopy bool`, `CapStaleMs` (from `lastCapNs`) → route stats +
  `ctl perf`. One INFO at open naming the path taken; one WARN per downgrade naming the reason.

Freshness note (**DECIDED-PROVISIONAL**): increment 1 paces BLIND — it does not consult
Spout's `IsFrameNew`, because that requires a bound receiver (and thus the GL/readback object
we just removed). A static sender therefore encodes duplicate frames as near-free
skipped-macroblock P-frames instead of going quiet. This is arguably better for a live route
(the peer's jitter buffer never starves), costs a few hundred bytes/frame, and is visible via
`capFrames` vs. AU sizes. Frame-new gating (metadata-only receiver, or the sender's
frame-count shared memory) is an increment-2 optimisation, not a blocker.

## 7. Wiring, flag gate, fallback semantics

### 7.1 Getting the handle to the encoder

New optional interface in `medialink` (pure declaration, no hardware):

```go
// ZeroCopySource is a raw-video Source whose pixels live in a GPU shared texture an
// encoder may consume directly (no host readback).
type ZeroCopySource interface {
    SharedTexture() (handle uint64, dxgiFormat uint32, w, h int, name string, ok bool)
}
```

`mediaroute.spoutSource` implements it over a new `videoshare.SenderShare(name)` (shim
addition). `mediapipe.mfBridge` type-asserts it.

**Critical wiring detail:** `openSpoutSource` currently attaches to the `captureHub`
EAGERLY, i.e. the readback + GL context + pool buffers start before the encoder factory runs.
On a zero-copy route that readback would still happen and be thrown away, defeating the whole
increment. Fix, in `spoutSource`: **attach on first `Next()`** (lazy). `mfBridge` in zero-copy
mode never calls `Next` and never starts its `feed` goroutine, so no capture is ever opened;
`Close()` still closes the source (closing an unattached feed is a no-op). The per-route fps
cap moves into the child's pacing period — do not keep two gates.

### 7.2 Gate

- `config.MediaLinkFeature.ZigCapture *bool` (`json:"zigCapture,omitempty"`), additive, **default
  OFF** for increment 1 (nil = off). Env `RAVE_MATE_ZIGMEDIA_CAPTURE=1|0` overrides (soak/tests).
- Requested per session open when ALL hold: gate on • source implements `ZeroCopySource` with
  `ok` • child `hello.ver >= 2` • handle non-zero. Otherwise `src:"shm"`, byte-identical to today.

### 7.3 Fallback ladder — never a dead route

```
zero-copy (src:spout)
  ├─ open-side refusal (opened.cap=="downgraded" / err_src) ──► same child, same session
  │                                                             reopened with src:"shm"
  ├─ mid-route srcgone ──► parent closes + reopens the session (fresh handle from a rescan);
  │                        if the rescan still fails N=3 times, pin the route to src:"shm"
  └─ mfenc open failure / poisoned tuple (existing) ─────────► ffmpeg child (existing)
```

Loud on every rung: exactly ONE WARN per route naming the reason, plus a monotone
`downgrades` counter so a rig that always downgrades is visible in `ctl perf` rather than
silently slow. The Go readback path (`videoshare` receiver + `pool.go` + `captureHub`) is
**untouched by increment 1** — it is both the fallback and the parity reference.

## 8. Increment-1 gates + verify recipe

### 8.1 Automated

1. **Pixel parity (the honest gate; byte-exact AU comparison is impossible — different upload
   route, same pixels).** Child test mode dumps the NV12 of frame N; drive the SAME static
   Spout sender through both `src` modes and assert max abs per-channel diff ≤1 after CSC
   (BT.709 studio both sides). Prove non-vacuous by injecting a swizzle/vertical flip and
   watching it FAIL. Orientation is the trap: `SendImage` is called with `bInvert=false` and
   the shim does the geometric flip itself (`spout_shim.cpp:72-93`) — the shared texture's row
   order must be re-established for the zero-copy path, not assumed.
2. **Protocol:** parent/child header-offset table asserted from one shared constant list; a v1
   child + a `src:"spout"` request must refuse cleanly (never map past the mapping).
3. **Crash continuity:** `RAVE_MATE_MFENC_TEST_FAULT_FIRST=600` → AV mid-route → session
   re-placed WITH the spout source reopened (new assertion: the reopen re-issues the share
   handle) → forced IDR → route survives.
4. **Geometry change:** sender resized live → `srcgone` → reopen → route continues; assert ≤1
   shm recreate and the ring size UNCHANGED (geometry independence).
5. **Sender restart:** kill the sending app → route idles, no error storm, no leak → restart →
   route resumes within one registry-scan interval (≤2 s).
6. **Flag off = today:** full existing suites green, and the readback path's numbers unchanged.
7. `GOWORK=off go build ./... && go vet ./... && go test ./...`; untagged (non-cgo, non-Windows)
   build + stubs green; `zig build test` in `native/zigenc`.

### 8.2 Manual 2-instance 4K60 soak (this box)

```
make zig
GOWORK=off go build -tags "spout vr zigdsp zigui zigvr encembed" -o dist/rave-mate.exe ./cmd/rave-mate
# A (sender side)                          # B (receiver side)
RAVE_MATE_CONFIG_DIR=%TEMP%\rmA            RAVE_MATE_CONFIG_DIR=%TEMP%\rmB
RAVE_MATE_CTL_ADDR=127.0.0.1:47695         RAVE_MATE_CTL_ADDR=127.0.0.1:47696
```

**Never touch ctl 47620** (the user's live instance). Source: OBS with a Spout output at
3840×2160@60 (or rave-mate's own deckcard sender for a synthetic 4K source). Pair A↔B over
peerlink (SAS), `mediaLink.shareVideo` on A, open the route from B.

Assert, with `zigCapture=true` on A:
- `ctl logs` shows the zero-copy INFO; `ctl perf` shows `zeroCopy=1`, `capSkips` low,
  submit→AU p99 < 25 ms (the measured 1080p baseline is p50 5.4 / p99 22 ms).
- **RSS flat** — sample `rave-mate.exe`, the media child and `rave-mate-enc*.exe` at 1 Hz for
  30 min into a CSV; require no monotone trend and ±5% band. This is THE gate: it is the
  regression the increment exists to kill.
- Receiver picture correct (`ctl screenshot` on B at start / +15 min / +30 min).
- Then flip `zigCapture=false`, re-open the route, and record the same series as the control —
  the old path's churn must be visible, or the soak is measuring nothing.
- `ctl quit` both; assert clean exit and NO orphan `rave-mate-enc*.exe` (`tasklist`).

### 8.3 Go-runtime workarounds that must NOT be ported (increment-1 scope)

Per the migration rule — flag, don't blind-copy:

- `videoshare/pool.go` `pixPool` (GC-dodging 33 MB buffer pool): a zero-copy session has **no
  host pixel buffers**. Do not recreate a pool in Zig.
- `mediaroute/capture.go` `pixRef` refcount + `sharedFrame` + the captureHub fanout: exists
  because the READBACK is expensive and must be shared by N routes. With zero-copy each session
  does its own GPU-local `CopyResource` (~0.3 ms at 4K). Do not port the fanout. *Open
  measurement:* if N sessions × CopyResource on one adapter shows up, increment 3 adds ONE
  shared copy per (adapter, sender) inside the child — do not pre-build it.
- The newest-wins cap-1 channels (receiver→hub→route) — scheduler seams. Replaced by
  "sample at pacing time", no queue exists.
- `spoutSource.minGap` per-route fps drop — becomes the child's pacing period. One gate, not two.
- `recvpoll.go`'s 4 ms/50 ms interval backoff + `recvNeedSize` state machine — exists because
  `ReceiveImage` is expensive and mutex contention is systemic. No readback ⇒ no purpose. Carry
  over only the PRINCIPLE (never poll faster than the target rate).
- `ProcSession.submitAt map[int64]time.Time` (bounded 1024) — a Go map write per frame to
  measure latency the child already knows (`encBusyNs`). Leave for increment 1, flagged: the
  fix is to compute submit→AU wholly in the header.
- `procparent_windows.go:934` `data := make([]byte, ln)` per AU (~6 MB/s of garbage): the honest
  fix is reading AUs in place and advancing `auRead` only after `medialink.Frame.Release` runs —
  which couples a wire stall to ring occupancy, i.e. real backpressure the ring already handles
  by dropping. Tracked as **sub-item 1e, separately gated**, because it changes ring semantics.

## 9. Scope + scheduling (increment 1)

Protocol §3 is FROZEN by this document, so 1a/1b and 1c can run in PARALLEL.

| # | Work | Files | LOC | Agent-days |
|---|---|---|---|---|
| 1a | Parent + wiring: open fields, header v2, ring sizing, `srcgone` handling, `ZeroCopySource`, lazy `spoutSource` attach, mfBridge zero-copy branch, stats, flag+env gate | `mfenc/procparent_windows.go`, `mfenc/procstats.go`, `mediapipe/mf_bridge.go`, `medialink/pipeline.go`, `mediaroute/mediaroute.go`, `config/config.go` | ~450 Go | 0.5 |
| 1b | Shim: expose share handle + format (+ scan variant) | `videoshare/spout_shim.{h,cpp}`, `receiver_spout.go`, `videoshare.go` | ~80 | 0.2 (fold into 1a) |
| 1c | Child capture: `cap.zig`, `feedTexture`, pacing loop, header counters, `srcgone` | `native/zigenc/src/cap.zig`, `mf.zig`, `main.zig` | ~350 Zig | 0.5 |
| 1d | Gates: pixel-parity harness, crash/geometry/sender-restart tests, soak script | `mfenc/*_test.go`, `mediapipe/*_test.go`, `scripts/` | ~300 | 0.5 |
| 1e | (optional, own flag) AU in-place read + `Frame.Release` ring reclaim | `procparent_windows.go`, `mf_bridge.go` | ~120 | 0.3 |

Convergence: 1a+1b and 1c land behind the OFF-by-default flag, 1d proves them, then the
2-instance soak (§8.2). Flag flips to default-on only after a 7-day soak, in a follow-up commit
that records the numbers here — same discipline as the B-waves.

## 10. Increment 2 — receive side

Today's receive path is the sender path's mirror image, and worse:

```
wire AU → jitterbuf (Go) → ffmpeg decode child: AU→stdin, raw RGBA→stdout (33 MB/frame pipe)
        → Go buffer → videoshare.FrameSender → cgo SendImage (GL upload)
          └─ and when flip != 0, spout_shim.cpp:81 does malloc(w*h*4) + a scalar
             per-pixel transpose + free — a 33 MB malloc/free PER FRAME at 4K
```

Target: the child decodes and publishes; no raw frame ever crosses a pipe or the Go heap.

```
parent writes AU → inbound AU ring (SHM) → child: MF H.264/HEVC decoder MFT (D3D11,
  hardware) → NV12 surface → VideoProcessorBlt → the Spout sender's shared texture
```

**Protocol delta (symmetric with §3):** session gains `dir:"enc"|"dec"` (absent = `"enc"`).
A `dec` session's SHM = header + inbound AU ring (`in_ring_kb`), and the header's ring
counter block is instantiated twice (outbound at 32-55 as today, inbound at a new 128-159
block: `inWrite`/`inRead`/`inDropped`/`decBusyNs`). Parent → `SetEvent(-f)` on AU append;
child → `-c` on consume. Reuses the exact ring code from §3, opposite direction.

**Who owns the Spout sender (DECIDED-PROVISIONAL).** The child must write into a texture the
Spout registry already advertises. Cleanest available seam: **Go calls `CreateSender(name,w,h,fmt)`
+ `GetHandle()`** through the existing shim (no pixels move) and passes that handle in `open`;
the child opens it and uses it as the VP **output** view. Known cost: Spout increments a
sender's frame counter only inside `SendTexture`/`SendImage`, so a receiver's `IsFrameNew()`
dedup goes stale — content updates are still seen (receivers copy the texture each tick), only
the "is this new" hint is lost. This is the same trade §6 already accepted on the capture side,
so the system stays consistent. **Must be live-verified against OBS's Spout input and Resolume
before the flag flips**; fallback = today's ffmpeg-decode + `SendImage` path, unchanged.

Stays in Go, correctly: `jitterbuf`, `nack`, `syncclock`, `mediaclock`, LTC/timecode — these
reason about compressed AUs and time, not pixels, and they are where protocol behaviour lives.

Scope: ~400 LOC Zig (`dec.zig` + decoder MFT drive), ~250 LOC Go (inbound ring, `DecodeSpec`
deltas, sender-handle plumbing), gates mirror §8. **1.5 agent-days**, and it unblocks deleting
the ffmpeg decode child for H.264/HEVC.

## 11. Increments 3-5

**Increment 3 — measurement-driven (do NOT pre-build).** Only if the inc-1 soak shows it:
one shared `CopyResource` per (adapter, sender) inside the child when N sessions share a
sender; frame-new gating via the sender's frame-count shared memory or a metadata-only
receiver; adapter-affinity resolution so a cross-adapter sender re-places the session on the
right adapter instead of downgrading. ~0.5-1 agent-day each, independent.

**Increment 4 — the produce paths.** Note first that webcam and VRSL/deckcard **publish INTO
Spout**, so increment 1 already covers their consumption by a route — this is only about how
they generate pixels. `webcam` (ffmpeg dshow → `framepipe` → `SendImage`), `vrslgrid` +
`deckcard` (Go raster → `rz_fill_cells` → `SendImage`). Target: render/receive straight into a
D3D11 texture and publish it, which deletes both the GL upload and `spout_shim.cpp:81`'s
per-frame malloc. Also folds in the `rave-mate-media.exe` rename (cosmetic; touches
`build-zig.sh`, embed staging, NSIS, `RAVE_MATE_ENC_EXE`, CI, docs). ~2 agent-days.

**Increment 5 — retire the Go readback path.** Only after inc 1+2 soak 7 days at default-on:
delete `pool.go`, `pixRef`/`captureHub` fan-out, the four duplicated newest-wins loops, and
lift `nack.go`'s raw-video retransmit carve-out (§12 explains why that one is a *feature*
regression caused by the allocator). Each deletion needs the flag already default-on and its
gate green. ~1 agent-day.

## 12. Go-runtime workarounds — full inventory

§8.3 is the increment-1 subset. The complete plane (scouted with file:line refs) splits three
ways. **Rule (ZIG_MIGRATION.md): never carry a workaround into Zig when its only reason is the
Go runtime; port faithfully + flag when unsure.**

### 12.1 Must NOT be ported — pure GC-dodges

- `videoshare/pool.go:5-30` — `pixPool` + `getPix`/`PutPix`, including the `*[]byte` pointer
  form chosen *only* to dodge the interface-boxing alloc on `Put` (`:12`). The package doc
  states the motive outright: "~500 MB/s of short-lived 8 MB frame copies (GC churn was a
  contributor to the … melt)". **Also the only long-lived buffer in the whole path with no
  frames/bytes cap** (`:9-10` admits it) — i.e. the OOM had no ceiling to hit.
- `mediaroute/capture.go:30-55, 185-244` — `pixRef` refcounting + the release-past-zero warn.
  Exists *only* because buffers are pooled. Zero-copy has nothing to refcount.
- `medialink/nack.go:58-84` — `retainOrRelease` **excludes raw pooled video frames from the
  NACK retransmit window** because "Retaining one starves the capture pool, so every readback
  re-allocates 8 MB (1080p) / 33 MB (4K)". This is the sharpest finding in the inventory: an
  **output-visible protocol feature was switched off to relieve the allocator**. On a zero-copy
  route there is no pool to starve, so the carve-out must be revisited (increment 5), not ported.
- `videoshare/videoshare.go:180-193, 274-301` — per-publish `signature()` string building.

### 12.2 Must NOT be ported — scheduler seams the design removes

- The four duplicated newest-wins cap-1 loops (`sender_spout.go:273-286`,
  `receiver_spout.go:150-163`, `capture.go:199-218`, `webcam/capture.go:184-198`).
- `recvpoll.go:17-19` 4 ms/250 Hz ↔ 50 ms backoff + the `recvNeedSize` state machine.
- `mediaroute/mediaroute.go:386-407` `spoutSource.minGap` per-route fps drop.
- `videoshare/fpsgate.go:17-21` lock-free atomics for the gate (no cross-thread gate remains).
- `videoshare/scan.go:8-17, 83` — the 1 s TTL + process-global scan cache exists to amortize
  "1+2N COM objects per scan". **Keep the cache** (Go still owns discovery) but do not build a
  second one in Zig, and keep `:63-72`'s forced-refresh-on-unknown-name: that carve-out is
  output-visible (a route opens off that call).

### 12.3 Load-bearing — preserve verbatim, with the rationale

- `LockOSThread` GL workers (`sender_spout.go:130`, `:240`, `receiver_spout.go:86`) — required
  for the FALLBACK and produce paths; only the zero-copy path escapes them.
- `videoshare/helpers.go:15-43` `waitAll`/`closeJoin 1 s` bounded joins + `defer close(stopped)`
  ordered after release. **Go cannot kill a goroutine wedged in cgo** — that is a runtime
  limitation, not a crutch, and the same condition is caught at process level by
  `mediaproxy.go:60-63`'s 5 s heartbeat. The Zig child faces the identical problem with driver
  calls: keep bounded waits + a supervised kill, never an unbounded join.
- `mediapipe/encode.go:199-203` — `JobRealtime`, **no CPU cap**, with the ruling that "parking
  it in the 10% batch bucket does not save the machine work … Bounding cost belongs upstream
  (fps/pixel-rate gates), not here." Carry this verbatim; it is the policy that stops a future
  agent from "fixing" load by throttling the encoder.
- All aggregate host guards: `router.go:78-86` + `:417-480` (`memWatchdog`, `admit`,
  `shedAllRoutes`, `maxRoutes 8`, ceiling 2048 / recover 1536 MB), `codec.go:65-73`
  (`swTier4MaxPixelRate`, `swAutoMaxHeight`), `codec.go:199-206` `rawVideoMaxPixels`,
  `nack.go:12-14` dual frames+bytes caps, `app.go:129-142` 3 GiB soft guard + `:2210`
  `mediaChildMemMB 2048`, `feat_media.go:304-321` 80 %-soft-under-hard ("The job cap KILLS the
  process; GOMEMLIMIT only makes the GC work harder — so the soft limit must bite first"),
  `sysexec/job_windows.go:40-150` job classes + `ensureMemJob`.
- `mediaroute/mediaroute.go:141-148, 258-270` loop guard, self-share guard, same-PC refusal,
  receive de-dup ("Activating the same source twice … a big multiplier in the RAM blowup").

### 12.4 Independent defects surfaced by the scout (not zigmedia scope; file them)

1. `webcam/framepipe.go:21-23` allocates a **fresh full frame per capture** ("never reused") —
   the exact opposite policy from the rest of the plane, 250 MB/s of garbage at 1080p30.
2. `spout_shim.cpp:81-92` — `malloc(w*h*4)` + scalar per-pixel transpose + `free` **per frame**
   whenever `flip != 0`. At 4K that is a 33 MB alloc/free per frame on the send path.
3. Collected-but-invisible telemetry: `PipelineStats.LatP50Ms/LatP99Ms/QueueDepth/ChildCPUPct`
   are produced by `mf_bridge.go:192-197` and **never rendered** (`view_peers_media.go:163-172`,
   `render_peers.go:987-992` show only Encoder/HWAccel/OutFPS/Restarts). Since §6 adds capture
   counters to the same struct, increment 1 should surface the whole block or it ships blind.
4. Dropped-frame counters that reach nobody: `encode.go:57,244`, `mf_bridge.go:40,96`,
   `mediaroute.go:426,445`.

## 13. Risk register

| # | risk | why it bites | mitigation / gate |
|---|---|---|---|
| R1 | **Stale share handle = silently frozen picture** | A sender restart can leave `OpenSharedResource` succeeding on a dead texture whose content never changes. Worst failure mode in the design: the route looks healthy and ships a still frame. | `lastCapNs` staleness oracle + Go compares the handle on every 2 s registry scan; a changed handle forces `srcgone`→reopen. **Gate: restart the sending app mid-route and assert the picture recovers** (§8.1 #5), and assert the frozen case is detected, not just the clean case. |
| R2 | Access-mutex name / semantics | `"<sender>_SpoutAccessMutex"` is inferred, not read from the SDK. Wrong name ⇒ unsynchronized copies ⇒ tearing. | Verify against Spout SDK source at implementation time; QI keyed mutex first; unsynchronized is a counted, logged last resort (`capFlags` bit3). |
| R3 | Keyed-mutex deadlock | If a sender *does* expose `IDXGIKeyedMutex` and holds a non-zero key, `AcquireSync(0, INFINITE)` hangs the session thread. | Always `AcquireSync(0, 1..4 ms)`, always `ReleaseSync(0)`, timeout ⇒ skip tick + `mtxTimeouts++`. Never an unbounded wait (same rule as R6). |
| R4 | TYPELESS / exotic formats | Shared textures are sometimes `_TYPELESS`, or `R10G10B10A2`/`RGBA16F`; `CreateVideoProcessorInputView` refuses TYPELESS and the VP may refuse the rest. | Explicit format allowlist + `CheckVideoProcessorFormat` probe → `fmt_unsupported` downgrade. Do not guess a typed view. |
| R5 | Orientation | Spout textures are bottom-up in GL terms and the shim currently flips on the CPU (`spout_shim.cpp:72-93`) with `bInvert=false`. Zero-copy must re-establish row order once, on the GPU. | The §8.1 #1 pixel-parity gate, proven non-vacuous by injecting a flip. |
| R6 | GL context ownership | Dissolved on the zero-copy path, but the fallback + produce paths still own GL contexts; two owners for one sender would be a real hazard. | Lazy attach (§7.1) means the fallback never creates a context unless it is actually the active path. Assert: zero-copy route ⇒ no GL context created (count `rave_spout_create` calls). |
| R7 | Cross-adapter sender | `OpenSharedResource` fails across adapters; the child is pinned per LUID and device choice is user policy (WP-3). | Downgrade to `src:"shm"` with a warn naming the likely cause. **Never silently move adapters.** Affinity resolution is increment 3. |
| R8 | DX9 / CPU-share / memoryshare senders | These have no DX11 shared texture at all. | `sh == 0` or unknown format ⇒ downgrade. These senders keep the readback path permanently, correctly. |
| R9 | Job-object accounting | The enc child sits in `JobRealtime` (no CPU cap) and `ensureMemJob` caps **committed memory**; a mapped SHM view counts against it. | This is an argument *for* the design: 66.4 → 4.0 MB per session moves the child far from its cap. Assert committed memory in the soak. |
| R10 | Embed / self-update version skew | A stale sidecar `rave-mate-enc.exe` could be protocol v1 while the parent requests `src:"spout"`. | The `hello.ver >= 2` gate (§3) plus the existing content-hash-stamped embed extraction. Assert a v1 child refuses cleanly and the route still runs. |
| R11 | HVCI / driver hardening | Vendor MFT AVs are the reason the child exists; capture adds `OpenSharedResource` on a foreign process's texture, which HVCI-hardened drivers may refuse. | Already contained: capture failures are HRESULT-level → downgrade; AV-class faults still kill only the child and the supervisor re-places sessions (proven by execution). |
| R12 | Blind pacing bandwidth | Duplicate frames on a static sender cost bitrate (§6). | Counted (`capFrames` vs AU sizes); increment 3 adds frame-new gating if the soak shows it matters. |

### 13.1 2-PC verify recipe (beyond §8.2's single-box soak)

The single-box soak proves memory + throughput; it does NOT prove the wire, and the OOM was
first seen *sending to a peer*. Two-PC pass, after §8.2 is green:

1. PC-A (sender, this box): OBS Spout out at 3840×2160@60, `zigCapture=true`, `ctl` on 47695.
   PC-B (receiver, peer PC): `ctl` on its own port. **Neither instance uses 47620.**
2. Pair over LAN peerlink (mDNS + Ed25519 + SAS), enable `shareVideo` on A, open the route from B.
3. 30 min soak. On A: RSS of `rave-mate.exe` + media child + `rave-mate-enc*.exe` flat; GPU
   encode utilisation steady; `capSkips`/`mtxTimeouts`/`auDropped` low and non-accelerating.
4. On A, while the route runs: **verify the sending app stays usable** — OBS preview must not
   stutter and the mouse must not lag. Systemic pointer lag on the sender PC is the documented
   symptom of hammering the sender's shared-texture mutex (`recvpoll.go:7-11`), so this is the
   regression the pacing rule exists to prevent, and it is only observable on a real rig.
5. On B: picture correct, `late %` and jitter-buffer depth comparable to the flag-off control.
6. Fault matrix on the live 2-PC route: kill the sending app (R1) · resize its canvas (§8.1 #4)
   · `taskkill` the enc child (§8.1 #3) · pull the network briefly (jitterbuf/NACK unaffected by
   this change — assert no new behaviour) · flip `zigCapture=false` live and re-open.

## 14. Premise correction — the "in-flight stopgap" branch

The brief assumed an unmerged Go stopgap on `fix/spout-4k-oom` that would land before
implementation. **It is not unmerged and it is not in flight:** `fix/spout-4k-oom` resolves to
`82a3543`, byte-identical to `development` and to this branch's parent;
`git log development..fix/spout-4k-oom` is empty. It is a stale pointer, not a work branch.

The stopgap work is **already merged** and this design sits on top of it — so there is nothing
to avoid contradicting, and these are the commits whose behaviour the fallback path must keep:

| commit | what |
|---|---|
| `c567b50` | bounded worker joins on Close + `PutPix` on receiver resize |
| `2d9aceb` | gate the spout receive poll — a stale sender must reach idle backoff |
| `1d471cd` | 4K60 open crash: deliberate adapter pick + driver-fault guard |
| `b698dd8` | the Zig per-adapter encoder child (the seed this design grows) |
| `d260ada` | realtime encode/decode children out of the 10 % CPU cap |
| `9d560e1` | embed the encoder child in the exe + child-gated advertisement + crash forensics |

Implementers should re-check this before starting: if a new stopgap appears on that branch
later, §8.3 and §12 are the sections it could collide with.

## 15. Full scheduling view

| inc | work | agent-days | depends on |
|---|---|---|---|
| 1a+1b | parent + wiring + shim | 0.7 | protocol frozen (this doc) |
| 1c | child capture (`cap.zig`) | 0.5 | protocol frozen — **parallel with 1a** |
| 1d | gates + soak harness | 0.5 | 1a, 1c |
| 1e | AU in-place read (own flag) | 0.3 | 1a |
| — | §8.2 single-box soak, then §13.1 2-PC | 0.5 + 7 d wall | 1a-1d |
| 2 | receive side (`dir:"dec"`) | 1.5 | 1 default-on |
| 3 | measurement-driven items | 0.5-1 each | inc-1 soak numbers |
| 4 | produce paths + rename | 2 | 1, 2 |
| 5 | retire the Go readback path | 1 | 1+2 soaked 7 d |

Critical path to the OOM being fixed on the field rig: **1a+1b ∥ 1c → 1d → soak ≈ 2 agent-days
plus the soak wall-clock.** Increments 2-5 are value, not the fix.
