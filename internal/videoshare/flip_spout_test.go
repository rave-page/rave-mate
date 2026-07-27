//go:build spout

package videoshare

import (
	"bytes"
	"math/rand"
	"testing"
)

// flip_spout_test.go - the parity gate for the send-path flip. It is PURE PIXEL MATH, so the right
// instrument is a deterministic comparison against the original algorithm, not a live Spout rig
// (which on this box is too flaky about stale sender textures to prove anything about a transform).
//
// flipRowsRef is the code that shipped before increment 4: one scalar 4-byte memcpy per PIXEL inside
// a doubly-nested loop - 8.3 M libc calls per 4K frame. The optimised version must be BYTE-IDENTICAL
// to it for every mode and every geometry.
func flipRowsRef(dst, src []byte, w, h, flip int) {
	for y := 0; y < h; y++ {
		sy := y
		if flip&2 != 0 {
			sy = h - 1 - y
		}
		for x := 0; x < w; x++ {
			sx := x
			if flip&1 != 0 {
				sx = w - 1 - x
			}
			copy(dst[(y*w+x)*4:(y*w+x)*4+4], src[(sy*w+sx)*4:(sy*w+sx)*4+4])
		}
	}
}

func TestFlipRowsMatchesTheOriginalPerPixelAlgorithm(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	geoms := [][2]int{{1, 1}, {2, 1}, {1, 2}, {3, 5}, {16, 9}, {64, 36}, {193, 71}, {320, 180}}
	for _, g := range geoms {
		w, h := g[0], g[1]
		src := make([]byte, w*h*4)
		rng.Read(src)
		for flip := 0; flip < 4; flip++ {
			got := make([]byte, w*h*4)
			want := make([]byte, w*h*4)
			flipRows(got, src, w, h, flip)
			flipRowsRef(want, src, w, h, flip)
			if !bytes.Equal(got, want) {
				t.Fatalf("%dx%d flip=%d: optimised flip differs from the original algorithm", w, h, flip)
			}
		}
	}
}

// TestFlipRowsIdentityAndInvolution: flip 0 is a straight copy, and applying any mode twice is the
// identity - properties the per-pixel original also had, so a regression in either direction shows.
func TestFlipRowsIdentityAndInvolution(t *testing.T) {
	const w, h = 37, 21
	rng := rand.New(rand.NewSource(2))
	src := make([]byte, w*h*4)
	rng.Read(src)
	out := make([]byte, w*h*4)
	flipRows(out, src, w, h, 0)
	if !bytes.Equal(out, src) {
		t.Fatal("flip 0 must be a straight copy")
	}
	for flip := 1; flip < 4; flip++ {
		once := make([]byte, w*h*4)
		twice := make([]byte, w*h*4)
		flipRows(once, src, w, h, flip)
		flipRows(twice, once, w, h, flip)
		if !bytes.Equal(twice, src) {
			t.Fatalf("flip=%d applied twice is not the identity", flip)
		}
		if bytes.Equal(once, src) {
			t.Fatalf("flip=%d changed nothing", flip)
		}
	}
}

// TestFlipRowsRejectsShortBuffers: a bogus geometry must never write out of bounds.
func TestFlipRowsRejectsShortBuffers(t *testing.T) {
	src := make([]byte, 16)
	dst := make([]byte, 16)
	flipRows(dst, src, 100, 100, 3) // buffers far too small
	for _, b := range dst {
		if b != 0 {
			t.Fatal("flipRows wrote into an undersized buffer")
		}
	}
}

// BenchmarkFlipRows quantifies the change at 4K (the geometry the epic exists for).
// go test -tags spout ./internal/videoshare -run XXX -bench FlipRows -benchtime 20x
func BenchmarkFlipRows(b *testing.B) {
	const w, h = 3840, 2160
	src := make([]byte, w*h*4)
	dst := make([]byte, w*h*4)
	for _, c := range []struct {
		name string
		flip int
	}{{"vertical", 2}, {"horizontal", 1}, {"both", 3}} {
		b.Run(c.name+"/optimised", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				flipRows(dst, src, w, h, c.flip)
			}
		})
		b.Run(c.name+"/perPixelOriginal", func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				flipRowsRef(dst, src, w, h, c.flip)
			}
		})
	}
}
