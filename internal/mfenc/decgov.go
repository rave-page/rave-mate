package mfenc

import (
	"strings"

	"rave.page/mate/internal/gpumem"
)

// decgov: the VRAM-headroom gate on decode recycling (DECODE_DEST_CONTENDED_HOLD follow-up).
//
// A decode close()+reopen must re-allocate a D3D11 device, a video processor and a 4K DXVA surface
// pool. On a saturated card it frees a healthy GPU-resident pipeline and then FAILS to rebuild,
// cascading to the ffmpeg CPU path - which creates a NEW Spout shared texture, forcing Resolume to
// re-register GL/DX interop and hit E_OUTOFVIDEOMEMORY (the mid-set crash). Under low free VRAM the
// only safe move is to HOLD the resident pipeline: the picture freezes on its last frame (which the
// owner accepts) and publish resumes when headroom returns.

// DecHeadroom reports primary-adapter VRAM headroom for the recycle gate. Package seam (same shape
// as mediapipe.ZeroCopyDecode): the daemon / media child point it at a live gpumem sampler, gated
// by the governor enable flag. Default fails OPEN (Present=false) so a rig without wiring behaves
// exactly as before.
var DecHeadroom = func() gpumem.Headroom { return gpumem.Headroom{} }

// DecRebuildFloorMB is the free-VRAM floor below which a decode reopen is presumed to fail. Below
// it, recycleDest holds instead of reopening. Wired from config.ResolvedVramReserveMB when set,
// else this conservative default (a 4K decode rebuild + margin).
var DecRebuildFloorMB uint64 = 768

// recycleAllowed reports whether a decode close()+reopen may run: yes when headroom is unknown
// (fail open) or free VRAM is at/above the rebuild floor. Pure, so the gate is testable off-set.
func recycleAllowed(h gpumem.Headroom, floorMB uint64) bool {
	return !h.Present || h.FreeMB >= floorMB
}

// DecVerdict is the unified VRAM-admission result for a decode pipeline (re)open, kept LOCAL so
// mfenc does not import gpubudget (app translates a gpubudget.Decision into this). Allow=false means
// HOLD the resident pipeline / refuse a new open - never fall to the heavier ffmpeg CPU path.
type DecVerdict struct {
	Allow     bool
	Present   bool
	FreeMB    uint64
	CostMB    float64
	ReserveMB uint64
	Reason    string
}

// DecAdmit, when non-nil, is the ONE VRAM admission policy for a decode pipeline open/rebuild: app
// wires it to the shared gpubudget.Budget. nil => the static-floor fallback
// recycleAllowed(DecHeadroom(), DecRebuildFloorMB). Geometry is the session's in/out dims so the
// reserve is geometry-aware (a 4K rebuild needs more than a 1080p one).
var DecAdmit func(inW, inH, outW, outH, fps int, hevc bool) DecVerdict

// vramPressureSignature recognizes a reopen/open failure caused by a FULL card (interop OOM, surface
// allocation timeout) vs a genuine codec/driver capability failure. A VRAM-pressure failure must
// HOLD (never pin to the heavier ffmpeg CPU decode, which re-registers Spout interop and crashes
// Resolume on a full card); only a genuine capability failure may pin after decMaxRecycles.
func vramPressureSignature(msg string) bool {
	m := strings.ToLower(msg)
	for _, s := range []string{
		"open timeout", "e_outofvideomemory", "0x8876017c", "out of memory", "outofmemory",
		"opensharedresource", "createtexture2d", "e_outofmemory", "0x8007000e", "insufficient",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// reopenFailedForVRAM classifies a FAILED reopen: a VRAM-pressure failure (error signature OR
// headroom now below the rebuild floor) must not count toward the pin budget. Fails SAFE: with no
// headroom reading it falls back to the error signature alone.
func reopenFailedForVRAM(msg string, hAfter gpumem.Headroom, floorMB uint64) bool {
	if vramPressureSignature(msg) {
		return true
	}
	return hAfter.Present && hAfter.FreeMB < floorMB
}
