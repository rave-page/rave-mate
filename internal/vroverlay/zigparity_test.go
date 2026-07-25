//go:build zigvr

// Parity gate for the ravevr Zig raster path: every dispatched render (panels, menu,
// stats, path-orbit preview, editor textures, edit-border stamp) must be
// PIXEL-IDENTICAL to the direct Go path across representative overlay states, and must
// actually dispatch (a silent fallback is a failure, not a pass). Runs only with -tags
// zigvr (lib linked); the benchmarks compare Go vs Zig render time on the same states.
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

// assertParity renders via the direct Go path and the Zig path and asserts equal bytes. It also
// asserts the Zig run actually DISPATCHED (no silent record/exec fallback) - otherwise "parity"
// would just be the Go path compared with itself.
func assertParity(t *testing.T, r *Renderer, name string, render func() *image.NRGBA) {
	t.Helper()
	r.zig = false
	goImg := render()
	goPix, w := clonePix(goImg), goImg.Rect.Dx()
	r.zig = true
	ok0, fb0 := r.zigOK.Load(), r.zigFB.Load()
	zigPix := clonePix(render())
	if got := r.zigFB.Load() - fb0; got != 0 {
		t.Fatalf("%s: %d render(s) fell back to the Go raster - not a parity result", name, got)
	}
	if got := r.zigOK.Load() - ok0; got == 0 {
		t.Fatalf("%s: nothing dispatched to the Zig raster", name)
	}
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
		(first/4)%w, first/4/w, first%4)
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

// --- small editor textures (hover row, ghost, tooltip, wrist, strip, outline) ---

func TestZigParitySmallRenders(t *testing.T) {
	r := newParityRenderer(t)
	long := "A tooltip body long enough to wrap over several lines so the panel grows and every glyph run gets recorded"
	cases := []struct {
		name   string
		render func() *image.NRGBA
	}{
		{"hoverRow", r.RenderHoverRow},
		{"stripHover", r.RenderStripHover},
		{"ghost/min", func() *image.NRGBA { return r.RenderGhost(0) }},
		{"ghost/rows", func() *image.NRGBA { return r.RenderGhost(9) }},
		{"tooltip/short", func() *image.NRGBA { return r.RenderTooltip("Chat overlay") }},
		{"tooltip/wrapped", func() *image.NRGBA { return r.RenderTooltip(long) }},
		{"tooltip/empty", func() *image.NRGBA { return r.RenderTooltip("") }},
		{"outline/brand", func() *image.NRGBA { return r.RenderOutline(colName) }},
		{"outline/mint", func() *image.NRGBA { return r.RenderOutline(colMint) }},
		{"strip/empty", func() *image.NRGBA { return r.RenderStrip(nil) }},
		{"strip/cells", func() *image.NRGBA { return r.RenderStrip(parityStripButtons()) }},
	}
	for _, on := range []bool{false, true} {
		for _, hover := range []bool{false, true} {
			on, hover := on, hover
			cases = append(cases, struct {
				name   string
				render func() *image.NRGBA
			}{fmt.Sprintf("wrist/on%v-hover%v", on, hover), func() *image.NRGBA { return r.RenderWrist(on, hover) }})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertParity(t, r, tc.name, tc.render) })
	}
	// Fallback branch: no embedded logo → the wrist badge draws its "RM" text instead.
	t.Run("wrist/nologo", func(t *testing.T) {
		saved := logo
		logo = nil
		t.Cleanup(func() { logo = saved })
		assertParity(t, r, "wrist/nologo", func() *image.NRGBA { return r.RenderWrist(true, true) })
	})
}

func parityStripButtons() []StripButton {
	return []StripButton{
		{Glyph: "◧", Label: "edit", Active: true},
		{Glyph: "▣", Label: "overlays"},
		{Glyph: "WIDEGLYPH", Label: "truncated"}, // exceeds the cell → truncText path
		{Glyph: "🎧", Label: "missing glyph"},
		{Glyph: "≡", Label: "menu", Active: true},
	}
}

