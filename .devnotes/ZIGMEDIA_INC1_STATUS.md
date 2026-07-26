# zigmedia increment 1 - status

Branch `feat/zigmedia-inc1` off `development` @7ab3d3b. Spec: `ZIGMEDIA_DESIGN.md` part 1.
Flag **OFF by default** (`config.MediaLink.zigCapture`, env `RAVE_MATE_ZIGMEDIA_CAPTURE`).

## Shipped

| # | work | where |
|---|---|---|
| 1b | shim exports the sender's share handle + DXGI format | `videoshare/spout_shim.{h,cpp}`, `share.go`, `share_{spout,noop}.go` |
| 1a | lazy captureHub attach + `ZeroCopySource` | `medialink/pipeline.go`, `mediaroute/mediaroute.go` |
| 1a | session plumbing, header v2, ring sizing, srcgone, R1 oracle, pinning | `mfenc/procparent_windows.go`, `procstats.go` |
| 1a | flag gate + open ladder + stats | `mediapipe/mf_bridge.go`, `config/config.go`, `app.go`, `feat_media.go` |
| 1c | child capture + pacing loop | `native/zigenc/src/cap.zig`, `mf.zig`, `main.zig` |
| 1a | PipelineStats rendered (was collected, never displayed) | `ui/view_peers_media.go`, `webui/render_peers.go` |
| 1d | gates | `mfenc/zerocopy*_test.go`, `mediapipe/zerocopy_gate_test.go`, `mediaroute/zerocopy_test.go` |

Not done, deliberately: **1e** (AU in-place read + ring reclaim - own flag, changes ring
semantics) and the NACK raw-video carve-out (increment 5).

## Deviations from the spec, with reasons

1. **`pts0` added to `open`** (additive field beyond the frozen set). The child would otherwise
   stamp AU pts from raw QPC, but `jitterbuf.updateBase` and `telemetry.transit` compare pts
   against the SENDER's clock, and the readback path stamps `time.Now().UnixNano()`. The child
   now computes `pts0 + qpc elapsed`, so the two paths are timebase-identical. Gated by a live
   assertion that the first AU's pts is wall-clock ns.
2. **No `CopyResource`, and the mutex is held across the Blt instead.** The spec's grab sketch
   copies into "the VP input view's resource", which is the shared texture itself when the view
   is over it (§4 also demands zero own input texture, -33 MB VRAM). Holding the sender's mutex
   across `Enc.feed` would be the documented pointer-lag hazard, so `Enc.feed` was split into
   `gateInput` / `bltView` / `submitSlot`: the encoder wait happens BEFORE the acquire, and the
   lock covers exactly one GPU-queued Blt - the same discipline Spout's own receiver uses. The
   encode half is otherwise unmodified, which is what keeps the parity gate meaningful.
3. **Mid-route exhaustion re-pins on re-establish, not in place.** §7.3 says pin the route to
   `src:"shm"` after N=3 failed rescans. Swapping a LIVE route's session from zero-copy to
   readback means re-arming `mfBridge`'s feed/emit goroutines mid-flight; instead the session
   fails cleanly, the AU stream ends, the route re-establishes, and a per-sender pin
   (`ZeroCopyPinnedToReadback`) sends the new route down the readback path. Same end state, no
   live goroutine surgery on the hot path.
4. **`EncBusyMs` added to ProcStats/PipelineStats.** The parent submits nothing on a zero-copy
   route, so `submitAt`/p50/p99 are structurally empty there and the panel would have shown
   0.00 ms for the one path being added. `encBusyNs / capFrames` over the read interval is the
   honest per-frame cost.
5. **Pixel parity is gated by decode, not by an NV12 dump.** The spec's harness wants the child
   to dump NV12 of frame N in a test mode. Decoding the real bitstream with ffmpeg and asserting
   a red-top/blue-bottom probe covers the two failure modes that matter (row order, channel
   order) end to end, without adding a test-only surface to the child. Non-vacuity comes from
   the probe itself: a flip or a swizzle moves the sampled bands.

## Verified live on this box (NVIDIA, real Spout sender in a SECOND process)

Two processes are mandatory - a same-process Spout send+receive deadlocks in the driver's keyed
mutex, the same reason mediaroute refuses same-PC routes.

- 1280x720@60: `capFlags=0x5` (zero-copy live + Spout's NAMED access mutex → the inferred
  `<sender>_SpoutAccessMutex` name is **confirmed by execution**, risk R2), capFrames 90/1.5 s,
  capFPS 60.0, skips 0, mutex timeouts 0, src errors 0, encode 0.28 ms/frame, staleMs 16,
  90 AUs, first AU a keyframe, pts wall-clock.
- Orientation + colour: decoded probe intact (top red, bottom blue) → **risk R5 closed**.
- 4K60 @ 50 Mbps, 45 s per arm: zero-copy host RSS 29→30 MB / child 69→70 MB / 2880 AUs;
  readback control host 206→207 MB / child 170→170 MB / 2864 AUs. Both flat. No orphan
  `rave-mate-enc.exe` after either run.
- A bogus share handle comes back as `ErrZeroCopyRefused` from the real child (open-side
  downgrade rung, on hardware).

## NOT verified

- **7-day soak** (required before the flag defaults on).
- **2-PC wire pass** (§13.1): the single-box soak proves memory + throughput, not the wire, and
  the sender-PC pointer-lag regression the pacing rule exists to prevent is only observable on a
  real rig with a real sending app (OBS).
- **Live sender restart / canvas resize** against a real app: the R1 oracle is gated by unit
  tests (changed handle + frozen clock) and the recycle wiring, not by killing OBS mid-route.
- **A keyed-mutex sender**: this rig's Spout senders expose the NAMED mutex (`capFlags` bit 2),
  so the `IDXGIKeyedMutex` branch (bit 1) is unexercised at runtime.
- **Cross-adapter refusal (R7)** and **TYPELESS/exotic formats (R4)**: the allowlist + probe are
  unit-tested, but no rig here produces those senders.
- **Fyne/webui route-panel rendering was not eyeballed** - the new stats lines are covered by
  the existing renderer tests only.

## Defects found in neighbouring code (reported, not fixed)

1. `videoshare/spout_shim.h:37` declares `rave_spout_roundtrip` with no implementation anywhere.
   Dead declaration; any caller would fail to link.
2. `videoshare/spout_shim.cpp:86-110` still does the per-frame geometric flip on the CPU for
   `flip != 0` (pooled now, so no malloc churn, but still a full-frame scalar transpose at 4K on
   the SEND path). Increment 4's target.
3. `internal/dmx` `TestRouterIngestToGrid` binds a fixed UDP port (16454) and fails under a
   parallel `go test ./...` when anything else holds it; passes in isolation. Flaky by design.
4. `mediapipe/mf_bridge.go` `dropped` and `mediaroute` frame-drop counters still reach nobody
   (design §12.4 item 4) - the capture counters now render, these do not.
5. `videoshare/sender_spout_test.go:29` `TestSpoutSenderRegisters` **hard-fails** when
   `SpoutLibrary.dll` is not resolvable (the DLL is runtime-loaded, so it is absent unless staged
   beside the test binary), while every other Spout test in the tree skips cleanly. Unchanged by
   this branch and reproducible on `development`; the fix is a skip like `soak_spout_test.go` uses.
   Practical note for the tagged lane: `go test -tags spout ./internal/videoshare` needs
   `third_party/spout/bin/SpoutLibrary.dll` next to the compiled test exe (build with
   `go test -c -o <dir>/x.test.exe` and copy the DLL there - PATH does not help, the shim's
   `LoadLibraryA` searches the exe dir).
