package mfenc

import "errors"

// encgov: the VRAM-admission seam on native encode-session open (sibling of decgov's decode gate).
//
// An encode session allocates a VP NV12 input pool + encoder MFT on the GPU. On a saturated card the
// open fails, and substituting the ffmpeg encoder is NOT lighter (h264_nvenc/amf use the same GPU;
// libx264 pegs the CPU and starves the DAW). The right move under VRAM pressure is to HOLD the route
// and retry when headroom returns, exactly like the decode side.

// EncAdmit, when non-nil, is the ONE VRAM admission policy for a native encode-session open: app
// wires it to the shared gpubudget.Budget. nil => no gating (behaviour unchanged). allow=false =>
// ErrEncodeHeldVRAM, and the mediapipe encode factory holds instead of substituting ffmpeg. Geometry
// is the OUTPUT (encoded) resolution so the reserve is geometry-aware.
var EncAdmit func(outW, outH, fps int, codec string) (allow bool, reason string)

// ErrEncodeHeldVRAM is the VRAM-pressure refusal on encode open: free headroom cannot afford a new
// encode session. The mediapipe factory detects it and HOLDS the route (retries on renegotiation)
// rather than demoting to the ffmpeg encoder (no lighter on one GPU).
var ErrEncodeHeldVRAM = errors.New("mfenc: native encode held - VRAM pressure")
