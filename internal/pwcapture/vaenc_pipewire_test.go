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
	// Warm-up frames excluded from the fps window: the first Next pays for format
	// negotiation + first-frame latency, which would understate steady-state throughput.
	warm := 60
	if v := os.Getenv("RPW_H264_WARMUP"); v != "" {
		if x, err := strconv.Atoi(v); err == nil {
			warm = x
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

	// Generous wall budget: warm-up + measured window at >=2x the real-time duration.
	budget := time.Duration((warm+n)/30+30) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var bytes, keyish int
	var start time.Time
	for i := 0; i < warm+n; i++ {
		fr, err := src.Next(ctx)
		if err != nil {
			t.Fatalf("Next after %d frames: %v", i, err)
		}
		if i == warm { // begin timing once steady state is reached
			start = time.Now()
		}
		if _, err := f.Write(fr.Payload); err != nil {
			t.Fatal(err)
		}
		if i >= warm {
			bytes += len(fr.Payload)
			if len(fr.Payload) > 20000 { // crude: IDR AUs are much larger
				keyish++
			}
		}
	}
	dur := time.Since(start)
	fps := float64(n) / dur.Seconds()
	t.Logf("VA-ENC %d AUs (after %d warm-up) in %v (~%.1f fps), %d bytes (avg %d), big(IDR-ish)=%d -> %s",
		n, warm, dur.Round(time.Millisecond), fps, bytes, bytes/n, keyish, out)
	if fps < 30 {
		t.Errorf("encode fps too low: %.1f", fps)
	}
}

// TestVAEncodeSoak runs the zero-copy VA capture+encode for RPW_SOAK_SECONDS (default 600), reading
// H.264 AUs directly from the source (no mediapipe wrap). The driver restarts the producer; this
// proves capture+encode resume + no leak across restarts. Reports sustained fps.
func TestVAEncodeSoak(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_VA_ENC") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_VA_ENC")
	}
	if !captureAvailable() {
		t.Skip("libpipewire not available")
	}
	secs := 600
	if v := os.Getenv("RPW_SOAK_SECONDS"); v != "" {
		if x, err := strconv.Atoi(v); err == nil {
			secs = x
		}
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+30)*time.Second)
	defer cancel()

	start := time.Now()
	deadline := start.Add(time.Duration(secs) * time.Second)
	var frames, bytes, stalls int
	lastBeat := time.Now()
	lastFrames := 0
	for time.Now().Before(deadline) {
		fctx, fcancel := context.WithTimeout(ctx, 5*time.Second)
		fr, err := src.Next(fctx)
		fcancel()
		if err != nil {
			stalls++
			if ctx.Err() != nil {
				break
			}
			continue
		}
		frames++
		bytes += len(fr.Payload)
		if time.Since(lastBeat) >= 30*time.Second {
			t.Logf("[%4.0fs] frames=%d (+%d/30s) %dMB stalls=%d", time.Since(start).Seconds(), frames, frames-lastFrames, bytes/1024/1024, stalls)
			lastBeat = time.Now()
			lastFrames = frames
		}
	}
	t.Logf("VA-ENC SOAK %ds: frames=%d ~%.1f fps, %dMB, stalls=%d", secs, frames, float64(frames)/float64(secs), bytes/1024/1024, stalls)
	if frames < secs*30 {
		t.Errorf("only %d frames in %ds - did not sustain across restarts", frames, secs)
	}
}
