# PipeWire video capture source (`internal/pwcapture`)

Linux SENDER side: capture a PipeWire video node and route it to a paired Windows rave-mate over
the existing medialink. Target producer is Spout2PW from VRChat under Proton
(`spout2pw.VRChat.VRCSender1`, media.class `Stream/Output/Video`, 1920x1080). Implements
`.devnotes/MEDIALINK_DESIGN.md` §P7/D8 (cgo `libpipewire-0.3` dlopen shim, no go.mod dep, GStreamer
+ pure-Go rejected).

## Architecture

- **Discovery** (`pwcapture.go`, cross-platform, pure Go): parse `pw-dump -N` JSON, keep nodes whose
  `media.class` is `Stream/Output/Video` or `Video/Source`. Pull name + description + `EnumFormat`
  (format, size, framerate). A node is **capturable** when it offers an RGBA-family format
  (BGRA/RGBA/BGRx/RGBx) - a YUY2/NV12 camera is listed but not registered (no colour-convert path in
  v1). Pure pw-dump => testable without hardware (`pwcapture_test.go`), and the daemon can read it
  directly for `ctl pw-nodes` without touching the media child.
- **Manager** (`manager.go`, cross-platform): polls discovery (3s), reconciles `RegisterSource`/
  `UnregisterSource` on the medialink router with the real dims/fps so a peer routes by node name;
  withdraws a node that vanishes. This is the appear/disappear half of reconnect (VRChat restart);
  the mid-stream half is the capture stream auto-reconnecting by node name.
- **Capture** (`shim.c` + `capture_pipewire.go`, `//go:build linux && pipewire`): a cgo shim that
  `dlopen`s `libpipewire-0.3.so.0` at runtime (nothing load-time-linked, no go.mod dep; absent
  libpipewire => `rpw_available()==0` => clean no-op). One capture = one `pw_thread_loop` + one
  input `pw_stream` connected by `PW_KEY_TARGET_OBJECT` name with `AUTOCONNECT` (reconnects when the
  producer returns). Frames land in a **bounded single slot** (drop-oldest + `dropped`/
  `dmabuf_skipped` counters); `on_process` drains to the newest queued buffer (low latency). The Go
  `pwSource.Next` pulls the latest (`rpw_wait_frame`, 200ms timeout → loop so a reconnect gap
  doesn't EOF the route). One reusable RGBA buffer, returned without a copy (serial Next/consume by
  the encode feeder; `Release` nil).
- **No-op** (`capture_noop.go`, `!(linux && pipewire)`): discovery still works for `ctl pw-nodes`;
  `openCapture` errors, nothing is registered. Every non-Linux build and Linux-without-the-shim
  degrades cleanly.

Wired in `internal/featurehost/feat_media.go` beside the webcam manager (same isolated media child).

## Formats

Offered in `EnumFormat`: BGRA/RGBA/BGRx/RGBx, size 1..8192, framerate 0..240. `on_param_changed`
reads the negotiated format; requests SHM buffers (`MemFd|MemPtr`, also `DmaBuf`). `to_rgba` converts
to Go NRGBA order handling stride + the 4 layouts.

## Build

`CGO_ENABLED=1 go build -tags pipewire ./cmd/rave-mate` in the pinned Debian box
(`contrib/linux-build/`, carries `libpipewire-0.3-dev` + `libspa-0.2-dev` + EGL/GLES2/gbm/drm). The
shim CFLAGS hardcode the Debian include dirs (`/usr/include/pipewire-0.3`, `/usr/include/spa-0.2`)
on purpose - this builds ONLY in that box, and pkg-config would add `-lpipewire` (load-time link),
which we must not have. Build headers are pipewire 1.4.2; runtime on sus is 1.6.10 (API/ABI stable).

Fixed the previously-unbuildable `-tags pipewire` state: `videoshare/sender_noop.go` now also covers
`pipewire` (the tag drives this CAPTURE source, not a videoshare pipewire SENDER, which stays no-op).

## SHM path — VERIFIED (sus, 2026-11-11)

`gst-launch-1.0 videotestsrc is-live=true ! video/x-raw,format=BGRx,width=1920,height=1080,
framerate=60/1 ! pipewiresink mode=provide stream-properties="props,node.name=rmtest.video,
media.class=Stream/Output/Video"` creates a Stream/Output/Video producer. The capture test binary
(`go test -c -tags pipewire`, built in the box, run on sus) captured **60 frames, 8294400 bytes
each (1920x1080 RGBA), ~56.7 fps, non-zero pixels**. Discovery + `ctl pw-nodes` list it as
capturable with the right dims/fps.

## libfunnel producer EnumFormat (measured 2026-11-11, sus)

Test producer `contrib/linux-build/testprod/funnel-producer.c` (libfunnel, the exact lib Spout2PW
publishes with; headless surfaceless EGL, frame-counter pattern). Its node
(`page.rave.mate.testprod.<name>`, media.class Stream/Output/Video) advertises:
- **8 formats**: BGRA, ABGR, RGBA, ARGB, BGRx, xBGR, RGBx, xRGB (all 32bpp).
- Each format carries a **modifier CHOICE** of ~11 values: AMD GFX11 (Navi31) tiling modifiers
  `0x0200000028a6bf04, …28a67f04, …28a37f04, …28a01f04, …28a6bb04, …28a67b04, …28a37b04,
  …28a01b04, …28801604, 0x02000000000a04` + **LINEAR `0`** (vendor 0x02 = AMD).
- **NO SHM entry** — every format is dmabuf-with-modifier. So the SHM-only consumer CANNOT capture
  libfunnel/Spout2PW (confirmed): dmabuf+EGL with modifier negotiation is mandatory. The same GPU's
  EGL reports these same AMD modifiers, so a non-linear (zero-copy tiled) modifier is fixated.
