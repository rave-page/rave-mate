# zigmedia increment 2 (receive side) - status

Branch `feat/zigmedia-inc2` off `development` @68d8692. Spec: `ZIGMEDIA_DESIGN.md` §10.
Flag **OFF by default** (`config.MediaLink.zigDecode`, env `RAVE_MATE_ZIGMEDIA_DECODE`).

Target reached: on a `dir:"dec"` session the wire AUs ride a shared-memory ring into the Zig
child, an MF decoder MFT decodes on the GPU, and the video processor blits each frame straight
into the destination video-share sender's shared texture. No raw frame crosses a pipe, none
lands on the Go heap, and none is uploaded a second time.

## Shipped

| # | work | where |
|---|---|---|
| — | destination-texture seam: `rave_spout_open_sender`, `videoshare.SharedSender` | `videoshare/spout_shim.{h,cpp}`, `sharedsender.go`, `sender_spout.go`, `sender_noop.go` |
| — | `medialink.ZeroCopySink` + decode stats block | `medialink/pipeline.go` |
| — | eager sink open + `SharedTexture()` | `mediaroute/mediaroute.go` |
| — | child: `dir:"dec"`, inbound ring, `dstgone`, hello ver 3 | `native/zigenc/src/main.zig` |
| — | child: decoder MFT + GPU publish | `native/zigenc/src/dec.zig`, `mf.zig` |
| — | parent: `ProcDecSession`, inbound ring writer, frozen-destination oracle, pinning | `mfenc/procdec_windows.go`, `procparent_windows.go`, `mfenc_other.go` |
| — | engine selection + stats | `mediapipe/mf_dec_bridge.go`, `mediapipe.go`, `app.go`, `feat_media.go` |
| — | flag | `config/config.go` |
| — | UI | `ui/view_peers_media.go`, `webui/render_peers.go`, `i18n/locales/en.json` |
| — | gates | `mfenc/procdec_windows_test.go`, `mfenc/decode_live_spout_windows_test.go`, `mediapipe/zerodecode_gate_test.go`, `mediaroute/zerodecode_test.go` |

Also fixed (audited defects inside the receive surface, own commits): the torn-frame
read-after-recycle, the NACK window's raw-frame retention, the invisible drop counters, and
`spout_shim.h`'s dead `rave_spout_roundtrip` declaration. See §"Neighbour fixes".

## Protocol delta (implemented)

`open` gains `dir:"enc"|"dec"` (absent = `"enc"`), `codec:"h264"|"hevc"`, `dsh` (destination
share handle), `dfmt`, `dname`, `in_ring_kb`. A `dec` session's SHM is header + INBOUND ring
only; the parent appends and signals `-f`, the child consumes and signals `-c`, `-a` never
fires. Header 128-207 is the ring-counter block a second time plus decode telemetry:

| off | field | writer |
|---|---|---|
| 128 | `inWrite` u64 | parent |
| 136 | `inRead` u64 | child |
| 144 | `inDropped` u64 | parent |
| 152 | `decBusyNs` u64 | child |
| 160 | `decFrames` u64 | child |
| 168 | `decErrors` u64 | child |
| 176 | `lastPubNs` i64 | child |
| 184 | `decFlags` u32 | child |
| 192 | `decDropped` u64 | child |
| 200 | `decMtxTimeouts` u64 | child |

New event `{"ev":"dstgone","sid":N,"reason":…}`. `hello.ver` → **3**; the parent requires
`>= 3` for `dir:"dec"` (a v2 child ignores the unknown field and would size its mapping from
`in_w*in_h*4`, past the end of the much smaller dec mapping — the R10-class hazard).

## Bounds (CLAUDE.md rule), each stated at its declaration

- Inbound AU ring: 4-16 MiB, bitrate-derived (geometry-independent, so a stream resize costs no
  SHM realloc). Full ring **drops the newest AU + counts it** (`inDropped`). Blocking would
  stall the route's jitter drain; a receive route that cannot keep up must lose frames.
- `AUPOOL` 4 input samples × `au_cap` (ring/4, clamped 512 KiB..4 MiB) ⇒ ≤ 16 MiB of MF memory
  buffers per session. An AU over `au_cap` gets a one-off sample released right after
  `ProcessInput` rather than being dropped.
- `VIEWCACHE` 16 decoded-surface input views, round-robin evict + release.
- `dec_batch` 8 AUs per wake so close/quit stay prompt.
- Per session: 0 host frame bytes, 1 destination texture reference, the decoder's own NV12 pool.

## Deviations from the spec, with reasons