// TestZigParityEditBorder covers the ops-only border stamp (Manager.editBorder): the base panel is
// always rendered by the Go path, so only the border stage differs between the two runs.
func TestZigParityEditBorder(t *testing.T) {
	r := newParityRenderer(t)
	cases := []struct {
		name string
		w    int
		col  color.Color
	}{{"grab", 10, colMint}, {"edit", 6, colName}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertParity(t, r, "editBorder/"+tc.name, func() *image.NRGBA {
				zig := r.zig
				r.zig = false
				img := r.Panel(parityChatLines(), panelW, panelH, 0.82)
				r.zig = zig
				r.borderInto(img, tc.col, tc.w)
				return img
			})
		})
	}
}

// --- path orbit preview (worldpath.go: fill + border + Bresenham lines + discs + HUD text) ---

func parityPathGeom(n int) CamPathGeom {
	g := CamPathGeom{}
	for i := 0; i < n; i++ {
		f := float32(i)
		g.Pts = append(g.Pts, [3]float32{f * 0.7 * float32(math.Cos(float64(f)/2)), 1 + f*0.15, f * 0.9 * float32(math.Sin(float64(f)/3))})
		if i > 0 {
			g.Spd = append(g.Spd, 0.2+float32(i%5)*0.3)
			g.Dur = append(g.Dur, 0.4+float32(i%3)*0.5)
		}
	}
	return g
}

func TestZigParityPathOrbit(t *testing.T) {
	r := newParityRenderer(t)
	g := parityPathGeom(14)
	cases := []struct {
		name                string
		g                   CamPathGeom
		yaw, pitch, zoom    float32
		t                   float64
		playing, clampCheck bool
	}{
		{name: "default", g: g, yaw: 0.4, pitch: 0.38, zoom: 1, t: 1.7, playing: true},
		{name: "zoomed-out", g: g, yaw: 2.9, pitch: -1.1, zoom: 4, t: 0, playing: false},
		{name: "zoom-clamped", g: g, yaw: -1.2, pitch: 1.35, zoom: 9, t: 5.5, playing: true}, // zoom > 4 → clamp
		{name: "two-point", g: parityPathGeom(2), yaw: 0.9, pitch: 0.2, zoom: 1, t: 0.3},
		{name: "degenerate", g: parityPathGeom(1), yaw: 0, pitch: 0, zoom: 1}, // < 2 pts → bg + border only
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertParity(t, r, "pathOrbit/"+tc.name, func() *image.NRGBA {
				return r.RenderPathOrbit(tc.g, tc.yaw, tc.pitch, tc.zoom, tc.t, tc.playing)
			})
		})
	}
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

func BenchmarkPathOrbitGo(b *testing.B) {
	g := parityPathGeom(14)
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderPathOrbit(g, 0.4, 0.38, 1, 1.7, true) })
}

func BenchmarkPathOrbitZig(b *testing.B) {
	g := parityPathGeom(14)
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderPathOrbit(g, 0.4, 0.38, 1, 1.7, true) })
}

func BenchmarkGhostGo(b *testing.B) {
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderGhost(9) })
}

func BenchmarkGhostZig(b *testing.B) {
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderGhost(9) })
}

func BenchmarkTooltipGo(b *testing.B) {
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderTooltip(benchTip) })
}

func BenchmarkTooltipZig(b *testing.B) {
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderTooltip(benchTip) })
}

const benchTip = "A tooltip body long enough to wrap over several lines so the panel grows and every glyph run gets recorded"

func BenchmarkStripGo(b *testing.B) {
	btns := parityStripButtons()
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderStrip(btns) })
}

func BenchmarkStripZig(b *testing.B) {
	btns := parityStripButtons()
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderStrip(btns) })
}

func BenchmarkWristGo(b *testing.B) {
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderWrist(true, true) })
}

func BenchmarkWristZig(b *testing.B) {
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderWrist(true, true) })
}

func BenchmarkHoverRowGo(b *testing.B) {
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderHoverRow() })
}

func BenchmarkHoverRowZig(b *testing.B) {
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderHoverRow() })
}

func BenchmarkOutlineGo(b *testing.B) {
	benchRender(b, false, func() *image.NRGBA { return benchR.RenderOutline(colName) })
}

func BenchmarkOutlineZig(b *testing.B) {
	benchRender(b, true, func() *image.NRGBA { return benchR.RenderOutline(colName) })
}