- Sync: producer run with `-either` (FUNNEL_SYNC_BOTH); explicit-sync fences ride the buffer meta
  (SPA_META_SyncTimeline / dmabuf fence), handled at capture time, not in the node params.

## DMA-BUF path — IMPLEMENTED (shim.c); 1080p VERIFIED, 4K limited (2026-11-12)

EGL import + readback is implemented in `shim.c` and verified against the libfunnel producer:
- **dlopen** `libEGL.so.1` + `libGLESv2.so.2` + `libgbm.so.1` (no link, so absent EGL => SHM-only,
  same degrade as libpipewire). EGL display/context + `eglQueryDmaBufModifiersEXT` per format run in
  `rpw_start` (no current context needed); `eglMakeCurrent` + all GL run on the pw loop thread.
- **Modifier negotiation + fixate dance**: EnumFormat advertises, per format, the EGL-reported
  modifiers as a DONT_FIXATE enum choice (`build_dmabuf_format`) plus a SHM fallback object. On
  `param_changed` with the modifier still a choice, `fixate_format` re-submits a single modifier
  (intersection of the producer's offer and EGL's list). Verified: the fixate picks the native
  AMD GFX11 tiled modifier `0x200000028a6bf04` and negotiates dmabuf.
- **Import**: `eglCreateImageKHR(EGL_LINUX_DMA_BUF_EXT)` with per-plane fd/offset(chunk)/pitch +
  modifier LO/HI, `glEGLImageTargetTexture2DOES` -> GL_TEXTURE_2D -> FBO -> `glReadPixels(GL_RGBA)`
  (channel-correct: the GL stack swizzles per fourcc) -> vertical flip (GL is bottom-up) into the
  bounded slot. GL objects reused across frames (one FBO); EGLImage + texture per frame, destroyed.
- **Pixel-verified** at 1080p (`TestCaptureDmabufPattern`): corners TL=red TR=green BL=blue BR=white,
  alpha=255 - orientation/flip, channel order and detile all correct.

### Modifier / plane findings (hard-won)
- The chosen tiled modifier is **single-plane at 1080p** (imports correctly) but **multi-plane at 4K**
  (DCC adds metadata planes: 3 at 3840x2160). mesa's `eglCreateImageKHR` returns **EGL_BAD_MATCH**
  for that multi-plane DCC layout here, so 4K dmabuf import fails on the first consumer.
- libfunnel's **LINEAR (modifier 0) is mislabelled**: it reports modifier 0 but the buffer is not
  actually linear (garbage on readback) and comes with a stray `chunk->size==0` plane. So LINEAR is
  NOT a usable fallback; the shim prefers the first real tiled modifier and keeps LINEAR last.
- Net: **1080p60 dmabuf works** (the primary VRChat resolution). **4K60 is limited** - import fails
  on the multi-plane DCC buffer, and even where it negotiates a single-plane layout the readback is
  CPU-bound below 60fps (see measurements). The real fix for 4K is the zero-copy path below.

### DMA-BUF measurements (sus RX 7900 XTX, libfunnel producer, 2026-11-12)

capture -> VA-API H.264 encode (`TestCaptureEncodeE2E`), 180 frames:
- **1080p60**: ~58.5 fps (realtime). CPU: pwcap ~41%, ffmpeg(vaapi) ~75% of one core. RSS pwcap ~97 MB,
  ffmpeg ~126 MB. Pattern pixel-check PASS.
- **4K60**: ~36 fps (BELOW realtime). CPU: pwcap ~146%, ffmpeg ~204%. Pattern FAILS (multi-plane DCC
  import). So 4K readback cannot hold 60: the 33 MB/frame glReadPixels + flip + copy pins >1 core and
  the DCC multi-plane import is rejected.

**Soak (1080p60 dmabuf, 600s, 5 producer restarts, `TestCaptureSoak`):** capture -> VA-API encode
sustained **~58.9 fps, 0 stalls** across all 5 restarts (reconnect via `do_reconnect` on stream
ERROR recovers every time). **No leak**: pwcap RSS 97656 -> 97948 kB (+0.3 MB/10 min), fds 44 -> 44;
ffmpeg RSS 125756 -> 126528 kB, fds 8 -> 8. 35360 frames / 1405 MB total.

**Zero-copy dmabuf -> VAAPI (follow-up, NOT built):** the 4K answer. Instead of
EGLImage->glReadPixels->RGBA->ffmpeg rawvideo pipe, hand the dmabuf straight to the VAAPI encoder:
feed ffmpeg `-hwaccel vaapi -hwaccel_output_format vaapi` consuming the dmabuf (or a native libva
path) so the tiled surface (incl. DCC) never leaves the GPU and no readback/copy happens. That also
sidesteps the EGL multi-plane-DCC import limitation (VAAPI imports the dmabuf directly). Prefer the
TILED modifier there (not LINEAR). Build it only once 4K is actually required.

## End-to-end (single box) — VERIFIED (sus, 2026-11-11)

`TestCaptureEncodeE2E` drives the full sender data path on one box: the gst producer node ->
`pwcapture.Source` -> `mediapipe.Factories` VA-API encode child -> Annex-B H.264. Result at
1920x1080@60: **180 H.264 frames in 3.08s (~58.4 fps), 2 keyframes** (GOP 120 = 2s), ~53 KB/frame
(~9.6 MB total). CPU during the run: `pwcap.test` ~35-41%, `ffmpeg` (h264_vaapi child) ~58-63% of
one core (the encode is on the GPU; this CPU is RGBA capture + swscale NV12 + hwupload + pipes). RSS
~170 MB (Go test binary) / ~126 MB (ffmpeg). 0 separate config AUs is expected - `dump_extra=freq=
keyframe` folds SPS/PPS into each keyframe AU. The network transport past the encoder is
codec-agnostic and already P4-shipped, owned by the transport agent; not re-exercised here.

