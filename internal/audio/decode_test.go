package audio

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// sampleAt is the deterministic test signal: a per-channel ramp distinct enough that a wrong
// seek offset is caught. Range stays within [-1,1).
func sampleAt(frame, ch int) float32 {
	v := math.Sin(2*math.Pi*float64(frame)/64) * 0.5
	if ch == 1 {
		v = -v * 0.5
	}
	return float32(v)
}

func writeWAV(t testing.TB, path string, frames, channels, bits int, isFloat bool) {
	t.Helper()
	blockAlign := channels * bits / 8
	dataSize := frames * blockAlign
	var buf []byte
	put4 := func(s string) { buf = append(buf, s...) }
	put32 := func(v uint32) { var b [4]byte; binary.LittleEndian.PutUint32(b[:], v); buf = append(buf, b[:]...) }
	put16 := func(v uint16) { var b [2]byte; binary.LittleEndian.PutUint16(b[:], v); buf = append(buf, b[:]...) }
	put4("RIFF")
	put32(uint32(36 + dataSize))
	put4("WAVE")
	put4("fmt ")
	put32(16)
	if isFloat {
		put16(wavFmtFloat)
	} else {
		put16(wavFmtPCM)
	}
	put16(uint16(channels))
	put32(48000)
	put32(uint32(48000 * blockAlign))
	put16(uint16(blockAlign))
	put16(uint16(bits))
	put4("data")
	put32(uint32(dataSize))
	for f := 0; f < frames; f++ {
		for c := 0; c < channels; c++ {
			buf = append(buf, encLE(sampleAt(f, c), bits, isFloat)...)
		}
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func encLE(v float32, bits int, isFloat bool) []byte {
	if isFloat {
		b := make([]byte, bits/8)
		if bits == 32 {
			binary.LittleEndian.PutUint32(b, math.Float32bits(v))
		} else {
			binary.LittleEndian.PutUint64(b, math.Float64bits(float64(v)))
		}
		return b
	}
	switch bits {
	case 8: // WAV 8-bit is unsigned
		return []byte{byte(int16(v*127) + 128)}
	case 16:
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(int16(v*32767)))
		return b
	case 24:
		iv := int32(v * 8388607)
		return []byte{byte(iv), byte(iv >> 8), byte(iv >> 16)}
	case 32:
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(int32(v*2147483647)))
		return b
	}
	return nil
}

// wavX = writeWAVX container variants.
type wavX struct {
	extensible bool // WAVE_FORMAT_EXTENSIBLE fmt (sz 40, tag in SubFormat GUID)
	pad        int  // extra pad bytes per frame (blockAlign > ch*bps)
	junk       bool // odd-sized JUNK chunk before fmt (RIFF pad byte)
	oddFmt     bool // fmt sz 17 with stray byte, NO pad — exercises the Go quirk
}

