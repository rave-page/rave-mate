# Native MF hardware encode (internal/mfenc)

The preferred H.264 engine on Windows for medialink video routes. Replaces the ffmpeg
child on the send path: no subprocess, no multi-GB/s rawvideo stdin pipe - frames go to
the GPU once and come back as annex-B AUs.

## Pipeline

```
RGBA []byte ── UpdateSubresource ──> D3D11 texture (R8G8B8A8, or B8G8R8A8+swizzle when
    the VP rejects RGBA input - probed via CheckVideoProcessorFormat)
  ── ID3D11VideoContext::VideoProcessorBlt ──> NV12 texture pool (8, round-robin;
    CSC BT.709 studio + optional scale, all on GPU)
  ── MFCreateDXGISurfaceBuffer sample ──> hardware encoder MFT (MFTEnumEx
    MFT_ENUM_FLAG_HARDWARE; NVENC/AMF/QSV silicon) ──> annex-B H.264 AUs
```

- **Not** the VideoProcessorMFT (XVP): its D3D sample negotiation is vendor-flaky
  (E_INVALIDARG on ProcessOutput with both caller and provider samples on NVIDIA).
  The D3D11 Video API (`VideoProcessorBlt`) is the layer below it - deterministic.
- Encoder MFT runs **async** (IMFMediaEventGenerator: NeedInput credits, ONE
  ProcessOutput per HaveOutput event - more = E_UNEXPECTED); sync MFTs are also
  handled (drain-on-NOTACCEPTING) for vendor variance.
- ICodecAPI: CBR, mean bitrate, ~2 s GOP, no B-frames, low-latency;
  `AVEncVideoForceKeyFrame` = **live forced IDR** (KeyframeSource without the ffmpeg
  child's respawn + stream hole).
- COM/MF affinity: one LockOSThread goroutine per encoder owns every call (MTA).
- GUID hygiene: interface IIDs via `__uuidof` (hand-rolled IIDs caused two real bugs:
  wrong IMFMediaEventGenerator IID silently forced sync mode; wrong
  AVEncVideoForceKeyFrame GUID no-opped forced IDRs). Attribute/format GUIDs are
  TU-local DEFINE_GUIDs (INITGUID) so MinGW import-lib gaps can't break linking.

## Integration (internal/mediapipe/mf_bridge.go)

`Factories` dispatches on the NEGOTIATED ENCODER NAME (`encodeEngine`), not the codec:
only `medialink.EncoderMFNative` (`"h264_mf_native"`) runs this engine. That name is
advertised only when `mfenc.Available()`, and the §3.2 matrix ranks it FIRST in the H.264
tier, so it wins over the pipe-fed tiers whenever the peer decodes H.264 (see
`medialink.Negotiate`). Keying on `Codec == CodecH264` was a bug: a negotiated `libx264`
ran here on hardware while the Answer + route stats reported tier-4 software, and `SWOnly`
stopped forcing software at all.

An open failure falls back to the ffmpeg child, but the SPEC IS REWRITTEN to a probed
ffmpeg H.264 encoder first (hardware preferred, `libx264` last) - the peer was answered
H.264, so the wire codec must stay H.264. One loud warn names the substitution.

`spec.MaxHeight` rides the same Blt (4K→1080p costs nothing extra); `MaxHeight 0` = native
res - a 4K source encodes at full 4K on the silicon. PTS is preserved by MF (no ptsq remap);
TC carried FIFO (no B-frames = output order == input order).

**Device selection (WP-3).** `mfenc.NewOn(adapterLUID, …)` pins the pipeline to one GPU:
`mf_enc_open`'s formerly dead `codec` parameter is now `adapterLuid` (DXGI `AdapterLuid`
packed `HighPart<<32|LowPart`, 0 = default adapter). The shim resolves it via
`CreateDXGIFactory1`/`EnumAdapters1`, creates the device with
`D3D11CreateDevice(pAdapter, D3D_DRIVER_TYPE_UNKNOWN, …)` - mandatory when an adapter is
passed - and then picks the encoder MFT for THAT adapter: candidates whose friendly name
carries the adapter's vendor tag (from its PCI VendorID) are tried first, and
`MFT_MESSAGE_SET_D3D_MANAGER` is the hard gate (an MFT that refuses this adapter's device
manager is skipped instead of failing the pipeline). No more blind `acts[0]`. An adapter that
cannot host the pipeline degrades to the default one INSIDE the shim, so a device preference
can never kill a route. The LUID comes from `config.MediaLink.EncoderDevice` via
`encoderscan.ResolveDevice` → `EncodeSpec.DeviceLUID` → `encoderscan.LUIDInt64`.

This is the ACCURATE device path: ffmpeg has `-gpu` (NVENC) and `-qsv_device` (QSV) but no
device option for AMF at all, and NVENC's ordinal is a CUDA ordinal that need not match the
DXGI one. Here the adapter is identified by LUID.

Verified on hardware (NVIDIA H.264 Encoder MFT): 720p60 60/60 AUs with SPS+IDR
first AU + mid-stream forced IDR; 4K→1080p and native-4K bridge runs, keyframe-first,
clean drain. `go test ./internal/mfenc/ ./internal/mediapipe/ -run 'TestEncode|TestMFBridge'`.

## Fallback rules

| Condition | Engine |
|---|---|
| Negotiated encoder == `medialink.EncoderMFNative` (only advertised when `Available()`) | mfenc (native) |
| mfenc open/feed failure | ffmpeg child on a probed H.264 encoder (spec rewritten, warned) |
| Any other encoder name - incl. ffmpeg's own `h264_mf`, `h264_nvenc`, `libx264` | ffmpeg child |
| HEVC / AV1 / MJPEG | ffmpeg child |
| non-Windows / no cgo | ffmpeg child (stub `Available()=false`, name never advertised) |
| `MediaLink.SWOnly` | native name filtered out of the advertisement → software tier really is software |

## Phase B (next): zero-copy source

Spout senders ARE DX11 shared textures; `GetSenderInfo` (already in the videoshare
shim) returns the share handle. Plan: open the handle on mfenc's device
(`OpenSharedResource`), `CopyResource` into the Blt input - the CPU readback +
upload disappear entirely; frames never leave the GPU until they're compressed bits.
Pacing via the Spout frame-count semaphore.

Two former blockers are now cleared: a no-ffmpeg machine DOES advertise the native engine
(`mediaCaps`'s `mfOnly` set / `encFilter` add `EncoderMFNative`), and **device selection has
landed** - `OpenSharedResource` fails across adapters, so Phase B needs the encoder device to
be the adapter that owns the Spout texture. `EncodeSpec.DeviceLUID` + `mfenc.NewOn` give
Phase B exactly that handle; the remaining work is opening the share handle on it.

Phase B hooks that already exist (2026-07-25 capture pass): the readback is now rate-gated
inside the poll loop (`videoshare.RecvOptions.MaxFPS`, live via `FPSLimiter`) and there is ONE
capture per Spout sender fanned out to N routes (`mediaroute/capture.go`). A zero-copy source
replaces the readback INSIDE that single capture - keep the same seam (one shared capture per
sender name, refcounted, per-route fps applied downstream) so N routes still cost one GPU path.
Zero-copy hard-depends on device selection: `OpenSharedResource` fails across adapters.
