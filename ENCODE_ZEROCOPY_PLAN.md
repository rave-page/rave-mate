# Zero-copy GPU encode plan (medialink)

Goal: encode the peer-link video plane straight from the **Spout D3D11 texture on the GPU** into
a HW encoder, with **no GPU→CPU→GPU roundtrip**. The efficiency lever for 4K - not "pure Go"
(no pure-Go HW encoder exists; HW encode = vendor C APIs → cgo either way).

## Why (the actual waste)

Today: source pulls a Spout frame (D3D11 texture, GPU) → reads it back to CPU → pipes raw to a
shelled `ffmpeg` → ffmpeg re-uploads to the encoder (GPU). Two full-frame PCIe copies + a process
pipe per frame. At 4K60 (~1.5 GB/s uncompressed) that readback+reupload dominates; the encode
itself is near-free. NDI stays GPU-free by paying LAN bandwidth (CPU SpeedHQ); we can beat it by
keeping frames on-GPU and shipping HW-HEVC.

## Options

| Path | Zero-copy | Vendors | Build cost | Notes |
|---|---|---|---|---|
| **shell ffmpeg CLI** (today) | no | nvenc/amf/qsv/x264 | none | readback+pipe; proven; keep as fallback |
| **go-astiav** (libavcodec cgo) | yes (D3D11 hwframe) | all libav HW encoders | cgo + ship avcodec DLLs | RECOMMENDED - same encoders, `av_hwframe`, adapter select |
| vendor SDK cgo (NVENC/AMF direct) | yes (max control) | per-vendor bindings we maintain | heavy | last resort; only if libav path insufficient |
| Media Foundation (COM) | yes | Win HW MFTs | COM/cgo, win-only | not more efficient than libav; win-locked |

## Recommendation: go-astiav with a D3D11 hwframe context, CLI ffmpeg as fallback

- Import a Spout texture as a D3D11 `ID3D11Texture2D`, wrap in an `AVFrame` backed by an
  `AV_HWDEVICE_TYPE_D3D11VA` device + `AV_PIX_FMT_D3D11` hwframes pool → feed `hevc_nvenc`/
  `hevc_amf` directly. Encoder and Spout share the same D3D11 device → no copy.
- Keep the shelled-ffmpeg encoder (`internal/mediapipe`) as the fallback backend when the cgo
  device/hwframe setup fails or on non-Windows.
- **Device selection is mandatory here** and fixes the multi-GPU bug we just found (§ below):
  create the D3D11 device on the *correct adapter* (the one whose HW encoder we chose), so
  `hevc_amf` runs on the AMD adapter and `hevc_nvenc` on the NVIDIA one - never adapter-0-default.

## The device-selection dependency (blocks BOTH backends)

Actual hardware (2026-07-04):
- **SUS (VR PC, encoder side):** discrete **AMD GPU + Ryzen iGPU** = TWO AMD adapters, no NVIDIA/
  Intel. Working encoder = `amf` (OBS uses it). `nvenc`/`qsv` correctly absent.
- **Stream PC:** **NVIDIA 3060 only**, no iGPU. Working = `nvenc`. `amf`/`qsv` correctly absent
  (`amfrt64.dll failed to open` = no AMD driver - expected).

Bug: `h264_amf`/`hevc_amf` fail rave-mate's shelled test-encode on SUS even though OBS's AMD
encoder works. Not an NVIDIA-vs-AMD adapter-0 issue (no NVIDIA on SUS). Candidates: (a) ffmpeg
can't find `amfrt64.dll` (DLL search path) though OBS can; (b) ffmpeg defaults AMF to the wrong
AMD adapter (iGPU VCN vs the discrete card). The stderr-capture probe (bd7e8eb) will disambiguate.
Fix, needed by CLI *and* cgo paths:

1. Enumerate adapters (DXGI `EnumAdapters` LUID + vendor) - reuse encoderscan's PDH LUID keys.
2. Bind each probed encoder family to the adapter that has it; on a multi-AMD host, prefer the
   discrete AMD over the iGPU (or try both).
3. CLI: thread `-init_hw_device d3d11va:,adapter=N` + `-filter_hw_device` / `-gpu N` into
   `internal/mediapipe/args.go` (which has NO device flags today).
4. cgo: create the `AVHWDeviceContext` on that adapter's LUID.
5. Probe test-encode: try each in-build HW encoder on each matching adapter (not just adapter 0),
   so the scan stops false-negativing amf on multi-AMD hosts. If (a), also ensure ffmpeg's spawn
   env/working dir lets it resolve the AMF runtime DLL.

## Supply chain / build

- cgo already in use (Spout, OpenVR). go-astiav needs the FFmpeg *shared libs* (avcodec/avutil/…
  DLLs) shipped beside the exe (like SpoutLibrary.dll/openvr_api.dll - runtime-loaded, manifest
  assets[] for the self-updater). 7-day-soak pin per SUPPLY_CHAIN.md; justify the dep row.
- Feature-flag behind a build tag (e.g. `zerocopy`) + config `MediaLink.ZeroCopy`; default to the
  CLI backend until proven, mirroring the vr/spout tag pattern. CI must build the tag (see
  [[ci-build-all-feature-tags]]).

## Phasing

- **P0 (done/underway):** self-diagnosing probe - name encoders, capture per-encoder failure
  reason (bd7e8eb). Reveals the real blockers (DLL path, adapter) before we build.
- **P1:** adapter enumeration + per-family adapter binding; thread device flags into
  `mediapipe/args.go` (CLI). Fixes multi-GPU HW encode with zero new deps. Re-probe per adapter.
- **P2:** go-astiav backend behind `zerocopy` tag: D3D11 hwframe import of the Spout texture →
  HW encode, same-device. Fallback to CLI on any setup error.
- **P3:** wire the app-agnostic headroom planner to pick family+adapter (reorder source encoder
  list per PlanEncode) so the stream lands on a non-contended encoder - the coexistence goal.

Related: [[medialink-beat-ndi-encoder-affinity]], MEDIALINK_DESIGN.md §3.2, `internal/mediapipe`,
`internal/encoderscan`.
