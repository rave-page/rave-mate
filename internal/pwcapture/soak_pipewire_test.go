//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediapipe"
	"rave.page/mate/internal/mediatools"
)

// TestCaptureSoak runs capture -> VA-API encode for RPW_SOAK_SECONDS (default 600), logging a
// heartbeat every 30s. The driver script restarts the producer mid-run; this proves capture resumes
// (frames keep flowing across the gaps) with no leak. Reports frames + drops; the script samples
// RSS/fd. Skipped without RPW_TEST_NODE.
func TestCaptureSoak(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" {
		t.Skip("set RPW_TEST_NODE")
	}
	if !captureAvailable() {
		t.Skip("libpipewire not available")
	}
	if _, ok := mediatools.Resolve("ffmpeg"); !ok {
		t.Skip("ffmpeg not on PATH")
	}
	secs := 600
	if v := os.Getenv("RPW_SOAK_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			secs = n
		}
	}
	nodes, _ := DiscoverNodes(context.Background())
	var w, h int
	var fps float64
	for _, n := range nodes {
		if n.Name == node {
			w, h, fps = n.Width, n.Height, n.FPS
		}
	}
	if w == 0 {
		t.Fatalf("node %q not discovered", node)
	}
	if fps <= 0 {
		fps = 60
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+30)*time.Second)
	defer cancel()
	src, err := openCapture(ctx, nil, node)
	if err != nil {
		t.Fatal(err)
	}
	encFac, _ := mediapipe.Factories(logbus.New(64))
	spec := medialink.EncodeSpec{Encoder: "h264_vaapi", Codec: medialink.CodecH264, Width: w, Height: h, FPS: fps, DeviceIndex: -1}
	enc, err := encFac(ctx, spec, src)
	if err != nil {
		_ = src.Close()
		t.Fatal(err)
	}
	defer func() { _ = enc.Close() }()

	start := time.Now()
	deadline := start.Add(time.Duration(secs) * time.Second)
	var frames, bytes int
	lastBeat := time.Now()
	lastFrames := 0
	var stalls int
	for time.Now().Before(deadline) {
		fctx, fcancel := context.WithTimeout(ctx, 5*time.Second)
		f, err := enc.Next(fctx)
		fcancel()
		if err != nil {
			stalls++ // producer-restart gap or transient; keep going until the soak deadline
			if ctx.Err() != nil {
				break
			}
			continue
		}
		frames++
		bytes += len(f.Payload)
		if f.Release != nil {
			f.Release()
		}
		if time.Since(lastBeat) >= 30*time.Second {
			t.Logf("[%4.0fs] frames=%d (+%d last 30s) bytes=%dMB stalls=%d", time.Since(start).Seconds(), frames, frames-lastFrames, bytes/1024/1024, stalls)
			lastBeat = time.Now()
			lastFrames = frames
		}
	}
	t.Logf("SOAK DONE: %ds, frames=%d, ~%.1f fps avg, stalls(5s-gaps)=%d, bytes=%dMB",
		secs, frames, float64(frames)/float64(secs), stalls, bytes/1024/1024)
	// A sustained run at 60fps is ~secs*60; even allowing a ~5s gap per restart, expect >= secs*30.
	// A capture that stalls (stops after a restart) lands far below this.
	if frames < secs*30 {
		t.Errorf("only %d frames in %ds (~%.1f fps) - capture did not sustain across restarts",
			frames, secs, float64(frames)/float64(secs))
	}
}
