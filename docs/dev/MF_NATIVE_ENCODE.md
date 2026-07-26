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

`Factories`: H.264 encode specs try `newMFBridge` first when `mfenc.Available()`;
any failure falls back to the ffmpeg child unchanged (one warn). `spec.MaxHeight`
rides the same Blt (4K→1080p costs nothing extra); `MaxHeight 0` = native res - a 4K
source encodes at full 4K on the silicon. PTS is preserved by MF (no ptsq remap);
TC carried FIFO (no B-frames = output order == input order).

Verified on hardware (NVIDIA H.264 Encoder MFT): 720p60 60/60 AUs with SPS+IDR
first AU + mid-stream forced IDR; 4K→1080p and native-4K bridge runs, keyframe-first,
clean drain. `go test ./internal/mfenc/ ./internal/mediapipe/ -run 'TestEncode|TestMFBridge'`.

## Fallback rules

| Condition | Engine |
|---|---|
| H.264 + hardware MFT + D3D11 device | mfenc (native) |
| mfenc open/feed failure | ffmpeg child (auto, warned once) |
| HEVC / AV1 / MJPEG | ffmpeg child |
| non-Windows / no cgo | ffmpeg child (stub Available()=false) |

## Phase B (next): zero-copy source

Spout senders ARE DX11 shared textures; `GetSenderInfo` (already in the videoshare
shim) returns the share handle. Plan: open the handle on mfenc's device
(`OpenSharedResource`), `CopyResource` into the Blt input - the CPU readback +
upload disappear entirely; frames never leave the GPU until they're compressed bits.
Pacing via the Spout frame-count semaphore. Caps note: a no-ffmpeg machine still
can't ADVERTISE h264 (probe is ffmpeg-based, internal/mediapipe/probe.go) - wiring
`mfenc.Available()` into the caps list is a 3-line follow-up in probe/app wiring.

Phase B hooks that already exist (2026-07-25 capture pass): the readback is now rate-gated
inside the poll loop (`videoshare.RecvOptions.MaxFPS`, live via `FPSLimiter`) and there is ONE
capture per Spout sender fanned out to N routes (`mediaroute/capture.go`). A zero-copy source
replaces the readback INSIDE that single capture - keep the same seam (one shared capture per
sender name, refcounted, per-route fps applied downstream) so N routes still cost one GPU path.
Zero-copy hard-depends on device selection: `OpenSharedResource` fails across adapters.
