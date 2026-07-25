//go:build zigvr

// Parity gate for the ravevr Zig raster path: every dispatched render (Panel /
// RenderMenu / RenderStats) must be PIXEL-IDENTICAL to the direct Go path across
// representative overlay states. Runs only with -tags zigvr (lib linked); the
// benchmarks compare Go vs Zig render time on the same states.
package vroverlay

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"rave.page/mate/internal/zigvr"
)

func newParityRenderer(t testing.TB) *Renderer {
	t.Helper()
	if !zigvr.Available() {
		t.Skip("zigvr lib not linked")
	}
	r, err := NewRenderer(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if !r.zig {
		t.Fatal("renderer did not pick up zigvr")
	}
	return r
}

func clonePix(img *image.NRGBA) []byte { return append([]byte(nil), img.Pix...) }

// assertParity renders via the direct Go path and the Zig path and asserts equal bytes.
func assertParity(t *testing.T, r *Renderer, name string, render func() *image.NRGBA) {
	t.Helper()
	r.zig = false
	goPix := clonePix(render())
	r.zig = true
	zigPix := clonePix(render())
	if bytes.Equal(goPix, zigPix) {
		return
	}
	diff, first := 0, -1
	for i := range goPix {
		if goPix[i] != zigPix[i] {
			diff++
			if first < 0 {
				first = i
			}
		}
	}
	t.Fatalf("%s: %d/%d bytes differ (first at %d: go=%d zig=%d, px %d,%d ch %d)",
		name, diff, len(goPix), first, goPix[first], zigPix[first],
		(first/4)%640, first/4/640, first%4)
}

func parityChatLines() []Line {
	return []Line{
		{Name: "raver_99", Text: "this set goes so hard Kappa and this message is long enough to wrap across at least two visual rows in the panel", Color: color.NRGBA{R: 255, G: 127, B: 80, A: 255}},
		{Name: "bassqueen", Text: "PogChamp drop incoming", Color: color.NRGBA{R: 30, G: 144, B: 255, A: 255}},
		{Name: "nocolor", Text: "name color defaults to brand"},
		{Name: "emptymsg", Text: ""},
		{Text: "+ raver_99 followed", Color: colName},
		{Text: "* neonkid cheered 500 bits", Color: colMint},
		{Text: "alert with default color"},
	}
}

func TestZigParityPanel(t *testing.T) {
	r := newParityRenderer(t)
	cases := []struct {
		name  string
		lines []Line
		alpha float64
	}{
		{"chat", parityChatLines(), 0.82},
		{"chat-opaque", parityChatLines(), 1},
		{"chat-clear", parityChatLines(), 0},
		{"empty", nil, 0.82},
	}
	// Overflow: more rows than fit → bottom-aligned tail.
	var many []Line
	for i := 0; i < 40; i++ {
		many = append(many, Line{Name: fmt.Sprintf("user%02d", i), Text: fmt.Sprintf("message number %d with some body text", i), Color: chatColor("#9146FF")})
	}
	cases = append(cases, struct {
		name  string
		lines []Line
		alpha float64
	}{"overflow", many, 0.6})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertParity(t, r, "panel/"+tc.name, func() *image.NRGBA {
				return r.Panel(tc.lines, panelW, panelH, tc.alpha)
			})
		})
	}
}

func parityMenuItems() []MenuItem {
	return []MenuItem{
		{Kind: MIHeader, Label: "OVERLAYS"},
		{Kind: MIAction, Label: "Chat overlay", Value: "shown"},
		{Kind: MIAction, Label: "A very long action row label that must be truncated with the ellipsis suffix"},
		{Kind: MISlider, Label: "Opacity", Value: "82%", Frac: 0.82},
		{Kind: MISlider, Label: "Width", Value: "1.40 m", Frac: 0},
		{Kind: MISlider, Label: "Full", Value: "100%", Frac: 1},
		{Kind: MIAction, Label: "Unicode › row 🎧 emoji has no glyph", Value: "édition"},
	}
}

