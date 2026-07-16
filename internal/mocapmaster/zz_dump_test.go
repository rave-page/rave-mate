package mocapmaster

// zz_dump_test.go - dev fixture generator (env-gated, skipped in normal runs): dumps the
// golden v1.2 region rendered into a black 1920x1080 composite frame, for the world-side
// RaveMocapRegionReader conformance pass.

import (
	"image"
	"image/png"
	"os"
	"testing"
)

func TestDumpGoldenRegionPNG(t *testing.T) {
	out := os.Getenv("MOCAP_REGION_DUMP")
	if out == "" {
		t.Skip("set MOCAP_REGION_DUMP=<path> to dump the golden region fixture")
	}
	h, dancers := goldenRegion()
	// N frames with an advancing frameCounter so a reader's liveness gate can latch
	for n := 0; n < 8; n++ {
		img := image.NewNRGBA(image.Rect(0, 0, 1920, 1080))
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255 // opaque black canvas
		}
		h.FrameCounter = 42 + uint32(n)
		RenderRegion(img, h, dancers)
		path := out
		if n > 0 {
			path = out[:len(out)-4] + "_" + string(rune('0'+n)) + ".png"
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	t.Logf("golden region fixtures (fc 42..49) -> %s", out)
}
