//go:build manual

// On-file decode+seek verification against the user's real library (read-only). No pure-Go
// encoders exist for the compressed formats, so this exercises the wrapper decoders on actual
// FLAC/MP3/WAV/OGG/AIFF files and reports decode throughput (xRT), seek latency, and — for
// lossless formats — sample-accuracy of the seek vs a full-decode reference.
//
//   GOWORK=off go test -tags manual -run TestOnFile -v ./internal/audio/
//
// Files are found under %USERPROFILE%/Music (override with RAVE_AUDIO_TESTDIR). Nothing is
// written or modified.

package audio

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDir() string {
	if d := os.Getenv("RAVE_AUDIO_TESTDIR"); d != "" {
		return d
	}
	return filepath.Join(os.Getenv("USERPROFILE"), "Music")
}

// firstFile returns the first file matching any of exts (case-insensitive), >200KB, under dir.
func firstFile(dir string, exts ...string) string {
	want := map[string]bool{}
	for _, e := range exts {
		want["."+strings.ToLower(e)] = true
	}
	var found string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || found != "" {
			return nil
		}
		if want[strings.ToLower(filepath.Ext(p))] && fi.Size() > 200<<10 {
			found = p
			return io.EOF // stop
		}
		return nil
	})
	return found
}

// decodeWindow decodes up to maxFrames from the current position into a RAM buffer, returning it
// + the wall time spent. Used for both the xRT measurement and the seek reference.
func decodeWindow(d Decoder, maxFrames int64) ([]float32, time.Duration) {
	ch := d.Format().Channels
	buf := make([]float32, 8192*ch)
	var out []float32
	start := time.Now()
	var got int64
	for got < maxFrames {
		n, err := d.ReadFrames(buf)
		if n > 0 {
			out = append(out, buf[:n*ch]...)
			got += int64(n)
		}
		if err == io.EOF || n == 0 {
			break
		}
		if err != nil {
			break
		}
	}
	return out, time.Since(start)
}

func TestOnFile(t *testing.T) {
	dir := testDir()
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("no test dir %s", dir)
	}
	type fmtCase struct {
		name     string
		exts     []string
		lossless bool
	}
	cases := []fmtCase{
		{"flac", []string{"flac"}, true},
		{"wav", []string{"wav", "wave"}, true},
		{"aiff", []string{"aif", "aiff", "aifc"}, true},
		{"mp3", []string{"mp3"}, false},
		{"ogg", []string{"ogg", "oga"}, false},
	}
	const refSeconds = 30 // bound the reference window (huge recordings => don't decode hours)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := firstFile(dir, c.exts...)
			if path == "" {
				t.Skipf("no %s file under %s", c.name, dir)
			}
			fi, _ := os.Stat(path)
			t.Logf("file: %s (%.1f MiB)", filepath.Base(path), float64(fi.Size())/(1<<20))

			// 1) Open + format.
			d, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer d.Close()
			f := d.Format()
			total := d.TotalFrames()
			t.Logf("format: %d Hz, %d ch, total=%d frames (%s)", f.SampleRate, f.Channels, total, f.FrameToDuration(total).Round(time.Millisecond))
			if f.SampleRate <= 0 || f.Channels <= 0 {
				t.Fatalf("bad format %+v", f)
			}

			// 2) Decode a bounded window -> xRT (decode throughput vs realtime).
			refFrames := int64(refSeconds) * int64(f.SampleRate)
			if total > 0 && total < refFrames {
				refFrames = total
			}
			ref, dt := decodeWindow(d, refFrames)
			decoded := int64(len(ref) / f.Channels)
			if decoded == 0 {
				t.Fatalf("decoded 0 frames")
			}
			audioSec := f.FrameToSeconds(decoded)
			xrt := audioSec / dt.Seconds()
			t.Logf("decode: %d frames (%.1fs audio) in %s => %.0fx realtime", decoded, audioSec, dt.Round(time.Millisecond), xrt)

			// 3) Seek latency + accuracy at several in-window positions.
			targets := []int64{1 * int64(f.SampleRate), decoded / 3, decoded / 2, decoded - int64(f.SampleRate)}
			for _, k := range targets {
				if k <= 0 || k >= decoded {
					continue
				}
				st := time.Now()
				if err := d.SeekTo(k); err != nil {
					t.Fatalf("SeekTo(%d): %v", k, err)
				}
				win := make([]float32, 256*f.Channels)
				n, err := d.ReadFrames(win)
				lat := time.Since(st)
				if err != nil && err != io.EOF {
					t.Fatalf("read after seek(%d): %v", k, err)
				}
				if n == 0 {
					t.Fatalf("seek(%d) read 0 frames", k)
				}
				acc := "n/a (lossy)"
				if c.lossless {
					// Lossless: the decoded sample at frame k must equal the reference exactly.
					var maxDiff float32
					for i := 0; i < n && (k+int64(i)) < decoded; i++ {
						for ch := 0; ch < f.Channels; ch++ {
							ri := (k+int64(i))*int64(f.Channels) + int64(ch)
							diff := absf(win[i*f.Channels+ch] - ref[ri])
							if diff > maxDiff {
								maxDiff = diff
							}
						}
					}
					if maxDiff > 1e-4 {
						t.Errorf("seek(%d): sample mismatch vs reference, maxDiff=%v (seek not sample-accurate)", k, maxDiff)
					}
					acc = "exact"
				}
				t.Logf("seek->%d (%.1fs): %s, accuracy=%s", k, f.FrameToSeconds(k), lat.Round(10*time.Microsecond), acc)
			}

			// 4) Deep seek far past the reference window (proves the seektable-less-FLAC 15s
			// beep-freeze is gone for a real mid-file jump). Latency only (no reference to compare).
			if total > int64(5*60)*int64(f.SampleRate) {
				deep := total / 2
				st := time.Now()
				if err := d.SeekTo(deep); err != nil {
					t.Fatalf("deep SeekTo(%d): %v", deep, err)
				}
				win := make([]float32, 256*f.Channels)
				n, err := d.ReadFrames(win)
				lat := time.Since(st)
				if err != nil && err != io.EOF {
					t.Fatalf("deep read: %v", err)
				}
				if n == 0 {
					t.Fatalf("deep seek read 0")
				}
				t.Logf("DEEP seek->%d (%s, mid-file): %s [%d frames read]", deep, f.FrameToDuration(deep).Round(time.Second), lat.Round(10*time.Microsecond), n)
			}
		})
	}
}
