package vroverlay

import (
	"image/color"
	"testing"
)

func TestPanelRenders(t *testing.T) {
	r, err := NewRenderer(1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	lines := []Line{
		{Name: "bob", Text: "hey everyone this is a fairly long message that should wrap across lines", Color: color.NRGBA{R: 255, A: 255}},
		{Text: "★ alice followed", Color: colName},
	}
	img := r.Panel(lines, 640, 480, 0.82)
	if img.Bounds().Dx() != 640 || img.Bounds().Dy() != 480 {
		t.Fatalf("bad size: %v", img.Bounds())
	}
	// Some pixel must be non-zero (panel bg at least).
	nonzero := false
	for _, p := range img.Pix {
		if p != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("panel is empty")
	}
}

func TestWrapText(t *testing.T) {
	r, _ := NewRenderer(1)
	defer r.Close()
	segs := wrapText(r.body, "one two three four five six seven eight nine ten", 80)
	if len(segs) < 2 {
		t.Fatalf("expected wrapping, got %v", segs)
	}
}

func TestStubRuntime(t *testing.T) {
	rt := NewRuntime()
	if rt.Available() {
		t.Skip("vr build - stub test N/A")
	}
	if err := rt.Init(); err != nil {
		t.Fatal(err)
	}
	if err := rt.EnsureOverlay("k", "n"); err != nil {
		t.Fatal(err)
	}
	rt.Shutdown()
}
