package deckcard

import (
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/session"
)

// TestWavePreview renders sample cards to PNGs for visual review. Opt-in: set RAVEMATE_WAVE_PREVIEW
// to the output dir. Skipped in normal runs.
func TestWavePreview(t *testing.T) {
	outDir := os.Getenv("RAVEMATE_WAVE_PREVIEW")
	if outDir == "" {
		t.Skip("set RAVEMATE_WAVE_PREVIEW=<dir> to render previews")
	}
	f, err := LoadFacesScale(2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Synthetic peaks: 4000 buckets over 240s, kick-ish envelope.
	const n = 4000
	const durSec = 240.0
	peaks := make([]byte, n)
	for i := range peaks {
		ts := float64(i) / n * durSec
		env := 0.45 + 0.5*math.Abs(math.Sin(ts*0.3))
		beat := 0.6 + 0.4*math.Pow(math.Abs(math.Sin(ts*math.Pi*2*2)), 8) // ~2 Hz transients
		peaks[i] = byte(math.Min(255, env*beat*255))
	}
	d := session.DeckSnapshot{
		Deck: "A", Title: "Strobe (Extended Mix)", Artist: "deadmau5", Key: "4A", BPM: 128,
		TrackLength: durSec, ElapsedTime: 64, IsPlaying: true, OnAir: true,
		Fader: 0.92, HasFader: true, HasMixer: true,
		EQLow: 0.7, EQMid: 0.45, EQHigh: 0.85,
	}
	base := WaveOpts{Enabled: true, Peaks: peaks, Duration: durSec, Position: 64, ZoomSeconds: 20, PlayheadFrac: 1.0 / 3.0}

	cases := []struct {
		name   string
		filter float64
	}{
		{"neutral", 0.5},
		{"hp_lowcut", 0.82},  // high-pass: cut from left
		{"lp_highcut", 0.18}, // low-pass: cut from right
	}
	for _, c := range cases {
		dd := d
		dd.Filter = c.filter
		img := RenderScaled(f, dd, nil, base, 2)
		writePNG(t, filepath.Join(outDir, "wave_"+c.name+".png"), img)
	}
	// No-peaks placeholder + legacy (non-wave) for comparison.
	dd := d
	dd.Filter = 0.5
	writePNG(t, filepath.Join(outDir, "wave_generating.png"),
		RenderScaled(f, dd, nil, WaveOpts{Enabled: true, Duration: durSec, Position: 64, ZoomSeconds: 20}, 2))
	writePNG(t, filepath.Join(outDir, "legacy.png"), RenderScaled(f, dd, nil, WaveOpts{}, 2))

	// Green-on-green: waveform tinted mint like the EQ line - the halo must keep the line visible.
	green := base
	green.WaveColor = "#08F79B"
	green.WaveOpacity = 1
	green.BgColor = "#0a0a0e"
	green.BgOpacity = 0.85
	dd2 := d
	dd2.Filter = 0.5
	writePNG(t, filepath.Join(outDir, "wave_green_on_green.png"), RenderScaled(f, dd2, nil, green, 2))

	// Custom tint + background.
	custom := base
	custom.WaveColor = "#FFB547" // amber
	custom.WaveOpacity = 0.9
	custom.BgColor = "#101826" // navy
	custom.BgOpacity = 0.7
	writePNG(t, filepath.Join(outDir, "wave_custom.png"), RenderScaled(f, dd2, nil, custom, 2))
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	if err := png.Encode(out, img); err != nil {
		t.Fatal(err)
	}
}
