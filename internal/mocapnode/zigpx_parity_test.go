//go:build zigdsp && cgo

package mocapnode

// Parity + bench gates for the P3 video pixel kernels vs their Go originals
// (ZIG_MIGRATION.md). Skips when the lib isn't linked/ABI-stale.

import (
	"bytes"
	"math/rand"
	"testing"

	"rave.page/mate/internal/zignative"
)

func requireZig(t testing.TB) {
	t.Helper()
	if !zignative.Available() {
		t.Skip("zig core not available")
	}
}

// TestZigRGBAToRGB24Parity: byte-exact across sizes, odd dims, padded strides.
func TestZigRGBAToRGB24Parity(t *testing.T) {
	requireZig(t)
	rng := rand.New(rand.NewSource(1))
	cases := []struct{ w, h, pad int }{
		{1, 1, 0}, {2, 2, 4}, {3, 5, 0}, {17, 9, 12}, {640, 360, 0}, {639, 359, 64}, {1, 33, 8},
	}
	for _, c := range cases {
		stride := c.w*4 + c.pad
		src := make([]byte, (c.h-1)*stride+c.w*4)
		rng.Read(src)
		want := make([]byte, c.w*c.h*3)
		got := make([]byte, c.w*c.h*3)
		rgbaToRGB24Go(src, stride, c.w, c.h, want)
		if !zignative.RGBAToRGB24(src, stride, c.w, c.h, got) {
			t.Fatalf("%dx%d pad %d: kernel refused", c.w, c.h, c.pad)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("%dx%d pad %d: mismatch", c.w, c.h, c.pad)
		}
	}
}

// TestZigRGBAToRGB24Fuzz: seeded-random geometry fuzz.
func TestZigRGBAToRGB24Fuzz(t *testing.T) {
	requireZig(t)
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 200; i++ {
		w := 1 + rng.Intn(97)
		h := 1 + rng.Intn(41)
		stride := w*4 + 4*rng.Intn(9)
		src := make([]byte, (h-1)*stride+w*4)
		rng.Read(src)
		want := make([]byte, w*h*3)
		got := make([]byte, w*h*3)
		rgbaToRGB24Go(src, stride, w, h, want)
		if !zignative.RGBAToRGB24(src, stride, w, h, got) || !bytes.Equal(want, got) {
			t.Fatalf("iter %d (%dx%d stride %d): mismatch", i, w, h, stride)
		}
	}
}

func benchFrame(w, h, bpp, pad int) ([]byte, int) {
	stride := w*bpp + pad
	src := make([]byte, h*stride)
	rand.New(rand.NewSource(3)).Read(src)
	return src, stride
}

func BenchmarkRGBAToRGB24Go(b *testing.B) {
	src, stride := benchFrame(1920, 1080, 4, 0)
	dst := make([]byte, 1920*1080*3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rgbaToRGB24Go(src, stride, 1920, 1080, dst)
	}
}

func BenchmarkRGBAToRGB24Zig(b *testing.B) {
	requireZig(b)
	src, stride := benchFrame(1920, 1080, 4, 0)
	dst := make([]byte, 1920*1080*3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zignative.RGBAToRGB24(src, stride, 1920, 1080, dst)
	}
}
