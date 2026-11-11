//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
	"testing"
	"time"

	"rave.page/mate/internal/medialink"
)

// TestCaptureLive captures from a real PipeWire node (set RPW_TEST_NODE, e.g. a
// `gst-launch-1.0 videotestsrc ! pipewiresink mode=provide` producer). Skipped without it, so it
// never runs in CI/the build container (no PipeWire session). Built as a test binary in the
// container and run on the sender box.
func TestCaptureLive(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" {
		t.Skip("set RPW_TEST_NODE to a live PipeWire video node name")
	}
	if !captureAvailable() {
		t.Skip("libpipewire-0.3 not available")
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatalf("openCapture(%q): %v", node, err)
	}
	defer func() { _ = src.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	const want = 60
	var bytes int
	start := time.Now()
	for i := 0; i < want; i++ {
		f, err := src.Next(ctx)
		if err != nil {
			t.Fatalf("Next after %d frames: %v", i, err)
		}
		if f.Codec != medialink.CodecNRGBA || f.Kind != medialink.KindVideo {
			t.Fatalf("frame %d: kind=%d codec=%d", i, f.Kind, f.Codec)
		}
		if len(f.Payload) == 0 {
			t.Fatalf("frame %d: empty payload", i)
		}
		nonzero := false
		for _, b := range f.Payload[:1024] {
			if b != 0 {
				nonzero = true
				break
			}
		}
		if !nonzero {
			t.Fatalf("frame %d: all-zero pixels (capture not delivering real data)", i)
		}
		bytes = len(f.Payload)
	}
	fps := float64(want) / time.Since(start).Seconds()
	t.Logf("captured %d frames, %d bytes each, ~%.1f fps", want, bytes, fps)
}
