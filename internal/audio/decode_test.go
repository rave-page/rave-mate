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

func writeWAV(t *testing.T, path string, frames, channels, bits int, isFloat bool) {
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

func writeAIFF(t *testing.T, path string, frames, channels, bits int) {
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
			buf = append(buf, encBE(sampleAt(f, c), bits)...)
		}
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func encBE(v float32, bits int) []byte {
	switch bits {
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
		{"wav16", 16, false, false, 1e-3},
		{"wav24", 24, false, false, 1e-5},
		{"wav32f", 32, true, false, 1e-6},
		{"aiff16", 16, false, true, 1e-3},
		{"aiff24", 24, false, true, 1e-5},
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
