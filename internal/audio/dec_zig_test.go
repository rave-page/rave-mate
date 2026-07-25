//go:build zigdsp && cgo

package audio

import (
	"bytes"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/zignative"
)

// memRSC adapts a byte slice to io.ReadSeekCloser so both decoders run over
// identical bytes with no file I/O.
type memRSC struct{ *bytes.Reader }

func (memRSC) Close() error { return nil }

func openMem(b []byte) io.ReadSeekCloser { return memRSC{bytes.NewReader(b)} }

func goOpenKind(raw []byte, kind string) (Decoder, error) {
	if kind == "wav" {
		return newWAVDecoder(openMem(raw))
	}
	return newAIFFDecoder(openMem(raw))
}

func zigOpenKind(raw []byte, kind string) (Decoder, error) {
	var h *zignative.PCMDec
	if kind == "wav" {
		h = zignative.NewWAVDec()
	} else {
		h = zignative.NewAIFFDec()
	}
	return newZigPCMDecoder(openMem(raw), h)
}

// assertOpenParity opens both decoders; accept/reject must agree. Returns the
// pair when both succeeded (caller closes).
func assertOpenParity(t *testing.T, raw []byte, kind string) (gd, zd Decoder, ok bool) {
	t.Helper()
	gd, ge := goOpenKind(raw, kind)
	zd, ze := zigOpenKind(raw, kind)
	if (ge == nil) != (ze == nil) {
		t.Fatalf("open parity: go=%v zig=%v", ge, ze)
	}
	if ge != nil {
		return nil, nil, false
	}
	return gd, zd, true
}

// readParity compares one ReadFrames call bit-exactly (n, err class, samples).
func readParity(t *testing.T, gd, zd Decoder, dg, dz []float32, ctx string) (int, error) {
	t.Helper()
	ng, eg := gd.ReadFrames(dg)
	nz, ez := zd.ReadFrames(dz)
	if ng != nz || (eg == nil) != (ez == nil) || (eg == io.EOF) != (ez == io.EOF) {
		t.Fatalf("%s: read parity: go=(%d,%v) zig=(%d,%v)", ctx, ng, eg, nz, ez)
	}
	ch := gd.Format().Channels
	for i := 0; i < ng*ch; i++ {
		if math.Float32bits(dg[i]) != math.Float32bits(dz[i]) {
			t.Fatalf("%s: sample %d: go=%08x zig=%08x", ctx, i,
				math.Float32bits(dg[i]), math.Float32bits(dz[i]))
		}
	}
	return ng, eg
}

// assertDecParity: metadata + full sequential decode + seek matrix, bit-exact.
func assertDecParity(t *testing.T, raw []byte, kind string) {
	t.Helper()
	gd, zd, ok := assertOpenParity(t, raw, kind)
	if !ok {
		return
	}
	defer gd.Close()
	defer zd.Close()
	if gd.Format() != zd.Format() {
		t.Fatalf("format: go=%+v zig=%+v", gd.Format(), zd.Format())
	}
	if gd.TotalFrames() != zd.TotalFrames() {
		t.Fatalf("total: go=%d zig=%d", gd.TotalFrames(), zd.TotalFrames())
	}
	ch := gd.Format().Channels
	total := gd.TotalFrames()
	// sequential decode in odd-sized blocks until EOF
	dg := make([]float32, 371*ch)
	dz := make([]float32, 371*ch)
	for i := 0; ; i++ {
		if i > int(total)/371+16 {
			t.Fatal("sequential read did not terminate")
		}
		if _, err := readParity(t, gd, zd, dg, dz, "seq"); err == io.EOF {
			break
		}
	}
	// seek matrix incl. negative, EOF, past-EOF
	for _, k := range []int64{-5, 0, 1, total / 3, total - 1, total, total + 7} {
		eg := gd.SeekTo(k)
		ez := zd.SeekTo(k)
		if (eg == nil) != (ez == nil) {
			t.Fatalf("seek(%d) parity: go=%v zig=%v", k, eg, ez)
		}
		readParity(t, gd, zd, dg, dz, "postseek")
	}
	// interleaved: read, seek back, read
	if err := gd.SeekTo(total / 2); err != nil {
		t.Fatal(err)
	}
	if err := zd.SeekTo(total / 2); err != nil {
		t.Fatal(err)
	}
	readParity(t, gd, zd, dg, dz, "mid1")
	if err := gd.SeekTo(3); err != nil {
		t.Fatal(err)
	}
	if err := zd.SeekTo(3); err != nil {
		t.Fatal(err)
	}
	readParity(t, gd, zd, dg, dz, "mid2")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestZigWAVDecParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	dir := t.TempDir()
	const frames = 1777
	type fc struct {
		bits    int
		isFloat bool
	}
	for _, f := range []fc{{8, false}, {16, false}, {24, false}, {32, false}, {32, true}, {64, true}} {
		for _, ch := range []int{1, 2, 6} {
			p := filepath.Join(dir, "w")
			writeWAV(t, p, frames, ch, f.bits, f.isFloat)
			assertDecParity(t, mustRead(t, p), "wav")
		}
	}
	// container variants: extensible, padded block-align, junk + odd fmt
	for _, tc := range []struct {
		name    string
		bits    int
		isFloat bool
		o       wavX
	}{
		{"ext16", 16, false, wavX{extensible: true}},
		{"ext32f", 32, true, wavX{extensible: true}},
		{"ext8", 8, false, wavX{extensible: true}},
		{"pad24", 24, false, wavX{pad: 3}},
		{"pad16", 16, false, wavX{pad: 1}},
		{"junkOddFmt16", 16, false, wavX{junk: true, oddFmt: true}},
		{"junkPad32f", 32, true, wavX{junk: true, pad: 2}},
	} {
		p := filepath.Join(dir, tc.name)
		writeWAVX(t, p, 700, 2, tc.bits, tc.isFloat, tc.o)
		assertDecParity(t, mustRead(t, p), "wav")
	}
	// truncated data: total promises more than the file holds
	p := filepath.Join(dir, "trunc")
	writeWAV(t, p, frames, 2, 16, false)
	raw := mustRead(t, p)
	assertDecParity(t, raw[:len(raw)-501], "wav")
}

func TestZigAIFFDecParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	dir := t.TempDir()
	const frames = 1777
	for _, bits := range []int{8, 16, 24, 32} {
		for _, ch := range []int{1, 2} {
			p := filepath.Join(dir, "a")
			writeAIFF(t, p, frames, ch, bits)
			assertDecParity(t, mustRead(t, p), "aiff")
		}
	}
	// AIFC compression variants (incl. the sowt-8-bit-decoded-unsigned quirk
	// and int64 PCM decoded as silence)
	for _, tc := range []struct {
		bits int
		comp string
	}{
		{16, "NONE"}, {24, "twos"}, {16, "sowt"}, {8, "sowt"},
		{32, "fl32"}, {32, "FL32"}, {64, "fl64"}, {64, "FL64"}, {64, "NONE"},
	} {
		p := filepath.Join(dir, "c")
		writeAIFC(t, p, 700, 2, tc.bits, tc.comp)
		assertDecParity(t, mustRead(t, p), "aiff")
	}
	// truncated data (COMM total > actual SSND bytes)
	p := filepath.Join(dir, "trunc")
	writeAIFF(t, p, frames, 2, 16)
	raw := mustRead(t, p)
	assertDecParity(t, raw[:len(raw)-501], "aiff")
}

func TestZigDecMalformedParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	dir := t.TempDir()
	wp := filepath.Join(dir, "w.wav")
	writeWAV(t, wp, 64, 2, 16, false)
	wav := mustRead(t, wp)
	ap := filepath.Join(dir, "a.aiff")
	writeAIFF(t, ap, 64, 2, 16)
	aiff := mustRead(t, ap)

	// truncations across every parser state
	for _, n := range []int{0, 3, 11, 12, 19, 20, 27, 43} {
		assertDecParity(t, wav[:n], "wav")
		assertDecParity(t, aiff[:n], "aiff")
	}
	// targeted corruption (mut offset→value); both sides must agree
	mut := func(base []byte, off int, v byte) []byte {
		b := append([]byte(nil), base...)
		b[off] = v
		return b
	}
	cases := []struct {
		name string
		raw  []byte
		kind string
	}{
		{"wav-bad-magic", mut(wav, 9, 'X'), "wav"},
		{"wav-alaw-tag", mut(wav, 20, 6), "wav"},
		{"wav-depth-12", mut(wav, 34, 12), "wav"},
		{"wav-float-depth-16", mut(mut(wav, 20, 3), 34, 16), "wav"},
		{"wav-zero-ch", mut(wav, 22, 0), "wav"},
		{"wav-small-blockalign", mut(wav, 32, 1), "wav"},
		{"wav-data-first", []byte("RIFF\x00\x00\x00\x00WAVEdata\x04\x00\x00\x00abcd"), "wav"},
		{"aiff-bad-form", mut(aiff, 10, 'X'), "aiff"},
		{"aiff-zero-ch", mut(mut(aiff, 20, 0), 21, 0), "aiff"},
		{"aiff-neg-ch", mut(mut(aiff, 20, 0xFF), 21, 0xFF), "aiff"},
		{"aiff-bad-depth", mut(mut(aiff, 26, 0), 27, 11), "aiff"},
		{"aiff-zero-rate", mut(mut(aiff, 28, 0), 29, 0), "aiff"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assertDecParity(t, c.raw, c.kind) })
	}
	// unsupported AIFC compression
	cp := filepath.Join(dir, "c.aifc")
	writeAIFC(t, cp, 8, 1, 16, "ima4")
	assertDecParity(t, mustRead(t, cp), "aiff")
	// dispatch-level: Open on malformed wav must still error (Go fallback path)
	bp := filepath.Join(dir, "bad.wav")
	if err := os.WriteFile(bp, mut(wav, 20, 6), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bp); err == nil {
		t.Fatal("Open(alaw wav) succeeded, want error")
	}
}

