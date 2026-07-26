package videoshare

// share.go - the GPU shared-texture identity of a sender (zigmedia inc 1). A sender's pixels
// already live in a DX11 shared texture; an encoder on the same adapter can open it directly, so
// the whole GPU→CPU readback + host frame buffer disappear. Go only ever moves the two SCALARS
// (handle + DXGI format) - it never maps or touches the texture.

// shareFn is the backend seam (test override; per-tag impl in share_spout.go / share_noop.go).
var shareFn = senderShare

// SenderShare resolves a named sender's shared-texture handle + DXGI format + current dims.
// ok=false when there is no backend, the sender is unknown, the registry info is torn, or the
// sender has no DX11 shared texture at all (handle 0 - DX9/CPU-memoryshare senders keep the
// readback path, correctly). Dims are validated with FrameBytes: unvalidated shim geometry must
// never reach a consumer, and the encoder child refuses a mismatch anyway.
func SenderShare(name string) (handle uint64, dxgiFormat uint32, w, h int, ok bool) {
	h64, fmt, ww, hh, got := shareFn(name)
	if !got || h64 == 0 {
		return 0, 0, 0, 0, false
	}
	if _, valid := FrameBytes(ww, hh); !valid {
		return 0, 0, 0, 0, false
	}
	return h64, fmt, ww, hh, true
}