Parser bug found + fixed during E2E: pw-dump `props` mix value types (numbers like `client.id`,
booleans like `node.async`), so the original `map[string]string` unmarshal failed on the whole
document and discovery found nothing. Props are now `map[string]json.RawMessage` + `propStr`; the
unit-test sample carries mixed types to lock the regression.

## Follow-ups

1. **Verify DMA-BUF against the real VRChat node** when it's running (pw-dump its EnumFormat +
   modifiers; run the capture test with `RPW_TEST_NODE=spout2pw.VRChat.VRCSender1`).
2. **Zero-copy dmabuf → VAAPI** (import the dmabuf straight into the VAAPI encoder, no RGBA readback)
   - the §6 zero-copy path; only if the EGL readback can't hold 4K60.
3. **YUV camera formats** (NV12/YUY2) so `Video/Source` cameras are capturable too.
4. Multi-GPU EGL device selection (match the VAAPI render-node pick).

## 4K60: VA-API native zero-copy engine (feat/pw-4k, 2026-11-12)

The EGL readback path cannot do 4K60 (import rejects multi-plane DCC; RGBA readback is CPU-bound
~36fps). **Decision: Option A — zero-copy dmabuf -> VA-API in-process** (`internal/pwcapture/vaenc.c`,
libva/libva-drm dlopen'd). B (fix EGL + GPU NV12 readback) was rejected: its precondition is fixing
the same 4K DCC import EGL fundamentally rejects.

Precondition probes (sus, RX 7900 XTX, vainfo): libva 2.24 (`libva.so.2`/`libva-drm.so.2`); H264
Main/High + HEVC Main/Main10 + AV1 = **VAEntrypointEncSlice** (full, no LP); **VAEntrypointVideoProc**
present. libfunnel 4K producer fixates AMD GFX11 modifier `0x200000028a6bf04`; plane count grows with
size: **1080p = 2 planes** (color + DCC), **4K = 3 planes** (color + DCC + DCC_RETILE display aux).

VA import probe (`rva_import_probe`: dmabuf -> DRM_PRIME_2 VASurface -> VPP RGB->NV12 (+VA_MIRROR_
VERTICAL; the GL producer renders bottom-up) -> vaDeriveImage NV12 -> corner read):
- **1080p (2-plane DCC): rc=0, corners CORRECT** (TL red / TR green / BL blue / BR white, within YUV
  roundtrip tolerance). **VA imports the DCC dmabuf that EGL's eglCreateImageKHR rejected.**
- **4K (3-plane DCC_RETILE): import fails** (`invalid parameter`; the 2-plane cap gives 3/4 corners,
  `{0,last}` = `resource allocation failed`). The driver's retile layout is not VA-importable here.
- **Producer `AMD_DEBUG=nodcc` => 1-plane tiled (`0x200000028a01f04`) at BOTH 1080p and 4K, rc=0,
  ALL corners CORRECT.** So 4K is pixel-correct with a non-DCC producer buffer.
- LINEAR (modifier 0) rejected: libfunnel mislabels a tiled buffer as linear (garbage via both EGL
  and VA).

**Conclusion:** VA import + VPP works and is pixel-correct. 1080p works with DCC as-is. **4K requires a
non-DCC producer buffer** (`AMD_DEBUG=nodcc` on VRChat/Spout2PW, or any single-plane tiled dmabuf) -
a producer-side allocation choice the consumer can't override (the driver adds DCC per AMD_DEBUG, not
per the negotiated modifier). The shim imports 1-plane + 2-plane layouts and rejects 3-plane with a
clear error. Zero-copy => CPU is control + the compressed-bitstream readback only (well under the
1.5-core budget). Engine build (NV12 -> H.264 EncSlice, packed headers, CBR, IDR/reconnect, Annex-B)
follows.

### VA-native H.264 encoder - WORKING, pixel-correct (2026-11-12)

`vaenc.c` now has the full zero-copy engine: dmabuf -> VA import -> VPP (RGB->NV12, VA_MIRROR_
VERTICAL, scale) -> H.264 VAEntrypointEncSlice. Constrained Baseline, CAVLC, no B-frames, 1 ref
(P refs previous), CBR, IDR interval = 2s + on request. radeonsi needs app-written packed headers,
so the shim emits SPS/PPS/slice bitstreams (Annex-B, emulation-prevention) itself; an AUD is
prepended per AU. All on the GPU - only the compressed AU crosses to Go.

Verified on sus (RX 7900 XTX) vs the libfunnel producer (AMD_DEBUG=nodcc), decoding the output with
ffmpeg + numeric corner check (RPW_VA_ENC path, TestVAEncodeDump):
- **1080p60: ~59.9 fps, pwcap CPU ~2.3%** (0.02 core - zero-copy). Decoded corners TL(232,0,1)=red
  TR(19,255,8)=green BL(2,0,243)=blue BR=white - pixel-correct within codec tolerance.
- **4K60: ~55 fps (3s run incl. startup; CPU-trivial so sustained is ~60), pwcap CPU ~2.6%.** Decoded
  corners identical + correct. **4K encode works, pixel-correct, at ~2.6% CPU (<< 1.5-core budget).**
- CBR hits target: ~41.6 KB/frame at 20 Mbps/60.

**4K requires a non-DCC producer buffer** (AMD_DEBUG=nodcc), same as the import finding: the 3-plane
DCC_RETILE layout the driver adds at 4K is not VA-importable; nodcc gives a 1-plane tiled buffer that
imports + encodes correctly. For VRChat/Spout2PW: set AMD_DEBUG=nodcc in its launch env.

Status: the engine is proven end-to-end via the RPW_VA_ENC test hook in on_process. Production wiring
(negotiate "vaapi-native" so the pwcapture Source emits H.264 directly, bypassing the mediapipe
ffmpeg encoder; featurehost plumbing) is the remaining integration - larger, touches medialink/
mediapipe, not yet done. HEVC: the same path with VAProfileHEVCMain + HEVC packed headers (follow-up).

