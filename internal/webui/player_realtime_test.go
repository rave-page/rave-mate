package webui

import (
	"strings"
	"testing"
)

// The waveform playhead line must carry id=mp-<host>-ph so the client rAF runtime
// (shell.go __rt) can interpolate its x between the coarse ~1 Hz Go re-renders. It must
// render in the fixed 1000-unit viewBox at the same x Go computes (toX), so the client's
// units/sec velocity lands the line exactly where the next Go render would.
func TestPlayheadInterpolationID(t *testing.T) {
	st := mpSt{
		host:      "library",
		media:     []mpMedia{{path: "x.wav", kind: "audio", dur: 100}},
		viewStart: 0, viewSpan: 1,
		cursorSec: mpNone, hovT: mpNone, outSec: -1,
	}
	// playing at 50s of a 100s track, full view → playhead at the viewBox midpoint (x=500).
	svg := mpWaveSVG(&st, 50, nil)
	if !strings.Contains(svg, `id="mp-library-ph"`) {
		t.Fatalf("playing playhead missing interpolation id; svg=%q", svg)
	}
	if !strings.Contains(svg, `x1="500.0"`) {
		t.Fatalf("playhead not at expected viewBox x=500 (client interpolates in this space); svg=%q", svg)
	}

	// not playing (mpNone) → no playhead line, so nothing for the client to (fail to) animate.
	if idle := mpWaveSVG(&st, mpNone, nil); strings.Contains(idle, `id="mp-library-ph"`) {
		t.Fatalf("idle player must not render the playhead id; svg=%q", idle)
	}
}