// Fuzz-ish: random byte flips + random truncations; accept/reject and, when
// both accept, metadata + first block must match. Neither side may crash.
func TestZigDecFuzzParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	dir := t.TempDir()
	wp := filepath.Join(dir, "w.wav")
	writeWAVX(t, wp, 96, 2, 24, false, wavX{junk: true})
	ap := filepath.Join(dir, "a.aifc")
	writeAIFC(t, ap, 96, 2, 16, "NONE")
	bases := []struct {
		raw  []byte
		kind string
	}{{mustRead(t, wp), "wav"}, {mustRead(t, ap), "aiff"}}
	rng := rand.New(rand.NewSource(7))
	for _, base := range bases {
		buf := make([]byte, len(base.raw))
		for i := 0; i < 400; i++ {
			copy(buf, base.raw)
			m := buf
			if rng.Intn(4) == 0 {
				m = buf[:rng.Intn(len(buf)+1)]
			}
			if len(m) > 0 {
				for j := 0; j < 1+rng.Intn(16); j++ {
					m[rng.Intn(len(m))] = byte(rng.Intn(256))
				}
			}
			gd, ge := goOpenKind(m, base.kind)
			zd, ze := zigOpenKind(m, base.kind)
			if (ge == nil) != (ze == nil) {
				t.Fatalf("iter %d: open parity: go=%v zig=%v", i, ge, ze)
			}
			if ge != nil {
				continue
			}
			if gd.Format() != zd.Format() || gd.TotalFrames() != zd.TotalFrames() {
				t.Fatalf("iter %d: meta: go=%+v/%d zig=%+v/%d", i,
					gd.Format(), gd.TotalFrames(), zd.Format(), zd.TotalFrames())
			}
			dg := make([]float32, 2048)
			dz := make([]float32, 2048)
			readParity(t, gd, zd, dg, dz, "fuzz")
			gd.Close()
			zd.Close()
		}
	}
}

// Dispatch sanity: Open routes to the Zig decoder when linked.
func TestZigDecDispatch(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	p := filepath.Join(t.TempDir(), "d.wav")
	writeWAV(t, p, 100, 2, 16, false)
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, ok := d.(*zigPCMDecoder); !ok {
		t.Fatalf("Open returned %T, want *zigPCMDecoder", d)
	}
}

func BenchmarkWAVDecodeZig(b *testing.B) {
	if !zignative.Available() {
		b.Fatal("zignative not available")
	}
	path := filepath.Join(b.TempDir(), "bench.wav")
	writeWAV(b, path, benchFrames, 2, 16, false)
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	d, err := newZigPCMDecoder(f, zignative.NewWAVDec())
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	benchDecode(b, d, benchFrames*4)
}

func BenchmarkAIFFDecodeZig(b *testing.B) {
	if !zignative.Available() {
		b.Fatal("zignative not available")
	}
	path := filepath.Join(b.TempDir(), "bench.aiff")
	writeAIFF(b, path, benchFrames, 2, 16)
	f, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	d, err := newZigPCMDecoder(f, zignative.NewAIFFDec())
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	benchDecode(b, d, benchFrames*4)
}
