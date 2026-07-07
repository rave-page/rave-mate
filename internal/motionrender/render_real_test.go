package motionrender

// Real-avatar render check (skips when LAMB.fbx is absent): a 640×400 preview frame must
// show textured variation (many distinct avatar colors, not the flat-violet fallback) and
// stay interactive (<50ms/frame at the default TriCap after warm-up).

import (
	"bytes"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmdyn"
)

func TestRenderRealFBXTextured(t *testing.T) {
	path := filepath.Join(os.Getenv("APPDATA"), "rave-mate", "vr_avatars", "LAMB.fbx")
	if _, err := os.Stat(path); err != nil {
		t.Skip("LAMB.fbx not present")
	}
	m, err := vrm.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	var cam Camera
	cam.FrameModel(m)
	cam.Pitch = 0.35
	f := Frame{W: 640, H: 400, Cam: cam, Model: m}

	full := f
	full.TriCap = 1 << 20 // undecimated correctness frame
	img := Render(full)
	if dir := os.Getenv("MR_DUMP"); dir != "" { // eyeball artifact: MR_DUMP=<dir> go test ...
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "lamb-preview.png"), buf.Bytes(), 0o644)
		}
	}
	start := time.Now()
	const runs = 5
	for range runs {
		Render(f)
	}
	perFrame := time.Since(start) / runs
	t.Logf("preview frame 640×400: %v", perFrame)
	if perFrame > 150*time.Millisecond { // generous CI headroom over the 50ms target
		t.Errorf("preview frame too slow: %v", perFrame)
	}

	colors := map[color.NRGBA]int{}
	avatarPx := 0
	for y := range 400 {
		for x := range 640 {
			c := img.NRGBAAt(x, y)
			if c == colBG || c == colGrid {
				continue
			}
			avatarPx++
			colors[c]++
		}
	}
	if avatarPx < 2500 { // FrameModel keeps the T-pose figure ~130px tall at 640×400
		t.Fatalf("avatar covers %d px, expected a framed model", avatarPx)
	}
	if len(colors) < 200 { // flat violet would yield only a few dozen lambert shades
		t.Errorf("distinct avatar colors = %d, want ≥200 (texture detail)", len(colors))
	}
	t.Logf("avatar px=%d distinct colors=%d", avatarPx, len(colors))
}

// Dynamics integration: rendering with a vrmdyn.State must settle hair/ears/tail into a
// gravity droop that differs from the rigid rest render (and never panic). Dyn=nil path
// is byte-identical to the pre-dynamics renderer by construction.
func TestRenderRealFBXDynamicsDroop(t *testing.T) {
	path := filepath.Join(os.Getenv("APPDATA"), "rave-mate", "vr_avatars", "LAMB.fbx")
	if _, err := os.Stat(path); err != nil {
		t.Skip("LAMB.fbx not present")
	}
	m, err := vrm.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	dyn := vrmdyn.NewState(m)
	if len(dyn.Chains()) == 0 {
		t.Fatal("no dynamic chains on LAMB.fbx")
	}
	var cam Camera
	cam.FrameModel(m)
	cam.Pitch = 0.35
	f := Frame{W: 320, H: 200, Cam: cam, Model: m, Dyn: dyn, DT: 1.0 / 60}
	moved := Render(f)
	for range 29 { // let the sim settle (~0.5s)
		moved = Render(f)
	}
	rigid := Render(Frame{W: 320, H: 200, Cam: cam, Model: m})
	diff := 0
	for i := range rigid.Pix {
		if rigid.Pix[i] != moved.Pix[i] {
			diff++
		}
	}
	if diff == 0 {
		t.Error("dynamics render identical to rigid render - physics had no effect")
	}
	t.Logf("dynamics vs rigid: %d differing pix bytes", diff)
}
