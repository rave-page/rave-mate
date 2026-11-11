//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediapipe"
	"rave.page/mate/internal/mediatools"
)

// TestCaptureEncodeE2E drives the real Linux-sender data path on one box: a PipeWire node
// (RPW_TEST_NODE) -> pwcapture.Source -> the mediapipe VA-API encode child -> Annex-B H.264. It is
// the single-box integration the end-to-end unit asks for (the network transport beyond the encoder
// is codec-agnostic + proven in P4). Skipped without a live node. Built in the box, run on sus.
func TestCaptureEncodeE2E(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" {
		t.Skip("set RPW_TEST_NODE to a live PipeWire video node name")
	}
	if !captureAvailable() {
		t.Skip("libpipewire-0.3 not available")
	}
	if _, ok := mediatools.Resolve("ffmpeg"); !ok {
		t.Skip("ffmpeg not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	nodes, ok := DiscoverNodes(ctx)
	if !ok {
		t.Skip("pw-dump unavailable")
	}
	var w, h int
	var fps float64
	for _, n := range nodes {
		if n.Name == node {
			w, h, fps = n.Width, n.Height, n.FPS
		}
	}
	if w == 0 || h == 0 {
		t.Fatalf("node %q not discovered or no dims", node)
	}
	if fps <= 0 {
		fps = 60
	}

	src, err := openCapture(ctx, nil, node)
	if err != nil {
		t.Fatalf("openCapture: %v", err)
	}
	encFac, _ := mediapipe.Factories(logbus.New(64))
	spec := medialink.EncodeSpec{
		Encoder: "h264_vaapi", Codec: medialink.CodecH264,
		Width: w, Height: h, FPS: fps, DeviceIndex: -1,
	}
	enc, err := encFac(ctx, spec, src) // wraps the capture with the VA-API encode child; closing enc closes src
	if err != nil {
		_ = src.Close()
		t.Fatalf("encoder factory: %v", err)
	}
	defer func() { _ = enc.Close() }()

	const want = 180 // ~3s at 60fps
	var frames, keyframes, configFrames, bytes int
	start := time.Now()
	for frames < want {
		f, err := enc.Next(ctx)
		if err != nil {
			t.Fatalf("encoded Next after %d frames: %v", frames, err)
		}
		if f.Codec != medialink.CodecH264 {
			t.Fatalf("frame %d codec = %d, want H264", frames, f.Codec)
		}
		if f.Flags&medialink.FlagKeyframe != 0 {
			keyframes++
		}
		if f.Flags&medialink.FlagConfig != 0 {
			configFrames++
		}
		bytes += len(f.Payload)
		if f.Release != nil {
			f.Release()
		}
		frames++
	}
	dur := time.Since(start)
	encFPS := float64(frames) / dur.Seconds()
	t.Logf("E2E %dx%d@%.0f: encoded %d H.264 frames in %v (~%.1f fps), %d keyframes, %d config AUs, %d total bytes (avg %d B/frame)",
		w, h, fps, frames, dur.Round(time.Millisecond), encFPS, keyframes, configFrames, bytes, bytes/frames)
	if keyframes == 0 {
		t.Error("no keyframes in the encoded stream (Annex-B config/IDR missing)")
	}
	if encFPS < fps*0.5 {
		t.Errorf("encode fps %.1f far below source %.0f - the sender can't keep up", encFPS, fps)
	}
}