### Pipelined encoder + sustained-rate verification (feat/pw-4k, 2026-11-12)

The encoder now overlaps GPU work: `rva_enc_frame` submits frame N (BeginPicture/Render/EndPicture, no
sync) and emits frame N-1 (SyncSurface + MapBuffer + Annex-B copy) that was submitted the previous
call. `struct rva_enc` holds `nv12[2]`/`coded[2]` ping-pong surfaces + a 1-deep pending slot. Import
surfaces are cached by dmabuf fd (16 entries) so a steady buffer pool never re-imports. This lifted 4K
from ~55 fps (prior, submit+sync inline) to a steady ~58.5 fps.

Measured on sus (RX 7900 XTX, live KDE/Parsec desktop) via the RPW_VA_ENC hook, producer = libfunnel
`AMD_DEBUG=nodcc`, 60-frame warm-up excluded, CPU sampled from /proc utime+stime:
- **1080p60: ~60.2 fps**, pwcap CPU ~0.9% of one core, VmHWM ~67 MB. Decoded corners pixel-correct
  (TL red / TR green / BL blue / BR white).
- **4K60 (3840x2160): ~58.5 fps** sustained over 30 s (1754/1800 frames), **0 stalls**, pwcap CPU
  **~1.4% of one core** (0.014 core - zero-copy, GPU does VPP+encode), VmHWM ~67 MB. Decoded full
  3840x2160, corners pixel-correct. CBR ~41.6 KB/frame @ 20 Mbps.

**10-minute 4K soak with 5 producer restarts (TestVAEncodeSoak, 2026-11-12): PASS.** 600 s, 34778
frames, ~58.0 fps avg (each ~1 s producer-down + reconnect costs ~70 frames, so the 30 s window after
a restart reads ~1685 vs ~1758), **0 stalls**, clean resume across all 5 restarts (AUTOCONNECT
re-binds to the node.name). Memory bounded: VmRSS ~70 MB at the end (steady ~67 MB in short runs; ~+3 MB
over 10 min / 5 reconnects - small, not a runaway). FDs bounded (~37 steady: dmabuf pool + VA/EGL/
pipewire). Pool-8 build re-confirmed afterwards: 4K 58.7 fps, CPU 1.1%, corners pixel-correct.

**The ~1.5 fps 4K shortfall vs 60 is NOT the encoder.** The test producer renders a verified steady
**60.0 fps** (its own `render X fps` log), and the consumer sits at 1.4% CPU with 0 stalls - so the
encode keeps up with ~98.6% idle headroom. The loss is newest-wins frame dropping in on_process
(shared capture layer): under live-desktop GPU contention a few cycles per second run long enough that
two buffers queue and the older is dropped before encode. This is the correct low-latency behaviour for
a live stream (drop stale, show newest) and is the same delivery behaviour the production featurehost
path would have; it is a capture-phase artifact, not an encode bottleneck. 58.5 is ~0.5 fps under the
>=59 target; a larger PipeWire buffer pool did NOT help (ruled out buffer starvation); fully closing it
would need moving the per-frame GPU sync off the PipeWire thread (dedicated encode thread), a bigger
refactor deferred to production wiring.

### DMA-BUF sync mode is now explicit-only on this box (2026-11-12)

Mesa/driver on sus now sets `api_requires_explicit_sync` for the EGL producer API: libfunnel's
`funnel_stream_configure` rejects `FUNNEL_SYNC_IMPLICIT` **and** `FUNNEL_SYNC_BOTH` (it falls through to
implicit) with EINVAL ("default sync mode is unsupported"). The producer must run `-explicit` (frontend
+ backend explicit) or `-hybrid` (frontend EXPLICIT satisfies the EGL API, backend BOTH so an
implicit-sync consumer can still negotiate). `-hybrid` is what the drivers use now. This is an
environment change since the earlier soak (which used implicit); it does not touch our consumer code.

### ROOT CAUSE of the "no more input formats" reconnect churn (2026-11-12)

