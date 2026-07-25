package audio

import "rave.page/mate/internal/zignative"

// Dispatchers for the per-sample hot loops: Zig kernels when linked (-tags zigdsp;
// byte-exact ports, parity-tested in dsp_zig_test.go), else the Go loops stay
// authoritative. Caller allocates every buffer; kernels never grow anything.

// f32ToLEBytes serializes samples to LE bytes into p (>= 4*len(samples)) with
// pre-gain (0/1 = unity) + ±1 clamp.
func f32ToLEBytes(p []byte, samples []float32, gain float32) {
	if zignative.Available() {
		zignative.F32ToLEBytes(samples, gain, p)
		return
	}
	f32ToLEBytesGo(p, samples, gain)
}

// foldStereo folds interleaved ch-channel samples into out (frames*2).
func foldStereo(in []float32, frames, ch int, out []float32) {
	if zignative.Available() {
		zignative.FoldStereo(in, frames, ch, out)
		return
	}
	foldStereoGo(in, frames, ch, out)
}

// pcmToF32 batch-converts frames packed PCM frames (src: frames*blockAlign bytes)
// to interleaved f32 in dst (frames*ch).
func pcmToF32(src []byte, frames, ch, blockAlign, bits int, isFloat, bigEndian bool, dst []float32) {
	if zignative.Available() {
		zignative.PCMToF32(src, frames, ch, blockAlign, bits, isFloat, bigEndian, dst)
		return
	}
	pcmToF32Go(src, frames, ch, blockAlign, bits, isFloat, bigEndian, dst)
}

// pcmToF32Go is the pure-Go per-sample path (authoritative; parity reference).
func pcmToF32Go(src []byte, frames, ch, blockAlign, bits int, isFloat, bigEndian bool, dst []float32) {
	bps := bits / 8
	for i := 0; i < frames; i++ {
		base := i * blockAlign
		for c := 0; c < ch; c++ {
			s := src[base+c*bps : base+c*bps+bps]
			if bigEndian {
				dst[i*ch+c] = decodeSampleBE(s, bits, isFloat)
			} else {
				dst[i*ch+c] = decodeSample(s, bits, isFloat)
			}
		}
	}
}