func TestZigParityMenu(t *testing.T) {
	r := newParityRenderer(t)
	t.Run("items", func(t *testing.T) {
		assertParity(t, r, "menu/items", func() *image.NRGBA {
			return r.RenderMenu("RAVE-MATE — SETTINGS", parityMenuItems(), 0.95, 0)
		})
	})
	t.Run("padded", func(t *testing.T) {
		assertParity(t, r, "menu/padded", func() *image.NRGBA {
			return r.RenderMenu("SHORT", parityMenuItems()[:2], 1, 12)
		})
	})
	t.Run("empty", func(t *testing.T) {
		assertParity(t, r, "menu/empty", func() *image.NRGBA {
			return r.RenderMenu("EMPTY", nil, 0.5, 3)
		})
	})
}

func parityStatsView() statsView {
	mk := func(n int, f func(i int) float64) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = f(i)
		}
		return out
	}
	return statsView{
		title: "PERFORMANCE",
		rows: []statsRow{
			{"App CPU", "37%", colMint},
			{"App RAM", "412 MB", colText},
			{"System CPU", "88%", colAmber},
			{"VR frame rate", "90 / 90 fps", colMint},
			{"Reprojection", "3 reproj / 0 dropped", colAmber},
			{"nil-color row", "n/a", nil},
		},
		graph: []statsSeries{
			{label: "app cpu%", vals: mk(120, func(i int) float64 { return 20 + 15*math.Sin(float64(i)/9) }), col: colMint, fill: true},
			{label: "sys cpu%", vals: mk(90, func(i int) float64 {
				if i%17 == 0 {
					return math.NaN() // gap
				}
				return 40 + float64(i%23)
			}), col: colViolet},
		},
		footer: "reproj = frames re-shown to hold your headset's refresh rate; a sustained count above 0 means the GPU can't keep up.",
	}
}

func TestZigParityStats(t *testing.T) {
	r := newParityRenderer(t)
	t.Run("full", func(t *testing.T) {
		assertParity(t, r, "stats/full", func() *image.NRGBA {
			return r.RenderStats(parityStatsView(), panelW, panelH, 0.9)
		})
	})
	t.Run("waiting", func(t *testing.T) {
		v := statsView{title: "NETWORK", rows: []statsRow{{"Network", "waiting for data", colMuted}},
			footer: "rates are bytes/sec."}
		assertParity(t, r, "stats/waiting", func() *image.NRGBA {
			return r.RenderStats(v, panelW, panelH, 0.9)
		})
	})
}

// --- benchmarks: Go vs Zig on identical states ---

func benchRender(b *testing.B, zig bool, render func() *image.NRGBA) {
	r := newParityRenderer(b)
	r.zig = zig
	benchR = r
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		render()
	}
}

var benchR *Renderer // shared by the bench closures below

func BenchmarkPanelGo(b *testing.B) {
	lines := parityChatLines()
	benchRender(b, false, func() *image.NRGBA { return benchR.Panel(lines, panelW, panelH, 0.82) })
}

func BenchmarkPanelZig(b *testing.B) {
	lines := parityChatLines()
	benchRender(b, true, func() *image.NRGBA { return benchR.Panel(lines, panelW, panelH, 0.82) })
}

func BenchmarkMenuGo(b *testing.B) {
	items := parityMenuItems()
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderMenu("SETTINGS", items, 0.95, 0) })
}

func BenchmarkMenuZig(b *testing.B) {
	items := parityMenuItems()
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderMenu("SETTINGS", items, 0.95, 0) })
}

func BenchmarkStatsGo(b *testing.B) {
	v := parityStatsView()
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderStats(v, panelW, panelH, 0.9) })
}

func BenchmarkStatsZig(b *testing.B) {
	v := parityStatsView()
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderStats(v, panelW, panelH, 0.9) })
}
