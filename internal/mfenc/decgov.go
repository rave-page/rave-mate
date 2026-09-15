package mfenc

import "rave.page/mate/internal/gpumem"

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
