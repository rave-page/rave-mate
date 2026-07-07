package vroverlay

import (
	"errors"
	"math"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/vrbind"
)

func TestStripCellAt(t *testing.T) {
	cases := []struct {
		u    float32
		n    int
		want int
	}{
		{0, 6, 0}, {0.999, 6, 5}, {1, 6, 5}, {0.5, 6, 3}, {0.16, 6, 0}, {0.17, 6, 1},
		{0.5, 0, -1}, {-0.1, 6, 0},
	}
	for _, c := range cases {
		if got := stripCellAt(c.u, c.n); got != c.want {
			t.Fatalf("stripCellAt(%v,%d) = %d, want %d", c.u, c.n, got, c.want)
		}
	}
}

func TestStripCellOffset(t *testing.T) {
	// Middle cell of an odd count sits at the quad center; z always nudges toward the viewer.
	if x, z := stripCellOffset(1, 3, 0.3); math.Abs(x) > 1e-9 || z != 0.002 {
		t.Fatalf("center cell offset = (%v,%v), want (0,0.002)", x, z)
	}
	// First cell of n=4 at width 0.4: (0.5/4-0.5)*0.4 = -0.15.
	if x, _ := stripCellOffset(0, 4, 0.4); math.Abs(x+0.15) > 1e-9 {
		t.Fatalf("first cell x = %v, want -0.15", x)
	}
	// Symmetry: cell i and n-1-i mirror.
	xa, _ := stripCellOffset(0, 5, 0.5)
	xb, _ := stripCellOffset(4, 5, 0.5)
	if math.Abs(xa+xb) > 1e-9 {
		t.Fatalf("offsets not symmetric: %v vs %v", xa, xb)
	}
}

func TestStripWidthM(t *testing.T) {
	if w := stripWidthM(0); w != 0.09 {
		t.Fatalf("min clamp = %v, want 0.09", w)
	}
	if w := stripWidthM(6); math.Abs(w-0.27) > 1e-9 {
		t.Fatalf("6 cells = %v, want 0.27", w)
	}
	if w := stripWidthM(50); w != 0.55 {
		t.Fatalf("max clamp = %v, want 0.55", w)
	}
}

func TestQuickGlyphAndLabel(t *testing.T) {
	if g := quickGlyph(config.VRQuickButton{Glyph: "rec"}); g != "REC" {
		t.Fatalf("glyph = %q, want REC", g)
	}
	if g := quickGlyph(config.VRQuickButton{Label: "stream toggle"}); g != "STR" {
		t.Fatalf("derived glyph = %q, want STR", g)
	}
	if l := quickLabel(config.VRQuickButton{Action: "obs.record", Target: "pc1"}); l != "obs.record pc1" {
		t.Fatalf("label = %q", l)
	}
	if l := quickLabel(config.VRQuickButton{Label: "Rec", Action: "obs.record"}); l != "Rec" {
		t.Fatalf("label = %q, want Rec", l)
	}
}

// Strip clicks fire against the DISPLAYED snapshot; edit mode is a plain toggle + no longer gates cells.
func TestStripClickRoutingAndPlainToggle(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	e.on = true
	feat := config.VROverlayFeature{}
	e.driveStrip(feat, HandLeft)
	if len(e.stripShown) != 6 { // ED OV + OBS VIS MNU (no CAM without a provider)
		t.Fatalf("stripShown = %d buttons, want 6", len(e.stripShown))
	}
	cellU := func(i int) float32 { return (float32(i) + 0.5) / float32(len(e.stripShown)) }

	// ED is a plain toggle: one tap enters edit mode.
	e.pointerClick(pointerHit{key: stripKey, u: cellU(0)})
	if !e.editMode {
		t.Fatal("ED tap should enter edit mode")
	}
	// Edit mode no longer gates the strip - OV still jumps straight to the overlay-select page.
	e.pointerClick(pointerHit{key: stripKey, u: cellU(1)})
	if !e.fullMenu || e.page != pageOverlays {
		t.Fatalf("OV in edit mode → fullMenu=%v page=%q, want true/%q", e.fullMenu, e.page, pageOverlays)
	}
	// A second ED tap exits.
	e.pointerClick(pointerHit{key: stripKey, u: cellU(0)})
	if e.editMode {
		t.Fatal("second ED tap should exit edit mode")
	}
	// VIS toggles the global hide.
	e.pointerClick(pointerHit{key: stripKey, u: cellU(4)})
	if !e.m.contentHidden {
		t.Fatal("VIS should hide all overlays")
	}
}

