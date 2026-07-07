package giokit

import (
	"image"
	"testing"
)

// TestThemeDensityMetrics: density-first metrics are the migration's contract.
func TestThemeDensityMetrics(t *testing.T) {
	th := NewTheme()
	if th.ControlHeight < 24 || th.ControlHeight > 26 {
		t.Errorf("ControlHeight %v outside dense 24–26", th.ControlHeight)
	}
	if th.PadX < 4 || th.PadX > 6 || th.PadY < 4 || th.PadY > 6 {
		t.Errorf("padding %v/%v outside dense 4–6", th.PadX, th.PadY)
	}
	if th.CaptionSize < 12 || th.TextSize > 13 {
		t.Errorf("text sizes %v/%v outside dense 12–13", th.CaptionSize, th.TextSize)
	}
	if th.Radius != 8 {
		t.Errorf("Radius = %v, want 8 (--radius)", th.Radius)
	}
	if th.ToolStripH != 32 {
		t.Errorf("ToolStripH = %v, want 32", th.ToolStripH)
	}
}

// TestThemeBrandPalette: canonical web tokens.
func TestThemeBrandPalette(t *testing.T) {
	th := NewTheme()
	for name, got := range map[string][4]uint8{
		"Bg":        {th.Bg.R, th.Bg.G, th.Bg.B, 0x0a},
		"Fg":        {th.Fg.R, th.Fg.G, th.Fg.B, 0xfa},
		"BrandBase": {th.BrandBase.R, th.BrandBase.G, th.BrandBase.B, 0xf7},
	} {
		if got[0] != got[3] {
			t.Errorf("%s.R = %#x, want %#x", name, got[0], got[3])
		}
	}
	if ColBrandBase != rgb(0xF70864) || ColBrandHot != rgb(0xFF3E8A) || ColMint != rgb(0x08F79B) ||
		ColViolet != rgb(0x7C3AED) || ColAmber != rgb(0xFFB547) {
		t.Error("brand accent tokens drifted from the canonical web values")
	}
}

// TestThemeFonts: shaper built + Orbitron registered as display/strong faces.
func TestThemeFonts(t *testing.T) {
	th := NewTheme()
	if th.Shaper == nil {
		t.Fatal("nil shaper")
	}
	if th.Display.Typeface != Orbitron || th.Strong.Typeface != Orbitron {
		t.Error("display/strong faces must be Orbitron")
	}
	if th.Sans.Typeface == Orbitron {
		t.Error("body must be the base sans, not Orbitron")
	}
}

func TestRegistryOffsetsAndBounds(t *testing.T) {
	r := NewRegistry()
	r.BeginFrame()
	r.PushOffset(image.Pt(10, 20))
	r.Add("button", "play", image.Pt(30, 24), nil)
	r.PushOffset(image.Pt(5, 5)) // nested container: absolute (15,25)
	r.Add("toggle", "loop", image.Pt(12, 12), nil)
	r.PopOffset()
	r.PopOffset()
	r.Add("slider", "seek", image.Pt(100, 24), nil) // back at origin
	r.EndFrame()

	nodes := r.Snapshot()
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(nodes))
	}
	want := map[string]image.Rectangle{
		"play": image.Rect(10, 20, 40, 44),
		"loop": image.Rect(15, 25, 27, 37),
		"seek": image.Rect(0, 0, 100, 24),
	}
	for _, n := range nodes {
		if n.Bounds != want[n.Label] {
			t.Errorf("%s bounds = %v, want %v", n.Label, n.Bounds, want[n.Label])
		}
	}
	// Snapshot ordering: top-to-bottom, left-to-right.
	if nodes[0].Label != "seek" || nodes[1].Label != "play" || nodes[2].Label != "loop" {
		t.Errorf("snapshot order = %s,%s,%s", nodes[0].Label, nodes[1].Label, nodes[2].Label)
	}
}

func TestRegistryActivate(t *testing.T) {
	r := NewRegistry()
	invalidated := 0
	r.SetInvalidate(func() { invalidated++ })

	fired := 0
	r.BeginFrame()
	r.Add("button", "play", image.Pt(10, 10), func() { fired++ })
	r.EndFrame()

	if r.Activate("nope") {
		t.Error("unknown label must return false")
	}
	if !r.Activate("play") {
		t.Error("known label must return true")
	}
	if invalidated != 1 {
		t.Errorf("invalidate calls = %d, want 1", invalidated)
	}
	if fired != 0 {
		t.Error("activation must be deferred to the next frame")
	}
	r.BeginFrame() // runs the queued activation
	if fired != 1 {
		t.Errorf("fired = %d, want 1", fired)
	}
	r.EndFrame()
	r.BeginFrame() // queue drained - no re-fire
	if fired != 1 {
		t.Errorf("fired = %d after second frame, want 1", fired)
	}
}

func TestRegistryFramePublishing(t *testing.T) {
	r := NewRegistry()
	r.BeginFrame()
	r.Add("button", "a", image.Pt(1, 1), nil)
	// Not EndFrame'd yet - snapshot must still be the previous (empty) frame.
	if len(r.Snapshot()) != 0 {
		t.Error("snapshot leaked an unfinished frame")
	}
	r.EndFrame()
	if len(r.Snapshot()) != 1 {
		t.Error("snapshot missing the published frame")
	}
	r.BeginFrame()
	r.EndFrame() // empty frame replaces it
	if len(r.Snapshot()) != 0 {
		t.Error("stale nodes survived an empty frame")
	}
}

// TestRegistryNilSafe: giokit widgets pass reg through unconditionally.
func TestRegistryNilSafe(t *testing.T) {
	var r *Registry
	r.Add("button", "x", image.Pt(1, 1), nil)
	if r.Activate("x") {
		t.Error("nil registry must not activate")
	}
	if r.Snapshot() != nil {
		t.Error("nil registry snapshot must be nil")
	}
	r.PushOffset(image.Pt(1, 1))
	r.PopOffset()
}

func TestListMath(t *testing.T) {
	if y := RowY(5, 3, 7, 22); y != 2*22-7 {
		t.Errorf("RowY = %d", y)
	}
	if y := RowY(3, 3, 0, 22); y != 0 {
		t.Errorf("RowY first = %d", y)
	}
	for _, c := range []struct{ vp, row, want int }{
		{220, 22, 11}, // exact fit + 1 partial
		{221, 22, 12}, // spill row
		{0, 22, 0},    // empty viewport
		{100, 0, 0},   // degenerate row height
		{21, 22, 2},   // viewport smaller than a row: up to 2 partials
	} {
		if got := VisibleRows(c.vp, c.row); got != c.want {
			t.Errorf("VisibleRows(%d,%d) = %d, want %d", c.vp, c.row, got, c.want)
		}
	}
	if RowsHeight(5, 22) != 110 || RowsHeight(-1, 22) != 0 {
		t.Error("RowsHeight")
	}
}
