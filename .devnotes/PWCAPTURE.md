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

### Still unverified
- The **real VRChat/Spout2PW node** (VRChat was not running): all above is vs the libfunnel synthetic
  producer. Real-node unknowns: its exact node.name, whether it needs `AMD_DEBUG=nodcc` to avoid the
  3-plane DCC_RETILE layout at 4K, and its sync mode.
- Production engine wiring ("vaapi-native" in medialink/mediapipe/featurehost) and HEVC remain
  follow-ups.
