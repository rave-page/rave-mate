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
	svg := mpWaveSVG(&st, 50, nil, nil)
	if !strings.Contains(svg, `id="mp-library-ph"`) {
		t.Fatalf("playing playhead missing interpolation id; svg=%q", svg)
	}
	if !strings.Contains(svg, `x1="500.0"`) {
		t.Fatalf("playhead not at expected viewBox x=500 (client interpolates in this space); svg=%q", svg)
	}

	// not playing (mpNone) → no playhead line, so nothing for the client to (fail to) animate.
	if idle := mpWaveSVG(&st, mpNone, nil, nil); strings.Contains(idle, `id="mp-library-ph"`) {
		t.Fatalf("idle player must not render the playhead id; svg=%q", idle)
	}

	// cue-editor overlay active (hold-Space audition): the mint playhead is the moving element
	// (the white beat cursor stays put), so it must still carry the interpolation id.
	ce := &ceOverlay{cursorMs: mpNone, dragA: -1, dragB: -1, sel: map[int]bool{}, dsel: map[int]bool{}}
	if svg := mpWaveSVG(&st, 50, ce, nil); !strings.Contains(svg, `id="mp-library-ph"`) {
		t.Fatalf("cue-edit audition playhead missing interpolation id; svg=%q", svg)
	}
}

// The unplayed-side veil must carry id=mp-<host>-ph-veil (the rAF runtime moves it with
// the interpolated playhead - shell.go __rt), and the playhead must be a device-pixel
// hairline (vector-effect) - the 1000-unit viewBox used to fatten it on wide windows.
func TestPlayheadVeilAndHairline(t *testing.T) {
	st := mpSt{
		host:      "library",
		media:     []mpMedia{{path: "x.wav", kind: "audio", dur: 100}},
		viewStart: 0, viewSpan: 1,
		cursorSec: mpNone, hovT: mpNone, outSec: -1,
	}
	svg := mpWaveSVG(&st, 50, nil, nil)
	if !strings.Contains(svg, `id="mp-library-ph-veil"`) {
		t.Fatalf("playing wave missing the unplayed-side veil; svg=%q", svg)
	}
	if !strings.Contains(svg, `vector-effect="non-scaling-stroke"`) {
		t.Fatalf("playhead lost its non-scaling hairline stroke; svg=%q", svg)
	}
	if idle := mpWaveSVG(&st, mpNone, nil, nil); strings.Contains(idle, "ph-veil") {
		t.Fatalf("idle player must not render the veil; svg=%q", idle)
	}
}

// The Link phrase bar must carry the fill + caption ids the client rAF runtime (__rt 'link')
// targets, at the given fill width, so pushAbleLink can advance them at display refresh.
func TestLinkPhraseBarInterpolationIDs(t *testing.T) {
	bar := linkPhraseBar(37.5, "Beat 7 / 16")
	for _, want := range []string{`id=live-link-fill`, `id=live-link-cap`, `width:37.50%`, `Beat 7 / 16`} {
		if !strings.Contains(bar, want) {
			t.Fatalf("phrase bar missing %q; bar=%q", want, bar)
		}
	}
	// clamp out-of-range fill (a stale phase must never blow the bar past 100%).
	if over := linkPhraseBar(150, "x"); !strings.Contains(over, `width:100.00%`) {
		t.Fatalf("fill not clamped to 100%%; bar=%q", over)
	}
}