// A failed strip upload must not commit the snapshot - nothing displayed, nothing clickable.
func TestStripFailedUploadNoSnapshot(t *testing.T) {
	rt := &fakeRT{texErr: errors.New("no")}
	e := newTestEditor(t, rt)
	e.on = true
	e.driveStrip(config.VROverlayFeature{}, HandLeft)
	if len(e.stripShown) != 0 || e.stripSig != "" {
		t.Fatalf("failed upload committed snapshot (%d buttons, sig %q)", len(e.stripShown), e.stripSig)
	}
	e.pointerClick(pointerHit{key: stripKey, u: 0.5}) // must be a no-op
	rt.texErr = nil
	e.driveStrip(config.VROverlayFeature{}, HandLeft)
	if len(e.stripShown) == 0 {
		t.Fatal("recovered upload should commit the snapshot")
	}
}

// CAM appears only with a camera-path provider; quick buttons append after the fixed set.
func TestBuildStripProvidersAndQuickButtons(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	e.m.camPaths = func() []CamPathItem { return nil }
	feat := config.VROverlayFeature{QuickButtons: []config.VRQuickButton{
		{Label: "Club layout", Action: "layout.load", Target: "club"},
		{Action: ""}, // no action → skipped
	}}
	btns := e.buildStrip(feat)
	if len(btns) != 8 { // 6 fixed + CAM + 1 quick
		t.Fatalf("buttons = %d, want 8", len(btns))
	}
	if btns[3].Glyph != "CAM" {
		t.Fatalf("btns[3] = %q, want CAM", btns[3].Glyph)
	}
	if last := btns[len(btns)-1]; last.Label != "Club layout" || last.Glyph != "CLU" {
		t.Fatalf("quick button = %+v", last)
	}
}

// Quick actions route: layout/campath in-editor, everything else via the bind dispatcher.
func TestFireQuickActionRouting(t *testing.T) {
	feat := config.VROverlayFeature{
		Layouts:  []config.VRLayout{{Name: "club", Overlays: []config.VROverlay{{ID: "a", Type: "chat"}}}},
		Overlays: []config.VROverlay{{ID: "old", Type: "chat"}},
	}
	disp := vrbind.NewDispatcher()
	var fired vrbind.Bind
	disp.Register(vrbind.ActOverlayToggle, func(tgt string) { fired = vrbind.Bind{Action: vrbind.ActOverlayToggle, Target: tgt} })
	var loadedPath string
	m := &Manager{rt: &fakeRT{},
		mutate:      func(fn func(*config.VROverlayFeature)) { fn(&feat) },
		bindDisp:    disp,
		loadCamPath: func(f string) error { loadedPath = f; return nil },
	}
	e := &editor{m: m}
	e.resetSession()

	e.fireQuickAction("layout.load", "club")
	if len(feat.Overlays) != 1 || feat.Overlays[0].ID != "a" {
		t.Fatalf("layout.load did not apply: %+v", feat.Overlays)
	}
	e.fireQuickAction("campath.load", "path.json")
	if loadedPath != "path.json" {
		t.Fatalf("campath.load = %q", loadedPath)
	}
	e.fireQuickAction("overlay.toggle", "a")
	if fired.Action != vrbind.ActOverlayToggle || fired.Target != "a" {
		t.Fatalf("dispatcher got %+v", fired)
	}
}
