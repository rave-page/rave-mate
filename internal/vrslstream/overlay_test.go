package vrslstream

import (
	"image"
	"testing"
)

func TestOverlayForFrame(t *testing.T) {
	painter := func(*image.RGBA) {}

	// nil provider: no overlay, no suppression.
	if ov, sup := overlayForFrame(nil, true); ov != nil || sup {
		t.Errorf("nil provider = (%v, %v), want (nil, false)", ov != nil, sup)
	}
	// provider returning nil (module off): no overlay, no suppression.
	if ov, sup := overlayForFrame(func() func(*image.RGBA) { return nil }, true); ov != nil || sup {
		t.Errorf("provider-nil = (%v, %v), want (nil, false)", ov != nil, sup)
	}
	// standard mode forces nil + reports the suppression (one-time warn upstream).
	if ov, sup := overlayForFrame(func() func(*image.RGBA) { return painter }, false); ov != nil || !sup {
		t.Errorf("standard mode = (%v, %v), want (nil, true)", ov != nil, sup)
	}
	// extended passes the painter through.
	if ov, sup := overlayForFrame(func() func(*image.RGBA) { return painter }, true); ov == nil || sup {
		t.Errorf("extended = (%v, %v), want (non-nil, false)", ov != nil, sup)
	}
}
