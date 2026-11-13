package medialink

import "testing"

type capSink struct {
	Sink
	h int
}

func (c capSink) CodedCapHeight() int { return c.h }

// The cap is PER ROUTE: one route's CPU-mode cap never touches another route's hint.
func TestApplyDecodeCapPerRoute(t *testing.T) {
	cpuRoute, hwRoute := capSink{h: 720}, capSink{h: 0}
	a, b := RateHint{MaxHeight: 1080}, RateHint{}
	applyDecodeCap(&a, decodeCapOf(cpuRoute))
	applyDecodeCap(&b, decodeCapOf(hwRoute))
	if a.MaxHeight != 720 || b.MaxHeight != 0 {
		t.Fatalf("per-route caps: cpu route %d (want 720), hw route %d (want 0)", a.MaxHeight, b.MaxHeight)
	}
	c := RateHint{MaxHeight: 540}
	applyDecodeCap(&c, decodeCapOf(cpuRoute)) // tighter VRAM cap wins
	if c.MaxHeight != 540 {
		t.Fatalf("tighter cap must win: %d", c.MaxHeight)
	}
	var plain Sink
	if decodeCapOf(plain) != nil {
		t.Fatal("a sink without a cap has no cap source")
	}
}
