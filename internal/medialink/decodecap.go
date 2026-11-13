package medialink

// DecodeCapHeight, when non-nil, returns the coded-height cap the RECEIVE side's decoder asks for while
// it runs in CPU decode mode (adaptive VRAM fallback, .devnotes/VRAM_FULL_CARD_POLICY.md 1e); 0 = none.
// The receive governors min() it into their RateHint, so the sender scales down before encode and the
// destination stays nominal (coded size only). Wired by featurehost to mfenc.DecodeCapHeight.
var DecodeCapHeight func() int

// applyDecodeCap folds the decoder's CPU-mode cap into h (more restrictive wins; 0 = no cap).
func applyDecodeCap(h *RateHint) {
	if DecodeCapHeight == nil {
		return
	}
	if c := DecodeCapHeight(); c > 0 {
		h.MaxHeight = minCap(h.MaxHeight, c)
	}
}
