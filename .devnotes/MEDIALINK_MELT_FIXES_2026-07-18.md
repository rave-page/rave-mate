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
- Per-subscriber duplication: N peers on one source = N capture+encode stacks (cap 8).
  Needs capture fan-out sharing.
- `MediaLink.Subprocess` isolation child: still default-off/unverified (config comment).
- `mediaroute.scan` builds/releases a Spout runtime object per `ListSenders`/`SenderSize`
  call every 2 s - cacheable.