1. **§10's `CreateSender(name,w,h,fmt)` + `GetHandle()` seam does not exist.** `SPOUTLIBRARY`
   has no `CreateSender`, and on the shipped SDK pairing `GetHandle()` returns NULL for a sender
   created through `SendImage`. So `rave_spout_open_sender` publishes ONE zeroed frame to force
   the texture allocation and then reads the handle + the ACTUAL format out of the registry
   (`GetSenderInfo` — the same shared-memory read the capture side is already proven against).
   Cost: one `w*h*4` write per ROUTE, into the pooled flip buffer, not per frame.
2. **`videoshare.SharedSender` is the same object as the frame sender**, not a separate type. A
   refused native session must keep publishing, and two Spout senders under one name is not a
   thing — so the eagerly created sender is a full `FrameSender` too.
3. **`MF_SA_D3D11_AWARE` + MFT-provided samples are HARD gates.** System-memory decoder output
   would mean uploading NV12 rows by hand, i.e. re-creating the host frame plane this increment
   removes. That case downgrades cleanly (`sw_decode_unsupported`) to the ffmpeg decoder.
4. **`dec.zig` builds its own D3D11 device/VP rather than sharing `Enc`'s.** The encode pipeline
   is inc-1's proven path and the parity reference; ~60 duplicated lines beat refactoring it.
   `mf.zig` grew a `pub const api` alias block (Zig has no friend visibility) so no existing
   declaration changed.
5. **An explicit `Flush` before releasing the destination mutex.** Not in the spec, and
   load-bearing: a named (CPU) access mutex carries no implicit flush the way
   `IDXGIKeyedMutex.ReleaseSync` does, so without it a receiver reads pre-blit content — a
   blank picture with **zero errors in every counter**. Same discipline Spout's own sender uses.
6. **`VideoProcessorSetStreamSourceRect` is wired.** A hardware decoder's NV12 surface is
   16-row aligned (measured: 640×**368** for 640×360), and the VP would otherwise sample the
   whole surface and squash the alignment rows into the output.
7. **The picture oracle is a GPU read-back INSIDE the child** (`probeBands`, env
   `RAVE_MATE_MFDEC_PROBE_BANDS=1`, off by default), not a Spout receive. Inc-1 deliberately
   added no test-only surface to the child; here there was no alternative — see §"Not verified".
8. **`recycleDest` resets both ring heads.** Bitstream that survived a reopen is unusable to a
   fresh decoder without a keyframe; the route's own PLI machinery asks the peer for one.
9. **Decode sessions ride the same `procChild` supervisor** (a second map, not a refactor of
   `ProcSession`). Without the additions to `wait()` a child crash would recover every send
   route and silently kill the receive ones.

## Verified live on this box (NVIDIA, real Spout sender, real child)

`go test -tags spout ./internal/mfenc -run TestDecodeLive -v` (needs `SpoutLibrary.dll` beside
the compiled test exe — see the inc-1 note).

- Destination sender created by Go, handle handed over, format 87 (B8G8R8A8_UNORM).
- Decoder bound: **"Microsoft H264 Video Decoder MFT"**, D3D11-aware, sync MFT,
  `provides=true outSize=460800`. The HARDWARE enumeration pass finds nothing on this rig, so
  the unflagged pass is what carries it — exactly the fallback the design anticipated.
- Decoder surface: 640×368 NV12, `ArraySize=6`, `BindFlags=0x200` (BIND_DECODER), subresource 0.
- 40 AUs in → **40 frames published**, `decErrors=0`, `inDropped=0`, `decDropped=0`,
  `mtxTimeouts=0`, **0.8-1.0 ms per AU**, `decFlags=0x5` = live + Spout's NAMED access mutex
  (the `IDXGIKeyedMutex` branch is unexercised here, same as inc 1).
- **Destination texture read back: top r=255 b=0, bottom r=1 b=255** — row order and channel
  order correct end to end (decode → BT.709 CSC → publish).
- The source bitstream is cross-checked with ffmpeg first (top r=232 b=1, bottom r=2 b=243), so
  an encoder/harness problem can never be attributed to `dec.zig`.
- A bogus destination handle comes back as `ErrDecodeRefused (open_shared)` from the real child.
- inc-1's `TestZeroCopyLive` still passes → the two `mf.zig` vtable pad splits
  (`SetStreamSourceRect`; `Map`/`Unmap`/`CopyResource`/`Flush`) did not shift `VideoProcessorBlt`
  or `UpdateSubresource`.

## NOT verified

- **Spout's own receive side cannot see a foreign-device write on this rig.** `ReceiveImage`
  reports success (rc=1, correct dims) and returns all-zero pixels — **including for an ordinary
  `SendImage` publish from another process**, which is why the read-back was recognised as an
  instrument failure rather than a decode failure. So: no OBS/Resolume verification of a
  natively decoded route was possible here. `ZIGMEDIA_DESIGN.md` §10 requires that before the
  flag flips, and it is still open.
