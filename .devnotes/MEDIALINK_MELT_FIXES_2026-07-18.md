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
- `MediaLink.Subprocess` isolation child: still default-off/unverified (config comment).

# Capture-path pass (2026-07-25, WP-5)

Second wave, same incident. WP-1/3 (engine + device select) and WP-4/6 (QoS + isolation)
land alongside; this section is the CAPTURE path only.

| Cause | Fix |
|---|---|
| `MaxFPS` applied AFTER the readback: a 120 fps source capped to 60 still paid 120 full-frame GPU→CPU copies/s and dropped half | `fpsGate` inside the Spout poll loop (`videoshare/fpsgate.go`): over-budget ticks skip `ReceiveImage` entirely. `RecvOptions.MaxFPS` + live `FPSLimiter.SetMaxFPS`. Connect/resize detection stays at the fast poll rate |
| Per-route capture duplication: each route = own FrameReceiver + GL context + 250 Hz poll of the SAME texture | `mediaroute/capture.go` `captureHub`: refcounted capture per sender name, fanned out to N routes. Capture rate = the FASTEST subscriber (uncapped wins), re-rated live on attach/detach; each route drops to its own cap downstream. One pooled buffer, N refs (`pixRef`), pool gets it back exactly once |
| `rebuf.add(f)` retained pooled capture buffers with NACK armed (always) → the pool never refilled, every readback re-allocated 8/33 MB | `routeIO.retainOrRelease` (nack.go): unpooled AUs retained as before; pooled compressed AUs COPIED into the window (≤1 MiB); pooled raw pixels exempt (intra + one 4K frame evicts the 16 MB window anyway) |
| Registry scan churned 1+2N COM objects every 2 s | ONE process-wide `registry()` handle in the shim + `rave_spout_scan` (names+dims in one call) + 1 s TTL cache in `videoshare/scan.go` |
| Explicit `MaxHeight` on a HW tier resized with CPU swscale | `scaleFilter`: `scale_cuda` (NVENC) / `scale_qsv` (QSV); AMF + software keep swscale (scale_amf/scale_d3d11 are ffmpeg 7.1+). Early-fail demotion pins swscale if the local ffmpeg rejects the chain |
| Receive side allocated a full raw frame per frame and moved it ~3× (append → copy → memmove) | `decoder.pumpFrames`: reads land directly in the frame buffer capped at the missing bytes (one copy, no memmove), buffers from a bounded ring recycled after `Write`. `Sink.Write` contract: must not retain `Payload` |
| mocapnode never returned pooled readback buffers | `PutPix` after the RGB24 conversion copy |

Semantics to remember: **shared capture is per Spout sender name, not per route.** Spout has
exactly one capture format (NRGBA at the sender's current size) and the receiver detects resize
itself, so nothing per-route needs reconciling there; per-route differences (fps, MaxHeight,
codec) all live downstream of the single readback.

Needs the 2-PC rig (cannot be unit-tested): real Spout readback rate at a cap, two peers on one
source showing ONE poll loop, `scale_cuda`/`scale_qsv` acceptance by the installed ffmpeg (and the
demotion warning when rejected), and the end-to-end sender-CPU/bandwidth delta while OBS streams.