The churn is NOT a modifier/fixate negotiation bug. The consumer connects with
`PW_KEY_MEDIA_ROLE="Camera"` + `PW_KEY_TARGET_OBJECT=<name>` + AUTOCONNECT. `PW_KEY_TARGET_OBJECT`
matches a node's **node.name or object.serial, NOT node.nick**. libfunnel registers the producer as
node.name `page.rave.mate.testprod.<streamname>` with node.nick `<streamname>`. When the target string
is only the nick (or otherwise doesn't match node.name), the match fails and wireplumber's camera
policy (triggered by the Camera role) auto-links the consumer to a **real V4L2 webcam** instead (on
sus: C920 / "3D Camera", YUY2 1920x960 / MJPG). Those formats don't intersect the consumer's
RGBA/BGRA dmabuf EnumFormat, so the link errors `no more input formats`, disconnects, AUTOCONNECT
retries, and it churns forever delivering 0 frames. Confirmed from the pipewire daemon journal:
`pw.link: (100.0.0 -> 231.0.0) negotiating -> error no more input formats` where node 100 was the
webcam, not the funnel producer.

**Fix (verified): target the exact node.name**, e.g. `page.rave.mate.testprod.pw4ktest`, not the nick -
then negotiation reaches STREAMING and frames flow. Production must pass the real producer's exact
PipeWire `node.name` (VRChat/Spout2PW node). Optional hardening for production (cheap, but UNTESTED
against the real VRChat node so deferred): drop `PW_KEY_MEDIA_ROLE="Camera"` for a named
Stream/Output/Video target, so wireplumber's camera-default-device policy can't hijack the link when
the name doesn't match.

### REAL VRChat node inspected, 1080p AND 4K (2026-11-12)

Node `spout2pw.VRChat.VRCSender1` (Stream/Output/Video), VRChat running under Proton + Spout2PW, read
-only second consumer by exact node.name (service route untouched). Findings:

- **4K import works as-is, single-plane.** At 3840x2160 the node negotiates BGRA and the fixated
  modifier is `0x200000028a01f04` (**planes=1**, NOT the 3-plane DCC_RETILE my GL libfunnel producer
  emitted without nodcc). VA DRM_PRIME_2 import `rc=0`, zero-copy, real picture. **True even with the
  DCC filter DISABLED** (RPW_ALLOW_DCC=1) - Spout2PW re-exports a DX11/Spout texture, which GBM
  allocates single-plane; it is not DCC-compressed like a GL render target. **DCC VERDICT: real 4K needs
  NO game launch-option change (no AMD_DEBUG=nodcc); the consumer DCC-modifier filter is harmless
  insurance for producers that WOULD offer DCC.** The producer still OFFERS the full DCC+non-DCC family
  in EnumFormat; the filter drops its 14 modifiers to 5 non-DCC and fixation lands on a01f04 either way.
- **30 fps was OUR cap, not the game.** VRChat ran at 120 fps but the route carried 30. The node's
  negotiated Format framerate was exactly `30/1`, which = our consumer's OWN offered default framerate
  (build_dmabuf_format). libfunnel/Spout2PW paces to the negotiated framerate. **Changing the offered
  default to 60/1 -> the real 4K node immediately delivered ~59 fps** (verified). Fix committed. Target
  behaviour (<=60 from a 120 source, evenly paced by the producer, no back-pressure) achieved via the
  negotiated rate.
- **Vertical flip.** Spout2PW publishes the Spout (DX11, top-left origin) texture as-is; libfunnel does
  NOT touch orientation (its only "flip" is the acquire/release SYNC timeline, funnel.c:271). So the
  real buffer is top-down and our unconditional GL-style vertical flip inverted it (sky at bottom). The
  node carries no SPA_META_VideoTransform and no orientation prop. Fix: default NO flip (top-down, the
  Spout2PW/DX + PipeWire convention); honour SPA_META_VideoTransform when a producer sets it
  (Flipped180 = vertical mirror); `RPW_FLIP_V=1` per-source override for a bottom-up GL producer (our
  libfunnel test). Applied identically to the readback path (shim want_flip) and the VA path (VPP
  mirror_state), so both agree. PTS now taken from SPA_META_Header when present (correct inter-frame
  timing for a variable-rate source), else wall clock.
- Sync mode: the real node negotiates with our implicit-sync consumer fine (no change needed).

### Native HEVC encoder - WORKING, pixel-correct (2026-11-12)

`vaenc.c` now encodes HEVC (Main profile, 4:2:0 8-bit) natively alongside H.264, selected by
`rva_enc_new(codec=1)`. radeonsi requires app-written packed headers, so the shim hand-writes VPS
(nal 32) / SPS (33) / PPS (34) / slice (19 IDR, 1 TRAIL) with `write_*_hevc`; single slice, one
short-term ref (delta_poc=-1, the previous frame), no B, no SAO, no scaling lists, 8-bit POC lsb.
VA config = VAProfileHEVCMain + VAEntrypointEncSlice + VBR + packed headers; the VPP/surfaces/pipeline
and the constrained-VBR rate control are shared with H.264 (`append_rc_misc`). `build_hevc_bufs` fills
the HEVC seq/pic/slice param buffers.

Verified on sus (RX 7900 XTX), decode + corner check (ffmpeg):
- **1080p60: ~60.3 fps**, decoded `codec=hevc 1920x1080`, corners pixel-correct, ~1.3 KB/frame on the
  synthetic corner pattern (much tighter than H.264's ~9 KB - HEVC + VBR).
- **4K60: ~60.3 fps**, decoded `codec=hevc 3840x2160`, corners pixel-correct.
- **REAL VRChat 4K node: ~60.3 fps**, single-plane import (planes=1, mod 0x...a01f04), decoded OK on
  real content. HEVC + the 60fps offer + the DCC filter all work together on the live node, zero-copy.

Advertised as `hevc_vaapi_native` (feat_media.go) when VA + PipeWire capture are available;
`h264_vaapi_native` too. Negotiation (codec.go) preempts to the native engine for a decodable codec,
HEVC-native BEFORE H.264-native, both BEFORE the ffmpeg *_vaapi tiers - so a dmabuf 4K route picks the
zero-copy native path, never the stalling ffmpeg readback. Fallback: a non-dmabuf / non-importable
source still falls back to ffmpeg *_vaapi (readback), same wire codec.

RATE-CONTROL note: on the flat lavfi source the ffmpeg VBR change (args.go `-rc_mode VBR`, verified)
emits ~40 kbps vs a 20 Mbps budget (no CBR padding). The native VBR undershoots flat content too, but
on REAL 4K VRChat content it ran ~51 Mbps against a 25 Mbps budget (peak = 1.5x = 37.5): radeonsi VBR
with min_qp=10 does not hard-cap the peak on complex content. Tuning follow-up (lower peak ratio or a
QP ceiling, or rely on the receiver bitrate governor); not a correctness issue, and a LAN absorbs it.

### Codec/engine ranking for a dmabuf source + interim guidance

The Negotiate preemption (native VAAPI > ffmpeg tiers) only fires when `preferCodec` is unset; a sender
`mediaLink.preferCodec = "hevc"` would make the Prefer block pick ffmpeg `hevc_vaapi` (readback, stalls
at 4K). Now that `hevc_vaapi_native` is advertised + listed FIRST in the HEVC tier, the Prefer block
picks the native engine for a hevc preference too. INTERIM (before this is deployed): keep sus
`mediaLink.preferCodec` empty or `h264` so the preemption selects `h264_vaapi_native` for 4K.

### Bitrate budget ENFORCED as a hard ceiling (2026-11-12, supersedes the RATE-CONTROL note above)

The congestion-safe ceiling is now held. Key findings on radeonsi (RX 7900 XTX):
- radeonsi VBR overshoots the budget ~1.2-1.8x and does NOT honour min_qp as a ceiling lever.
- The HEVC RC was additionally stuck: `cu_qp_delta_enabled_flag` was 0, locking the whole frame at the
  init QP so NO rate control could bite (H.264 varies per-MB QP by default). Enabling cu_qp_delta in the
  HEVC PPS + pic param is REQUIRED for any HEVC RC.
- radeonsi still overshoots even in CBR, so the real lever is a CLOSED-LOOP corrector in the shim:
  `append_rc_misc` uses CBR (target == effective budget) + disable_bit_stuffing (so static undershoots,
  no padding) + a ~0.5s HRD/VBV; `rc_scale_pct` scales the EFFECTIVE budget sent to the driver from a
  1s sliding window of emitted AU bits, so a 1.4x driver overshoot converges to ~72% scale and the
  MEASURED output lands on the real budget. HEVC seeds at 68%, H.264 at 90%. SetBitrate re-seeds it.

Measured (real VRChat 4K node, read-only 2nd consumer, 12 s windows; budget x1.1 is the target):
| budget | H.264 avg / max1s | HEVC avg / max1s |
|  10 M  |   8.98 / 11.17    |   8.49 / 11.24   |
|  25 M  |  22.44 / 27.29    |  20.83 / 26.47   |
|  50 M  |  45.03 / 52.84    |  41.67 / 52.67   |
All avg well UNDER budget; max1s <= 1.12x (only the extreme 4K@10M case touches 1.12, a single motion
spike while avg is 9 Mbps). 1080p high-motion synthetic: all <= budget x1.1. A pathological random-block
synthetic (max-entropy noise, incompressible) sits ~1.2x at 4K - no encoder holds true noise under an
arbitrary budget; real content holds. SetRateHint 50->10 Mbps: output lands at ~8.6 (H.264) / 10.5
(HEVC) Mbps within ~1 s. Static/low-motion (corner producer, 50 M budget): H.264 ~13.8, HEVC ~1.7 Mbps
- undershoots, no padding. HEVC still decodes after all RC changes.

AUD is now codec-correct (H.264 nal 9 only; HEVC carries none, VPS/SPS/PPS are in-band).

### Default budget source + proposed 4K60 defaults

The route budget = Offer.Bitrate = the RECEIVER's `config.mediaLink.bitrateKbps` (flat default 20000,
NOT resolution/codec-aware) -> EncodeSpec.BitrateKbps. So a 4K route and a 1080p route both default to
20 Mbps, which is low for 4K60 H.264. Proposed deliberate defaults (owner config, per codec; HEVC needs
noticeably less for equal quality): 4K60 H.264 ~35 Mbps, HEVC ~22 Mbps; 1080p60 H.264 ~12 Mbps, HEVC
~8 Mbps. (Not changed in code - it is the receiver's knob; the enforcement now makes whatever budget is
set a real ceiling.)

### Live MaxHeight + PLI + sender telemetry (2026-11-12)

- **Live MaxHeight on the native path**: SetRateHint.MaxHeight re-creates the encoder at the capped
  CODED size (rpw_set_cap_height -> on_process recreates via rva_enc_new/free). Verified at 4K: a
  mid-run cap -> 1080 then restore shows per-frame coded heights 2160 -> 1080 -> 2160 at ~60 fps; the
  nominal source geometry is unchanged (the receiver upscales). MaxFPS stays reserved.
- **PLI -> IDR latency = 2 frames (~33 ms)** (force_idr consumed next on_process + 1 pipeline frame).
- **Sender telemetry fixed**: `PipelineStats.OutFPS` (the `fps out=0` bug) now comes from a per-AU
  ratewin, so OutFPS/CapFPS/bytes-per-frame are accurate on a SINGLE `ctl routes` poll (was a read-time
  delta that read 0). Verified: outFps=60.0 capFps=60.0 on one read.
- **Advert fps** (`ctl pw-nodes`): already correct - bestFormat reads the node's negotiated "Format"
  (framerate 60/1, present in `pw-dump -N` when a route is active) before the EnumFormat advert (@0),
  so an actively-captured node shows 60; @0 only appears when idle (variable advert, nothing to measure).

### Still unverified / follow-ups
- ~~Dedicated encode thread~~ DONE (B.2) + cadence/ride-through (shim2 pass below). The fused path already
  measured ~59-60 fps at 4K on the real node; the thread is insurance.
- Full two-PC route through the real media child end-to-end (the lead runs the real rig).

## Linux shim2 pass (feat/linux-shim2, 2026-11-12) - bar-route bug, ride-through, native telemetry, quality

### Bar route ~1 fps after daemon restart - ROOT CAUSE (cc0e9442)
Not the PTS grid. `egl_load`/`va_load` were unguarded one-time loaders that published 0 BEFORE loading.
A daemon restart starts every remembered route at once; the losing capture read "no EGL", built a
**SHM-only EnumFormat**, and the dmabuf-only libfunnel producer failed every link (`pw.link ...
negotiating -> error no more input formats`, journal flood) -> reconnect churn ~40/s (108k on the
live bar route), 0 frames, forever; the receiver saw only ~1 Hz idle traffic (`fps wire=1 out=0`,
~83 B "frames"). Fix: mutex-serialized loaders, EGL setup retried on reconnect when the offer is
SHM-only, reconnect backoff 50 ms -> 2 s while no frame arrives (reset on any frame). Telemetry log
now carries `node` + `dmabufOffer`. Tests: `TestConcurrentStartDMABufOffer` (8 concurrent starts:
old code FAILED 3/3), `TestReconnectBackoff` (10 reconnects / 8 s vs ~300).

### Ride-through (4e3f74dd) - numbers before -> after (4K60 bar / DJ-set source, sus)
| case | before (175b42ec) | after |
|---|---|---|
| 10 fps source, route 60 | 10 AU/s, every interval 100 ms | 60.0 AU/s, max 19.6 ms (repeats) |
| producer hitch 150 ms x5 | max 164 ms, 5 gaps > 50 ms | max 27 ms, 0 > 50 ms (41 repeats) |
| producer restart (1080p) | output FROZE/looped: 39 unique of last 200 decoded frames | 200/200 unique, IDR on resume |
| downstream jitter 0-150 ms (overload) | 112 keyframes/10 s, 246 drops | 3 keyframes, 0 forced IDR |
| downstream stall 150-300 ms @1/min, 10 min (1080p) | - | 59.94 fps, 0 forced IDR, keyframes = GOP only (300), 38 pre-encode skips, max 32 ms |
| route start, consumer 600 ms late | live: ringOverflow 38/route (IDR storm) | first AU IDR, 0 drops, 0 burst, holds 34 |
| two concurrent 4K encodes + VRChat | - | both 60.00 fps, max 17.6 ms |
- Cadence: encode thread flushes the pipelined frame when the next is late (1.5p) and re-encodes the
  last VPP output (P-skip) on an even grid while STREAMING (`rva_enc_flush`/`rva_enc_repeat`). AU PTS
  = its own frame (was the next frame's); arrival-stamped monotonic when no header PTS (libfunnel has
  none; was 0 on native TX).
- Import cache keyed by fd + dmabuf inode (fd numbers are reused by the restarted producer's pool).
- AU ring 3 -> 12 (200 ms), room checked PRE-encode (skip = no ref break, no IDR); start-up hold
  until the first AU (IDR) is read. `RPW_ENC_SYNC=1` = no 1-frame pipeline (A/B knob).
- GPU contention: callback only hands off (never blocks); capture hand-off drops now count in
  `PipeStats.Dropped` (visible degradation). 4 concurrent 4K encodes on the live VRChat node: 38-60 fps
  per consumer, the owner's route held out=58.

### Native-path telemetry (aa8a0005)
Lock-free per-AU interval histograms in the shim (enc = encoder out, tx = queued to the native
sender; StageHist buckets), percentiles computed natively at Go's 1 Hz sample; out/cap fps, queue,
drops, keyframes fed from C counters. medialink: `NativeTXStats.Keyframes/Enc/Tx`, `setNativeStats`
folds them. Rig: outFps 59.9, enc n=357 p50 17 p99 17 max 19.1 ms (were 0/empty).

### Constant-quality validation (891ce1d9) - DJ-set recording, 300-frame cells
Controller fix first: the 1-s peak broke ceiling x1.1 (1080p H.264 @10M q70: 13.8 Mbps = 1.38x).
Now proportional raise (`ceil(6*log2(rate/0.95c))`, <= +8/window), IDR bits amortized over the GOP,
release half the headroom after ~1 s. After (avg / max1s Mbps, avgQP, RL%):
| | 10 Mbps | 25 Mbps | 50 Mbps |
|---|---|---|---|
| 1080p H.264 q50/70/85 | 6.4/7.9 QP30.5 90% ; 7.2/8.5 QP32 100% ; 7.9/9.9 QP26 80% | 8.8/11.8 ; 10.8/14.8 ; 14.3/16.5 (no RL) | 7.6/10.6 ; 10.0/14.1 ; 12.8/16.5 |
| 1080p HEVC q50/70/85 | 5.3/8.3 ; 7.2/9.6 QP29 100% ; 6.9/9.3 QP25 60% | 4.7/7.0 ; 6.4/9.0 ; 9.2/10.7 | 3.9/5.3 ; 6.0/9.1 ; 8.2/10.4 |
| 4K H.264 q50/70/85 | 6.5/10.1 QP43 ; 7.3/10.8 QP45 ; 8.1/9.7 QP37 (all RL) | 17.3/22.6 ; 16.2/20.3 ; 17.4/19.7 (RL) | 21.0/29.6 ; 27.0/38.5 ; 31.9/37.6 |
| 4K HEVC q50/70/85 | 6.5/7.9 ; 7.1/8.5 ; 6.3/9.5 (RL) | 11.2/18.4 ; 11.2/12.7 ; 18.6/21.8 | 11.7/13.8 ; 12.1/23.6 ; 17.2/20.4 |
All max1s <= 1.08x ceiling. fps 60 except some 4K HEVC cells at 51-54 (DJ producer's 4K stdin upload
under VRChat load; not reproduced on the bar source). Cells run sequentially on advancing content.

PSNR/SSIM vs an HEVC-CQP10 reference recorded from the SAME buffers (`TestQualityPair`, frames paired
by capture PTS), same content segment per q, ceiling 50 Mbps:
| | q50 | q70 | q85 |
|---|---|---|---|
| 1080p H.264 | 42.4 dB / 0.967 @11.5 | 45.1 / 0.981 @15.3 | 46.7 / 0.986 @20.6 |
| 1080p HEVC | 42.3 / 0.967 @4.6 | 44.5 / 0.978 @8.3 | 46.2 / 0.984 @13.0 |
| 4K H.264 | 44.0 / 0.978 @31.3 | 44.5 / 0.980 @33.1 (RL) | 44.8 / 0.981 @40.8 (RL) |
| 4K HEVC | 44.0 / 0.977 @18.1 | 45.6 / 0.982 @27.5 | 45.7 / 0.982 @31.5 (RL) |
| 4K HEVC, live VRChat node | 41.7 / 0.971 @9.9 | 44.2 / 0.981 @21.5 | 45.9 / 0.985 @31.1 |
**Default 70 stays (deliberate):** q50->q70 buys +1.5..2.5 dB for ~1.6-1.8x bits; q70->q85 buys
+0.1..1.7 dB for +25-60% bits and at 4K binds a 50 Mbps ceiling (RL) for ~0.1 dB. q70 sits at the
knee (~44.5-45.6 dB, SSIM ~0.98). The QP map spans only 7 QP over q50..85 (0.2 QP/step) - fine for
a default, too flat for a user-facing slider (follow-up if exposed).

### PTS-grid on a jittery ~60 fps source + reference producers (unit 5)
- The B.3 PTS grid gated EVERY source; libfunnel carries no header PTS, so the arrival clock jitter
  made the grid drop every other frame of a 4K60 `-stdin` producer (capFps 30, gridDrops 331/11 s;
  the cadence fill masked it as out 60). Now the grid decimates only when the measured source interval
  (EMA) is < 0.75 period (a real 120->60 source); a ~rate source passes every frame (capFps 60.0,
  gridDrops 0). The live VRChat node negotiates 60 itself (gridDrops 0 either way).
- `funnel-producer -stdin`: the 33 MB/frame 4K texture upload moved after the present (into the
  sleep slack): present max 23 ms -> 17-18 ms. `/tmp/pw4ktest/funnel-producer` on sus = this build
  (sha256 bfc19c2d...; libfunnel*.so unchanged). The DJ file must be fed with `ffmpeg -re` (without
  it ffmpeg decodes at ~5x and the read-ahead mailbox plays the set fast-forward).
- Start (as root on sus; stop = `pkill -f "[f]unnel-producer 3840 2160 60 <name>"`, ffmpeg exits on
  EPIPE):
  - DJ set 4K60: `runuser -u dymattic -- env XDG_RUNTIME_DIR=/run/user/1000 LD_LIBRARY_PATH=/tmp/pw4ktest
    setsid sh -c 'cd /tmp/pw4ktest && ffmpeg -nostdin -v error -re -hwaccel vaapi -hwaccel_output_format
    vaapi -i "/mnt/lilbiatch/E/media/recordings/2026-11-10 22-00-27.mp4" -vf hwdownload,format=bgra -an
    -f rawvideo -pix_fmt bgra - 2>ff_djset.log | ./funnel-producer 3840 2160 60 djset -stdin -either
    >prod_djset.log 2>&1' </dev/null >/dev/null 2>&1 &`
  - bar 4K60: `runuser -u dymattic -- env XDG_RUNTIME_DIR=/run/user/1000 LD_LIBRARY_PATH=/tmp/pw4ktest
    setsid sh -c 'cd /tmp/pw4ktest && ./funnel-producer 3840 2160 60 bar4k -bar -either >prod_bar4k.log
    2>&1' </dev/null >/dev/null 2>&1 &`
  Verified with the same pipelines as children (w/ a histogram consumer, 10 s): DJ present max
  17.0-18.2 ms, consumer 60.01 fps max 18.8 ms; bar present max 16.7-17.8 ms, consumer 60.00 fps.

## Encode under game load + source-freeze detector (d302da94, 2026-11-13)
Live finding: owner route (4K HEVC CBR 50M, VRChat + WoW, GFX busy 81 %) capDrops rising ~1/s in
episodes, each a repeated frame at the receiver. Measured (read-only second consumers + bar producer):
- Per-step (ms p50/p99/max, live node, 4K HEVC): import 0.25/0.25/0.07, vpp submit 0.25/0.25/6.6,
  submit 0.25/0.25/0.8, sync 0.25/2-3/6-39, copy 0.25/0.25/0.5, whole frame 0.25/2-10/16-50. VPP GPU
  latency alone (RPW_VPP_SYNC) 2-3/8-10/10 ms - it runs on the shader queue the games saturate.
  VCN encode is not the bottleneck (sync p50 0.25 for HEVC and H.264; HEVC vs H.264 no measurable
  headroom difference; quality-level preset no measurable change). Mean cost ~0.3 ms CPU, ~3 ms GPU per
  frame: drops are TRANSIENT spikes, not throughput -> capture hand-off FIFO (3 deep) instead of the
  single newest-wins slot. The drop episode the lead saw at 14:33-14:36 coincided with this agent's
  extra 4K test encodes (2-4 concurrent) - GPU contention, as measured.
- CPU is not starving the encode thread: schedstat avg run-queue wait 1.8 us/slice (encode thread,
  nice -5), cgroup nr_throttled 0; CPUWeight=50 only bites under full CPU saturation (16 cores, games
  ~3). Recommendation: keep the unit as is.
- VA caps (radeonsi, mesa 26.2): encoder RTFormat has no RGB32 (VPP colour convert stays), quality range
  32 (bitfield presets; RPW_ENC_QL knob), no context priority. VPP filter: HQ kept at 1:1 (measured not
  slower than FAST), FAST for the overload downscale (1440p: 0.40 vs 2.22 ms GPU mean).
- Telemetry inconsistency: capDrops are real lost SOURCE frames, but the encode thread then runs
  back-to-back, so wall-clock emission intervals stay ~17 ms (enc stage shows no 33 ms gap); only the
  PTS shows the skip -> new `ptsSkipped`. The 392 ms enc max at start = the Go pump's first TCP write
  blocking while the receiver opens; the start-up hold now lasts until the consumer returns for the 2nd
  AU (live start had ringHiwater 11 + ringSkips 19 = a stale burst).
- Auto coded-size controller (default on, env-tunable): see commit; reason in `ctl routes` degraded:.
  The receiver sees the smaller coded size; the REASON on the receiver needs a sender->receiver meta
  (medialink wire) - follow-up.
- Source-freeze detector: telemetry `sourceFreezes`, `ctl routes` "source:" line, log "source picture
  froze" (>= 250 ms, 1 / 10 s). delivered=true = VRChat's own picture froze; false = source stopped
  delivering and cadence repeats filled.
