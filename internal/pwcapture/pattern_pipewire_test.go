//go:build linux && pipewire

package pwcapture

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"testing"
	"time"
)

// TestCaptureDmabufPattern verifies the DMA-BUF import is pixel-correct against funnel-producer's
// known pattern: corner squares TL=red TR=green BL=blue BR=white (image space) + opaque alpha.
// This proves orientation/flip, channel order (RGBA), alpha and that the tiled modifier detiled
// correctly. Set RPW_TEST_NODE to the producer node. Dumps the frame to RPW_DUMP_PNG if set.
func TestCaptureDmabufPattern(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" {
		t.Skip("set RPW_TEST_NODE")
	}
	if !captureAvailable() {
		t.Skip("libpipewire not available")
	}
	nodes, _ := DiscoverNodes(context.Background())
	var w, h int
	for _, n := range nodes {
		if n.Name == node {
			w, h = n.Width, n.Height
		}
	}
	if w == 0 {
		t.Fatalf("node %q not discovered", node)
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// skip a few frames to let negotiation settle
	var f struct {
		pix  []byte
		w, h int
	}
	for i := 0; i < 10; i++ {
		fr, err := src.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		f.pix, f.w, f.h = fr.Payload, w, h
	}

	if os.Getenv("RPW_DUMP_PNG") != "" {
		img := &image.NRGBA{Pix: f.pix, Stride: f.w * 4, Rect: image.Rect(0, 0, f.w, f.h)}
		if fo, err := os.Create(os.Getenv("RPW_DUMP_PNG")); err == nil {
			_ = png.Encode(fo, img)
			_ = fo.Close()
		}
	}

	px := func(x, y int) (r, g, b, a byte) {
		o := (y*f.w + x) * 4
		return f.pix[o], f.pix[o+1], f.pix[o+2], f.pix[o+3]
	}
	s := f.w
	if f.h < s {
		s = f.h
	}
	s /= 8 // corner square size, matches producer
	q := s / 2
	type ck struct {
		name       string
		x, y       int
		wr, wg, wb byte
	}
	checks := []ck{
		{"TL", q, q, 255, 0, 0},                         // red
		{"TR", f.w - 1 - q, q, 0, 255, 0},               // green
		{"BL", q, f.h - 1 - q, 0, 0, 255},               // blue
		{"BR", f.w - 1 - q, f.h - 1 - q, 255, 255, 255}, // white
	}
	near := func(got, want byte) bool {
		d := int(got) - int(want)
		return d > -40 && d < 40
	}
	for _, c := range checks {
		r, g, b, a := px(c.x, c.y)
		t.Logf("%s (%d,%d) = RGBA(%d,%d,%d,%d) want(%d,%d,%d)", c.name, c.x, c.y, r, g, b, a, c.wr, c.wg, c.wb)
		if !near(r, c.wr) || !near(g, c.wg) || !near(b, c.wb) {
			t.Errorf("%s corner = RGBA(%d,%d,%d) want ~(%d,%d,%d) - orientation/channel/detile wrong",
				c.name, r, g, b, c.wr, c.wg, c.wb)
		}
		if a != 255 {
			t.Errorf("%s alpha = %d, want 255", c.name, a)
		}
	}
	fmt.Fprintf(os.Stderr, "pattern OK %dx%d\n", f.w, f.h)
}
