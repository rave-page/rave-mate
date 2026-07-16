package audio

import (
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
