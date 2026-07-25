//go:build zigdsp && cgo

package vrslgrid

// Parity + bench gates for the batched rz_fill_cells render path vs the Go
// SetRGBA fill loops (ZIG_MIGRATION.md P3). Skips when the lib isn't linked.

import (
	"bytes"
	"image"
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

func randUniverses(rng *rand.Rand, n int) (mapReader, []int) {
	r := mapReader{}
	unis := make([]int, n)
	for i := range unis {
		unis[i] = i + 1
		var d [512]byte
		rng.Read(d[:])
		r[uint16(i+1)] = d
	}
	return r, unis
}

// TestZigRenderParity: Render byte-exact go vs zig across modes + universe counts.
func TestZigRenderParity(t *testing.T) {
	requireZig(t)
	rng := rand.New(rand.NewSource(6))
	for _, mode := range []Mode{ModeMono, ModeRGB9} {
		for _, n := range []int{0, 1, 3, 9, 12} {
			r, unis := randUniverses(rng, n)
			want := render(r, unis, mode, false)
			got := render(r, unis, mode, true)
			if !want.Rect.Eq(got.Rect) || !bytes.Equal(want.Pix, got.Pix) {
				t.Fatalf("mode %s n=%d: render mismatch", mode, n)
			}
		}
	}
}

// TestZigCompositeParity: RenderComposite byte-exact go vs zig — standard +
// extended, mono + rgb9, with and without an overlay painter (ordering pin).
func TestZigCompositeParity(t *testing.T) {
	requireZig(t)
	rng := rand.New(rand.NewSource(7))
	overlay := func(img *image.RGBA) { // deterministic painter in the gap
		for y := 40; y < 60; y++ {
			for x := 300; x < 340; x++ {
				img.Pix[y*img.Stride+x*4] = 0x7f
			}
		}
	}
	for _, mono := range []bool{false, true} {
		for _, ext := range []bool{false, true} {
			for _, ov := range []func(*image.RGBA){nil, overlay} {
				r, unis := randUniverses(rng, 9)
				spec := CompositeSpec{Universes: unis, Mono: mono, Extended: ext,
					FrameCounter: byte(rng.Intn(256)), LookID: 3, SceneID: 5, Blackout: 0, Overlay: ov}
				want := renderComposite(r, spec, false)
				got := renderComposite(r, spec, true)
				if !want.Rect.Eq(got.Rect) || !bytes.Equal(want.Pix, got.Pix) {
					t.Fatalf("mono=%v ext=%v overlay=%v: composite mismatch", mono, ext, ov != nil)
				}
			}
		}
	}
}

// TestZigCompositeFuzz: seeded-random spec/universe fuzz.
func TestZigCompositeFuzz(t *testing.T) {
	requireZig(t)
	rng := rand.New(rand.NewSource(8))
	for i := 0; i < 60; i++ {
		r, unis := randUniverses(rng, rng.Intn(13))
		spec := CompositeSpec{Universes: unis, Mono: rng.Intn(2) == 0, Extended: rng.Intn(2) == 0,
			FrameCounter: byte(rng.Intn(256)), LookID: byte(rng.Intn(256)),
			SceneID: byte(rng.Intn(256)), Blackout: byte(rng.Intn(256))}
		want := renderComposite(r, spec, false)
		got := renderComposite(r, spec, true)
		if !want.Rect.Eq(got.Rect) || !bytes.Equal(want.Pix, got.Pix) {
			t.Fatalf("iter %d (n=%d mono=%v ext=%v): mismatch", i, len(unis), spec.Mono, spec.Extended)
		}
	}
}

func benchSpec(b *testing.B) (mapReader, CompositeSpec) {
	rng := rand.New(rand.NewSource(9))
	r, unis := randUniverses(rng, 9)
	return r, CompositeSpec{Universes: unis, Extended: true, FrameCounter: 7}
}

func BenchmarkRenderCompositeGo(b *testing.B) {
	r, spec := benchSpec(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderComposite(r, spec, false)
	}
}

func BenchmarkRenderCompositeZig(b *testing.B) {
	requireZig(b)
	r, spec := benchSpec(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderComposite(r, spec, true)
	}
}
