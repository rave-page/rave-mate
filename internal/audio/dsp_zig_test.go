//go:build zigdsp && cgo

package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/rand"
	"testing"

	"rave.page/mate/internal/zignative"
)

// randF32 yields raw bit patterns — includes NaN/Inf/denormals so the parity
// covers the full IEEE surface (both sides run the same hw ops).
func randF32(rng *rand.Rand, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(rng.Uint32())
	}
	return out
}

func TestZigF32ToLEParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 7, 4096} {
		in := randF32(rng, n)
		// Deterministic edge values on top of raw-bit randoms.
		if n >= 7 {
			copy(in, []float32{0, 1, -1, 1.5, -1.5, float32(math.Inf(1)), float32(math.NaN())})
		}
		for _, g := range []float32{0, 1, 0.5, 1.7, -0.3} {
			goOut := make([]byte, 4*n)
			zgOut := make([]byte, 4*n)
			f32ToLEBytesGo(goOut, in, g)
			zignative.F32ToLEBytes(in, g, zgOut)
			if !bytes.Equal(goOut, zgOut) {
				t.Fatalf("f32ToLE mismatch n=%d gain=%v", n, g)
			}
		}
	}
}

func TestZigFoldStereoParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	rng := rand.New(rand.NewSource(2))
	for _, ch := range []int{1, 3, 6} {
		for _, frames := range []int{0, 1, 5, 4096} {
			in := randF32(rng, frames*ch)
			goOut := make([]float32, frames*2)
			zgOut := make([]float32, frames*2)
			foldStereoGo(in, frames, ch, goOut)
			zignative.FoldStereo(in, frames, ch, zgOut)
			for i := range goOut {
				if math.Float32bits(goOut[i]) != math.Float32bits(zgOut[i]) {
					t.Fatalf("foldStereo mismatch ch=%d frames=%d i=%d", ch, frames, i)
				}
			}
		}
	}
}

func TestZigPCMToF32Parity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	rng := rand.New(rand.NewSource(3))
	type fmtCase struct {
		bits    int
		isFloat bool
	}
	cases := []fmtCase{{8, false}, {16, false}, {24, false}, {32, false}, {32, true}, {64, true}}
	for _, fc := range cases {
		for _, be := range []bool{false, true} {
			for _, ch := range []int{1, 2, 6} {
				bps := fc.bits / 8
				// Native block align + a padded one (WAV files may pad frames).
				for _, ba := range []int{ch * bps, ch*bps + 3} {
					const frames = 999
					src := make([]byte, frames*ba)
					rng.Read(src)
					goOut := make([]float32, frames*ch)
					zgOut := make([]float32, frames*ch)
					pcmToF32Go(src, frames, ch, ba, fc.bits, fc.isFloat, be, goOut)
					zignative.PCMToF32(src, frames, ch, ba, fc.bits, fc.isFloat, be, zgOut)
					for i := range goOut {
						if math.Float32bits(goOut[i]) != math.Float32bits(zgOut[i]) {
							t.Fatalf("pcm mismatch bits=%d float=%v be=%v ch=%d ba=%d i=%d: go=%08x zig=%08x",
								fc.bits, fc.isFloat, be, ch, ba, i,
								math.Float32bits(goOut[i]), math.Float32bits(zgOut[i]))
						}
					}
				}
			}
		}
	}
}

// ── benchmarks (Go vs Zig) — one device pull / one decode block ──────────────

func benchSamples(n int) []float32 {
	rng := rand.New(rand.NewSource(4))
	out := make([]float32, n)
	for i := range out {
		out[i] = rng.Float32()*2 - 1
	}
	return out
}

func BenchmarkF32ToLEGo(b *testing.B) {
	in := benchSamples(4096 * 2)
	out := make([]byte, len(in)*4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f32ToLEBytesGo(out, in, 1.1)
	}
}

func BenchmarkF32ToLEZig(b *testing.B) {
	in := benchSamples(4096 * 2)
	out := make([]byte, len(in)*4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zignative.F32ToLEBytes(in, 1.1, out)
	}
}

func BenchmarkFoldStereoGo(b *testing.B) {
	in := benchSamples(4096 * 6)
	out := make([]float32, 4096*2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		foldStereoGo(in, 4096, 6, out)
	}
}

func BenchmarkFoldStereoZig(b *testing.B) {
	in := benchSamples(4096 * 6)
	out := make([]float32, 4096*2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zignative.FoldStereo(in, 4096, 6, out)
	}
}

func benchPCM(frames, ba int) []byte {
	src := make([]byte, frames*ba)
	rand.New(rand.NewSource(5)).Read(src)
	return src
}

func BenchmarkPCM24Go(b *testing.B) {
	src := benchPCM(4096, 6)
	out := make([]float32, 4096*2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pcmToF32Go(src, 4096, 2, 6, 24, false, false, out)
	}
}

func BenchmarkPCM24Zig(b *testing.B) {
	src := benchPCM(4096, 6)
	out := make([]float32, 4096*2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zignative.PCMToF32(src, 4096, 2, 6, 24, false, false, out)
	}
}

func BenchmarkPCM16Go(b *testing.B) {
	src := benchPCM(4096, 4)
	out := make([]float32, 4096*2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pcmToF32Go(src, 4096, 2, 4, 16, false, false, out)
	}
}

func BenchmarkPCM16Zig(b *testing.B) {
	src := benchPCM(4096, 4)
	out := make([]float32, 4096*2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zignative.PCMToF32(src, 4096, 2, 4, 16, false, false, out)
	}
}

// Sanity: the LE serialization really is the raw bits (unity path).
func TestF32ToLEUnityBits(t *testing.T) {
	in := []float32{0.5, -2}
	out := make([]byte, 8)
	f32ToLEBytes(out, in, 1)
	if binary.LittleEndian.Uint32(out) != math.Float32bits(0.5) {
		t.Fatal("unity path altered bits")
	}
}