- **The `IsFrameNew()` consequence is therefore unmeasured.** The design already accepts that a
  child-written texture cannot bump Spout's frame counter. Containment worth noting:
  `mediaroute.scan` excludes `rave-mate link ` senders from re-sharing (loop guard), so
  rave-mate's OWN `IsFrameNew`-gating receiver never consumes one. Only external apps do, and
  those copy the texture every tick.
- **7-day soak** and the **2-PC wire pass** (§13.1) — neither attempted.
- **4K60 receive soak / RSS flatness**: not run. The counters look right at 640×360 only.
- **A real end-to-end route** (peer → jitterbuf → `mfDecoder`) was not driven; the live gate
  feeds `ProcDecSession` directly. The factory/gate ladder is unit-tested, the wire is not.
- **HEVC**: the codec plumbing exists and is unit-tested, no HEVC bitstream was decoded.
- **A true hardware decoder MFT** (`flag_hw_decode`, bit 4): unexercised — this rig has none.
- **`dstgone` / destination-recycle against a live re-created sender**: the oracle and the
  wiring are unit-tested (changed handle, frozen clock), not driven by killing a real sender.
- **Fyne/webui route-panel rendering** of the new decode lines was not eyeballed; covered by
  the existing renderer tests only.
- **1e** (AU in-place read + ring reclaim) and the NACK raw-video carve-out lift (increment 5)
  remain deliberately out of scope.

## Neighbour fixes shipped alongside (own commits)

1. **Torn frames on every received route.** `videoshare`'s Spout backend queued the caller's
   `*image.NRGBA` and read it LATER inside cgo, while `mediaroute`'s receive sink forwards
   `mediapipe`'s decode buffer verbatim and the decoder recycles it the moment `Write` returns.
   `handoff.go` makes `Send` wait for the read; a displaced or budget-expired UNCLAIMED frame is
   reclaimed and reported unsent, a CLAIMED one is waited out (abandoning it is the race).
   `TestHandoffCanaryTornByFireAndForget` is the non-vacuity arm.
2. **NACK window vs raw frames.** `retainOrRelease` retained UNPOOLED raw video (`Release == nil`),
   so every webcam frame displaced the whole 16 MB window. The exemption is now keyed on RAW,
   not ownership. `TestRebufExemptsUnpooledRawFrames` fails on the old rule.
3. **Invisible drop counters** (design §12.4 item 4): `PipelineStats.Dropped` +
   `medialink.InnerDrops` sums a wrapped stage into the one reporter the router asks; both
   renderers print it. `mfBridge.dropped` was also a data race.
4. `spout_shim.h`'s dead `rave_spout_roundtrip` declaration deleted.

## Neighbour defects found, NOT fixed (reported)

1. `webcam/framepipe.go:24` still allocates a fresh full frame per capture (250 MB/s at
   1080p30). Design §12.4 item 1 / increment **4** (the produce paths) — out of scope here: inc 2
   is strictly the receive side. The dangerous half (window pollution) is fixed above.
2. `videoshare/spout_shim.cpp` still does a per-frame scalar transpose when `RAVE_SPOUT_FLIP != 0`
   (pooled, so no malloc churn, but a full-frame pass at 4K on the SEND path). Increment 4.
3. `videoshare/sender_spout_test.go:29` `TestSpoutSenderRegisters` hard-fails without
   `SpoutLibrary.dll` while every other Spout test skips cleanly. Unchanged by this branch,
   reproducible on `development`.
4. `internal/dmx` `TestRouterIngestToGrid` binds a fixed UDP port (16454) and is flaky under a
   parallel `go test ./...`. Unchanged by this branch.
5. `mediapipe/decode.go`'s `decFreeRing` comment claims "Cap in BYTES = 4 × W·H·4 (32 MB at
   1080p)" — that is 8 MB per frame at 1080p, so the cap is 32 MB only at that geometry; at 4K
   the same ring is 132 MB. The bound is real, the stated number is geometry-specific.

## Gate results (verbatim)

```
gofmt -l .                                        (clean)
GOWORK=off go vet ./...                           (clean)
GOWORK=off go test ./...                          all ok
GOWORK=off go build -tags "zigdsp zigui zigvr encembed" ./...                   TAG1-OK
GOWORK=off go build -tags "spout vr zigdsp zigui zigvr encembed" ./...          TAG2-OK
bash scripts/build-zig.sh                         rave-mate-enc built + embed-staged (0.16.0)
zig fmt --check src/                              (clean)
zig build test --summary all                      9/9 tests passed
go test -tags spout ./internal/mfenc -run 'TestDecodeLive|TestZeroCopyLive'     PASS
```
