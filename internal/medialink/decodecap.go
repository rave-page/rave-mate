package medialink

// CodedCapper is implemented by a receive route's decode sink that may ask its sender for a lower CODED
// height (CPU decode mode, .devnotes/VRAM_FULL_CARD_POLICY.md 1e). Per route: the receive governors
// min() THIS route's cap into THIS route's RateHint; the destination stays nominal.
type CodedCapper interface {
	CodedCapHeight() int // 0 = no cap
}

// KeyframeRequestable is implemented by a decode sink that can switch decoders mid-route (adaptive
// HW/CPU mode): the route hands it the route's keyframe request (PLI) so a fresh decoder never waits up
// to a GOP for the sender's next natural IDR.
type KeyframeRequestable interface {
	SetKeyframeRequester(func())
}

// decodeCapOf returns the route sink's coded-height cap source (nil when the sink has none).
func decodeCapOf(s Sink) func() int {
	if c, ok := s.(CodedCapper); ok {
		return c.CodedCapHeight
	}
	return nil
}

// applyDecodeCap folds the route decoder's CPU-mode cap into h (more restrictive wins; 0 = no cap).
func applyDecodeCap(h *RateHint, capFn func() int) {
	if capFn == nil {
		return
	}
	if c := capFn(); c > 0 {
		h.MaxHeight = minCap(h.MaxHeight, c)
	}
}
