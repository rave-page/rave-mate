package vroverlay

import (
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

// Tooltip textures keep ONE size across hover sweeps (high-water padded, content centered):
// SetOverlayRaw can't resize, so any height change destroy+recreated the overlay at up to 90Hz.
func TestTooltipTextureSizeStable(t *testing.T) {
	r, err := NewRenderer(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	a := r.RenderTooltip("hi")
	b := r.RenderTooltip("a somewhat longer tooltip that wraps to two lines in the panel width")
	if a.Bounds() != b.Bounds() {
		t.Fatalf("short vs medium tooltip resized: %v vs %v", a.Bounds(), b.Bounds())
	}
	long := strings.Repeat("many words that force wrapping well past the initial high-water ", 6)
	c := r.RenderTooltip(long)
	if c.Bounds().Dy() <= a.Bounds().Dy() {
		t.Fatalf("long tooltip did not grow the high-water: %v vs %v", c.Bounds(), a.Bounds())
	}
	d := r.RenderTooltip("hi") // after growth, short tooltips stay at the grown size
	if d.Bounds() != c.Bounds() {
		t.Fatalf("post-growth short tooltip resized again: %v vs %v", d.Bounds(), c.Bounds())
	}
}

// Cold start is paced: at most tickCreateBudget new overlays (create + first upload) per tick;
// the remainder follow on later ticks until all exist.
func TestTickColdStartPaced(t *testing.T) {
	const n = 5
	var ovls []config.VROverlay
	for i := 0; i < n; i++ {
		ovls = append(ovls, config.VROverlay{ID: string(rune('a' + i)), Type: "chat", Enabled: true})
	}
	m := New(logbus.New(16), nil, &fakeRT{}, func() config.VROverlayFeature {
		return config.VROverlayFeature{Overlays: ovls}
	}, nil)
	rend, err := NewRenderer(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rend.Close)
	m.rend = rend
	want := 0
	for tick := 1; want < n; tick++ {
		m.tick()
		want = min(tick*tickCreateBudget, n)
		if got := len(m.created); got != want {
			t.Fatalf("tick %d: created=%d, want %d", tick, got, want)
		}
	}
}

// The camera-path preview overlay is created lazily on first use - an idle editor session must
// never add it to the cold-start burst (it used to be Ensure'd unconditionally every tick).
func TestWorldPathLazyCreate(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	for i := 0; i < 3; i++ {
		e.driveWorldPath(config.VROverlayFeature{}, HandNone) // editor closed / no path loaded
	}
	if e.worldPathInit {
		t.Fatal("path-preview overlay created while unused")
	}
}
