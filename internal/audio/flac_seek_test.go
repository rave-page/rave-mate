package audio

import (
	"bytes"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// testdata/seekless.flac: 4 s 44.1 kHz stereo sine, ffmpeg-encoded = STREAMINFO+VORBIS_COMMENT+
// PADDING, NO SEEKTABLE - byte-identical block layout to a real direct capture. Pins the
// binary-search seek: mewkiz Stream.Seek on such a file decodes the ENTIRE file to build a
// table before the first seek returns (~47 s on an hour-long 485 MB set).
const seeklessFixture = "testdata/seekless.flac"

func openSeekless(t *testing.T) Decoder {
	t.Helper()
	d, err := Open(seeklessFixture)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// decodeAll returns the fixture's full interleaved PCM as the seek reference.
func decodeAll(t *testing.T, d Decoder) []float32 {
	t.Helper()
	ch := d.Format().Channels
	buf := make([]float32, 4096*ch)
	var out []float32
	for {
		n, err := d.ReadFrames(buf)
		if n > 0 {
			out = append(out, buf[:n*ch]...)
		}
		if err == io.EOF || n == 0 {
			return out
		}
		if err != nil {
			t.Fatalf("reference decode: %v", err)
		}
	}
}

func TestFLACSeekNoSeektableExact(t *testing.T) {
	ref := decodeAll(t, openSeekless(t))
	d := openSeekless(t)
	f := d.Format()
	total := int64(len(ref) / f.Channels)
	if got := d.TotalFrames(); got != total {
		t.Fatalf("TotalFrames %d, decoded %d", got, total)
	}
	// out-of-order targets: forward, deep, BACKWARD, frame-interior, first, near-end
	targets := []int64{total / 2, total - 300, 1000, total / 3, 0, 12345, total - 1}
	win := make([]float32, 256*f.Channels)
	for _, k := range targets {
		if err := d.SeekTo(k); err != nil {
			t.Fatalf("SeekTo(%d): %v", k, err)
		}
		n, err := d.ReadFrames(win)
		if err != nil && err != io.EOF {
			t.Fatalf("read after seek(%d): %v", k, err)
		}
		if n == 0 {
			t.Fatalf("seek(%d): 0 frames", k)
		}
		for i := 0; i < n && k+int64(i) < total; i++ {
			for c := 0; c < f.Channels; c++ {
				want := ref[(k+int64(i))*int64(f.Channels)+int64(c)]
				got := win[i*f.Channels+c]
				if math.Abs(float64(got-want)) > 1e-7 {
					t.Fatalf("seek(%d): sample %d ch %d = %v want %v (not sample-accurate)", k, i, c, got, want)
				}
			}
		}
	}
	// past-end clamps and EOFs cleanly
	if err := d.SeekTo(total + 500); err != nil {
		t.Fatalf("SeekTo past end: %v", err)
	}
	if n, _ := d.ReadFrames(win); n != 0 {
		t.Fatalf("read past end returned %d frames", n)
	}
}

func TestFLACEnsureSeekTable(t *testing.T) {
	src, err := os.ReadFile(filepath.FromSlash(seeklessFixture))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "cap.flac")
	if err := os.WriteFile(p, src, 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	refDec, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	ref := decodeAll(t, refDec)
	_ = refDec.Close()

	wrote, err := FLACEnsureSeekTable(p)
	if err != nil || !wrote {
		t.Fatalf("ensure: wrote=%v err=%v", wrote, err)
	}
	// layout now carries a SEEKTABLE; size + audio bytes untouched; mtime restored
	fi, _ := os.Stat(p)
	if fi.Size() != int64(len(src)) {
		t.Fatalf("file size changed: %d -> %d", len(src), fi.Size())
	}
	if !fi.ModTime().Equal(past) {
		t.Fatalf("mtime not restored: %v", fi.ModTime())
	}
	f, _ := os.Open(p)
	lay, err := flacLayout(f)
	_ = f.Close()
	if err != nil || !lay.hasSeekTable {
		t.Fatalf("no SEEKTABLE after ensure (err=%v)", err)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(after[lay.dataStart:], src[lay.dataStart:]) {
		t.Fatal("audio bytes were modified")
	}

	// decoder on the rewritten file: identical PCM + sample-exact anchored seeks
	d, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	got := decodeAll(t, d)
	if len(got) != len(ref) {
		t.Fatalf("PCM length changed: %d -> %d", len(ref), len(got))
	}
	for i := range got {
		if got[i] != ref[i] {
			t.Fatalf("PCM diverges at %d", i)
		}
	}
	ch := d.Format().Channels
	k := d.TotalFrames() * 3 / 4
	if err := d.SeekTo(k); err != nil {
		t.Fatal(err)
	}
	win := make([]float32, 64*ch)
	n, _ := d.ReadFrames(win)
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			if win[i*ch+c] != ref[(k+int64(i))*int64(ch)+int64(c)] {
				t.Fatalf("anchored seek not exact at frame %d", k+int64(i))
			}
		}
	}

	// idempotent
	if wrote2, err2 := FLACEnsureSeekTable(p); err2 != nil || wrote2 {
		t.Fatalf("second ensure: wrote=%v err=%v", wrote2, err2)
	}
}

func TestFLACEnsureSeekTableTinyPaddingRefuses(t *testing.T) {
	src, err := os.ReadFile(filepath.FromSlash(seeklessFixture))
	if err != nil {
		t.Fatal(err)
	}
	// shrink the padding below table minimum by rewriting the metadata region: keep
	// STREAMINFO + VORBIS_COMMENT, replace PADDING(8192) with PADDING(16) and shift audio.
	f, _ := os.Open(filepath.FromSlash(seeklessFixture))
	lay, err := flacLayout(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	out = append(out, src[:lay.padOff]...)
	hdr := []byte{1, 0, 0, 16}
	if lay.padWasLast {
		hdr[0] |= 0x80
	}
	out = append(out, hdr...)
	out = append(out, make([]byte, 16)...)
	out = append(out, src[lay.dataStart:]...)
	p := filepath.Join(t.TempDir(), "tiny.flac")
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	wrote, err := FLACEnsureSeekTable(p)
	if err != nil || wrote {
		t.Fatalf("tiny padding: wrote=%v err=%v (must refuse)", wrote, err)
	}
	afterB, _ := os.ReadFile(p)
	if !bytes.Equal(before, afterB) {
		t.Fatal("file modified despite refusal")
	}
}

// countingRSC counts bytes read - the no-full-scan regression gate.
type countingRSC struct {
	*os.File
	n int64
}

func (c *countingRSC) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	c.n += int64(n)
	return n, err
}

func TestFLACSeekReadsBounded(t *testing.T) {
	// A meaningful bound needs a file that dwarfs the probe windows AND the 64 KB bufio
	// prefill, so generate a 60 s sine at runtime (~1.5 MB). Skip without ffmpeg (the
	// committed-fixture exactness test above always runs).
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	// Test-scale the terminal window so the binary search must engage (production: 512 KB).
	old := flacSeekWindow
	flacSeekWindow = 32 << 10
	defer func() { flacSeekWindow = old }()
	p := filepath.Join(t.TempDir(), "long.flac")
	out, err := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=60",
		"-ac", "2", "-sample_fmt", "s16", p).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}

	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := f.Stat()
	c := &countingRSC{File: f}
	d, err := newFLACDecoder(c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	opened := c.n
	if err := d.SeekTo(d.TotalFrames() * 2 / 3); err != nil {
		t.Fatal(err)
	}
	win := make([]float32, 256*d.Format().Channels)
	if n, rerr := d.ReadFrames(win); n == 0 {
		t.Fatalf("read after bounded seek: n=0 err=%v", rerr)
	}
	seekBytes := c.n - opened
	// log-n probes of ≤512 KB... on 1.5 MB the search does ~2 probes + a ≤512 KB linear tail +
	// the 64 KB bufio prefill. A full scan reads ~100% - gate at 2/3.
	if limit := fi.Size() * 2 / 3; seekBytes > limit {
		t.Fatalf("seek read %d of %d bytes - smells like a full-file scan (limit %d)", seekBytes, fi.Size(), limit)
	}
	t.Logf("seek read %d of %d bytes (%.0f%%)", seekBytes, fi.Size(), 100*float64(seekBytes)/float64(fi.Size()))
}
