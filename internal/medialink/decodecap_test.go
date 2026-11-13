package medialink

import "testing"

func TestApplyDecodeCap(t *testing.T) {
	defer func() { DecodeCapHeight = nil }()
	h := RateHint{MaxHeight: 1080}
	applyDecodeCap(&h) // nil seam: unchanged
	if h.MaxHeight != 1080 {
		t.Fatalf("nil seam changed the hint: %d", h.MaxHeight)
	}
	DecodeCapHeight = func() int { return 720 }
	applyDecodeCap(&h)
	if h.MaxHeight != 720 {
		t.Fatalf("CPU-mode cap must win when tighter: %d", h.MaxHeight)
	}
	h = RateHint{} // no VRAM cap: decoder cap applies
	applyDecodeCap(&h)
	if h.MaxHeight != 720 {
		t.Fatalf("decoder cap on an uncapped hint: %d", h.MaxHeight)
	}
	DecodeCapHeight = func() int { return 0 } // promotion lifts it
	h = RateHint{}
	applyDecodeCap(&h)
	if h.MaxHeight != 0 {
		t.Fatalf("lifted cap must not cap: %d", h.MaxHeight)
	}
}