func writeWAVX(t testing.TB, path string, frames, channels, bits int, isFloat bool, o wavX) {
	t.Helper()
	bps := bits / 8
	blockAlign := channels*bps + o.pad
	dataSize := frames * blockAlign
	var buf []byte
	put4 := func(s string) { buf = append(buf, s...) }
	put32 := func(v uint32) { var b [4]byte; binary.LittleEndian.PutUint32(b[:], v); buf = append(buf, b[:]...) }
	put16 := func(v uint16) { var b [2]byte; binary.LittleEndian.PutUint16(b[:], v); buf = append(buf, b[:]...) }
	tag := uint16(wavFmtPCM)
	if isFloat {
		tag = wavFmtFloat
	}
	fmtCore := func(t16 uint16) {
		put16(t16)
		put16(uint16(channels))
		put32(48000)
		put32(uint32(48000 * blockAlign))
		put16(uint16(blockAlign))
		put16(uint16(bits))
	}
	put4("RIFF")
	put32(0) // decoder ignores the RIFF size
	put4("WAVE")
	if o.junk {
		put4("JUNK")
		put32(5)
		buf = append(buf, 1, 2, 3, 4, 5, 0) // odd size → pad byte
	}
	put4("fmt ")
	switch {
	case o.extensible:
		put32(40)
		fmtCore(wavFmtExtensible)
		put16(22)           // cbSize
		put16(uint16(bits)) // valid bits
		put32(3)            // channel mask
		put16(tag)          // SubFormat GUID leads with the real tag
		buf = append(buf, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71)
	case o.oddFmt:
		put32(17)
		fmtCore(tag)
		buf = append(buf, 0xEE) // stray byte; Go does NOT pad fmt to even
	default:
		put32(16)
		fmtCore(tag)
	}
	put4("data")
	put32(uint32(dataSize))
	for f := 0; f < frames; f++ {
		for c := 0; c < channels; c++ {
			buf = append(buf, encLE(sampleAt(f, c), bits, isFloat)...)
		}
		for p := 0; p < o.pad; p++ {
			buf = append(buf, 0x55)
		}
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAIFF(t testing.TB, path string, frames, channels, bits int) {
	t.Helper()
	blockAlign := channels * bits / 8
	dataSize := frames * blockAlign
	var buf []byte
	put4 := func(s string) { buf = append(buf, s...) }
	put32 := func(v uint32) { var b [4]byte; binary.BigEndian.PutUint32(b[:], v); buf = append(buf, b[:]...) }
	put16 := func(v uint16) { var b [2]byte; binary.BigEndian.PutUint16(b[:], v); buf = append(buf, b[:]...) }
	put4("FORM")
	put32(uint32(4 + 8 + 18 + 8 + 8 + dataSize))
	put4("AIFF")
	put4("COMM")
	put32(18)
	put16(uint16(channels))
	put32(uint32(frames))
	put16(uint16(bits))
	buf = append(buf, float64ToExtended80(48000)...)
	put4("SSND")
	put32(uint32(8 + dataSize))
	put32(0) // offset
	put32(0) // blockSize
	for f := 0; f < frames; f++ {
		for c := 0; c < channels; c++ {
			buf = append(buf, encBE(sampleAt(f, c), bits, false)...)
		}
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeAIFC writes a FORM/AIFC file with the given compression code
// (NONE/twos/sowt/fl32/FL32/fl64/FL64 or an unsupported one for negative tests).
func writeAIFC(t testing.TB, path string, frames, channels, bits int, comp string) {
	t.Helper()
	isFloat := comp == "fl32" || comp == "FL32" || comp == "fl64" || comp == "FL64"
	sowt := comp == "sowt"
	blockAlign := channels * bits / 8
	dataSize := frames * blockAlign
	var buf []byte
	put4 := func(s string) { buf = append(buf, s...) }
	put32 := func(v uint32) { var b [4]byte; binary.BigEndian.PutUint32(b[:], v); buf = append(buf, b[:]...) }
	put16 := func(v uint16) { var b [2]byte; binary.BigEndian.PutUint16(b[:], v); buf = append(buf, b[:]...) }
	put4("FORM")
	put32(uint32(4 + 8 + 22 + 8 + 8 + dataSize))
	put4("AIFC")
	put4("COMM")
	put32(22)
	put16(uint16(channels))
	put32(uint32(frames))
	put16(uint16(bits))
	buf = append(buf, float64ToExtended80(48000)...)
	put4(comp)
	put4("SSND")
	put32(uint32(8 + dataSize))
	put32(0) // offset
	put32(0) // blockSize
	for f := 0; f < frames; f++ {
		for c := 0; c < channels; c++ {
			if sowt {
				buf = append(buf, encLE(sampleAt(f, c), bits, false)...)
			} else {
				buf = append(buf, encBE(sampleAt(f, c), bits, isFloat)...)
			}
		}
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func encBE(v float32, bits int, isFloat bool) []byte {
	if isFloat {
		b := make([]byte, bits/8)
		if bits == 32 {
			binary.BigEndian.PutUint32(b, math.Float32bits(v))
		} else {
			binary.BigEndian.PutUint64(b, math.Float64bits(float64(v)))
		}
		return b
	}
	switch bits {
	case 8: // AIFF 8-bit is signed
		return []byte{byte(int8(v * 127))}
	case 16:
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, uint16(int16(v*32767)))
		return b
	case 24:
		iv := int32(v * 8388607)
		return []byte{byte(iv >> 16), byte(iv >> 8), byte(iv)}
	case 32:
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(int32(v*2147483647)))
		return b
	case 64: // int64 PCM: decoders emit silence — bytes are don't-care
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(float64(v)))
		return b
	}
	return nil
}

func float64ToExtended80(f float64) []byte {
	b := make([]byte, 10)
	if f == 0 {
		return b
	}
	mant := f
	exp := 16383
	for mant >= 2 {
		mant /= 2
		exp++
	}
	for mant < 1 {
		mant *= 2
		exp--
	}
	binary.BigEndian.PutUint16(b[0:2], uint16(exp))
	binary.BigEndian.PutUint64(b[2:10], uint64(mant*math.Pow(2, 63)))
	return b
}

func TestPCMDecodeAndSeek(t *testing.T) {
	const frames, ch = 4000, 2
	cases := []struct {
		name    string
		bits    int
		isFloat bool
		aiff    bool
		tol     float32
	}{
		{"wav8", 8, false, false, 2e-2},
		{"wav16", 16, false, false, 1e-3},
		{"wav24", 24, false, false, 1e-5},
		{"wav32", 32, false, false, 1e-6},
		{"wav32f", 32, true, false, 1e-6},
		{"wav64f", 64, true, false, 1e-6},
		{"aiff8", 8, false, true, 2e-2},
		{"aiff16", 16, false, true, 1e-3},
		{"aiff24", 24, false, true, 1e-5},
		{"aiff32", 32, false, true, 1e-6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if tc.aiff {
				writeAIFF(t, path, frames, ch, tc.bits)
			} else {
				writeWAV(t, path, frames, ch, tc.bits, tc.isFloat)
			}
			d, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer d.Close()
			if got := d.Format(); got.Channels != ch || got.SampleRate != 48000 {
				t.Fatalf("format = %+v", got)
			}
			if got := d.TotalFrames(); got != frames {
				t.Fatalf("TotalFrames = %d want %d", got, frames)
			}
			// Sequential decode from 0.
			out := make([]float32, 100*ch)
			n, err := d.ReadFrames(out)
			if err != nil || n != 100 {
				t.Fatalf("ReadFrames head: n=%d err=%v", n, err)
			}
			for f := 0; f < 100; f++ {
				for c := 0; c < ch; c++ {
					if diff := absf(out[f*ch+c] - sampleAt(f, c)); diff > tc.tol {
						t.Fatalf("head frame %d ch %d: got %v want %v (diff %v)", f, c, out[f*ch+c], sampleAt(f, c), diff)
					}
				}
			}
			// Sample-accurate seek to an arbitrary frame, first decoded sample must match.
			for _, k := range []int64{1, 999, 2500, 3999} {
				if err := d.SeekTo(k); err != nil {
					t.Fatalf("Seek(%d): %v", k, err)
				}
				if n, err := d.ReadFrames(out); err != nil || n == 0 {
					t.Fatalf("read after seek(%d): n=%d err=%v", k, n, err)
				}
				for c := 0; c < ch; c++ {
					if diff := absf(out[c] - sampleAt(int(k), c)); diff > tc.tol {
						t.Fatalf("seek(%d) ch %d: got %v want %v", k, c, out[c], sampleAt(int(k), c))
					}
				}
			}
			// Seek to EOF => io.EOF.
			if err := d.SeekTo(frames); err != nil {
				t.Fatalf("seek EOF: %v", err)
			}
			if _, err := d.ReadFrames(out); err != io.EOF {
				t.Fatalf("read at EOF: err=%v want EOF", err)
			}
		})
	}
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// readHead decodes the first n frames and checks them against sampleAt.
func readHead(t *testing.T, path string, frames, ch int, tol float32) {
	t.Helper()
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if got := d.TotalFrames(); got != int64(frames) {
		t.Fatalf("TotalFrames = %d want %d", got, frames)
	}
	n := 200
	if n > frames {
		n = frames
	}
	out := make([]float32, n*ch)
	got, err := d.ReadFrames(out)
	if err != nil || got != n {
		t.Fatalf("ReadFrames: n=%d err=%v", got, err)
	}
	for f := 0; f < n; f++ {
		for c := 0; c < ch; c++ {
			if diff := absf(out[f*ch+c] - sampleAt(f, c)); diff > tol {
				t.Fatalf("frame %d ch %d: got %v want %v", f, c, out[f*ch+c], sampleAt(f, c))
			}
		}
	}
}

func TestWAVVariantsDecode(t *testing.T) {
	const frames, ch = 500, 2
	cases := []struct {
		name    string
		bits    int
		isFloat bool
		tol     float32
		o       wavX
	}{
		{"ext16", 16, false, 1e-3, wavX{extensible: true}},
		{"ext32f", 32, true, 1e-6, wavX{extensible: true}},
		{"pad24", 24, false, 1e-5, wavX{pad: 3}},
		{"junkOddFmt16", 16, false, 1e-3, wavX{junk: true, oddFmt: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name+".wav")
			writeWAVX(t, path, frames, ch, tc.bits, tc.isFloat, tc.o)
			readHead(t, path, frames, ch, tc.tol)
		})
	}
}

func TestAIFCVariantsDecode(t *testing.T) {
	const frames, ch = 500, 2
	cases := []struct {
		name string
		bits int
		comp string
		tol  float32
	}{
		{"sowt16", 16, "sowt", 1e-3},
		{"twos24", 24, "twos", 1e-5},
		{"fl32", 32, "fl32", 1e-6},
		{"fl64", 64, "fl64", 1e-6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name+".aifc")
			writeAIFC(t, path, frames, ch, tc.bits, tc.comp)
			readHead(t, path, frames, ch, tc.tol)
		})
	}
	t.Run("unsupportedComp", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.aifc")
		writeAIFC(t, path, 8, 1, 16, "ima4")
		if _, err := Open(path); err == nil {
			t.Fatal("Open(ima4) succeeded, want error")
		}
	})
}

// ── decode benchmarks (Go container; under -tags zigdsp the sample kernel is
// already Zig — untagged run = pure Go baseline) ─────────────────────────────

const benchFrames = 30 * 48000 // 30s stereo

func benchDecode(b *testing.B, d Decoder, bytes int64) {
	b.Helper()
	dst := make([]float32, 4096*2)
	b.SetBytes(bytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := d.SeekTo(0); err != nil {
			b.Fatal(err)
		}
		for {
			if _, err := d.ReadFrames(dst); err != nil {
				break
			}
		}
	}
}

func BenchmarkWAVDecodeGo(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bench.wav")
	writeWAV(b, path, benchFrames, 2, 16, false)
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	d, err := newWAVDecoder(f)
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	benchDecode(b, d, benchFrames*4)
}

func BenchmarkAIFFDecodeGo(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bench.aiff")
	writeAIFF(b, path, benchFrames, 2, 16)
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	d, err := newAIFFDecoder(f)
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	benchDecode(b, d, benchFrames*4)
}
