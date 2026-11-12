//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestVAEncodeDump drives the zero-copy VA capture+encode path (set RPW_VA_ENC=1): PipeWire dmabuf
// -> VA import -> VPP NV12 -> H.264 EncSlice, writing the Annex-B AUs to RPW_H264_OUT. The driver
// script then decodes the file with ffmpeg and checks the corner pattern. Reports encode fps.
func TestVAEncodeDump(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	out := os.Getenv("RPW_H264_OUT")
	if node == "" || out == "" || os.Getenv("RPW_VA_ENC") == "" {
		t.Skip("set RPW_TEST_NODE, RPW_H264_OUT, RPW_VA_ENC")
	}
	if !captureAvailable() {
		t.Skip("libpipewire not available")
	}
	n := 180
	if v := os.Getenv("RPW_H264_FRAMES"); v != "" {
		if x, err := strconv.Atoi(v); err == nil {
			n = x
		}
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var bytes, keyish int
	start := time.Now()
	for i := 0; i < n; i++ {
		fr, err := src.Next(ctx)
		if err != nil {
			t.Fatalf("Next after %d frames: %v", i, err)
		}
		if _, err := f.Write(fr.Payload); err != nil {
			t.Fatal(err)
		}
		bytes += len(fr.Payload)
		if len(fr.Payload) > 20000 { // crude: IDR AUs are much larger
			keyish++
		}
	}
	dur := time.Since(start)
	t.Logf("VA-ENC %d AUs in %v (~%.1f fps), %d bytes (avg %d), big(IDR-ish)=%d -> %s",
		n, dur.Round(time.Millisecond), float64(n)/dur.Seconds(), bytes, bytes/n, keyish, out)
	if float64(n)/dur.Seconds() < 30 {
		t.Errorf("encode fps too low: %.1f", float64(n)/dur.Seconds())
	}
}
