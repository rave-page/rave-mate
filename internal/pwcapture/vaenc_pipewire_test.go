//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"rave.page/mate/internal/medialink"
)

// TestDMABufEncodeAPI drives the PRODUCTION source API (not the RPW_VA_ENC env hook): openCapture ->
// StartDMABufEncode(h264_vaapi_native) -> Next returns Annex-B AUs. Asserts the route opens with a
// keyframe, a mid-run RequestKeyframe (PLI) produces another, writes the AUs to RPW_H264_OUT for the
// driver's decode+corner check, and logs PipeStats (engine, fps, dropped, bytes/frame). Set
// RPW_TEST_NODE (+ RPW_VA_ENC gate kept off: this path does not use it).
func TestDMABufEncodeAPI(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	out := os.Getenv("RPW_H264_OUT")
	if node == "" || os.Getenv("RPW_ENC_API") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_ENC_API")
	}
	if !captureAvailable() || !VAEncodeAvailable() {
		t.Skip("pwcapture/VA not available")
	}
	n := 300
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
	de, ok := src.(medialink.DMABufEncodeSource)
	if !ok {
		t.Fatal("source is not a DMABufEncodeSource")
	}
	node4 := os.Getenv("RPW_EGL_DEVICE")
	if node4 == "" {
		node4 = "/dev/dri/renderD128"
	}
	w, _ := strconv.Atoi(os.Getenv("RPW_ENC_W"))
	h, _ := strconv.Atoi(os.Getenv("RPW_ENC_H"))
	kbps, _ := strconv.Atoi(os.Getenv("RPW_ENC_KBPS"))
	maxh, _ := strconv.Atoi(os.Getenv("RPW_ENC_MAXH"))
	spec := medialink.EncodeSpec{Encoder: medialink.EncoderVAAPINativeH264, Codec: medialink.CodecH264,
		Width: w, Height: h, FPS: 60, BitrateKbps: kbps, MaxHeight: maxh, VAAPIRenderNode: node4}
	esrc, started, err := de.StartDMABufEncode(spec)
	if err != nil || !started {
		t.Fatalf("StartDMABufEncode started=%v err=%v", started, err)
	}
	var f *os.File
	if out != "" {
		if f, err = os.Create(out); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(n/30+30)*time.Second)
	defer cancel()
	var frames, keyframes, bytes int
	var sawFirstKey bool
	var start time.Time
	warm := 30
	pliAt := warm + n/2 // request a keyframe mid-run (scales with frame count)
	var pliKeyframeAfter bool
	for i := 0; i < warm+n; i++ {
		fr, err := esrc.Next(ctx)
		if err != nil {
			t.Fatalf("Next after %d: %v", i, err)
		}
		if i == 0 && fr.Keyframe() {
			sawFirstKey = true
		}
		if i == warm {
			start = time.Now()
		}
		if i == pliAt {
			if ks, ok := esrc.(medialink.KeyframeSource); ok {
				ks.RequestKeyframe()
			}
		}
		if i > pliAt && i <= pliAt+15 && fr.Keyframe() {
			pliKeyframeAfter = true
		}
		if f != nil {
			_, _ = f.Write(fr.Payload)
		}
		if i >= warm {
			frames++
			bytes += len(fr.Payload)
			if fr.Keyframe() {
				keyframes++
			}
		}
	}
	dur := time.Since(start)
	fps := float64(frames) / dur.Seconds()
	var st medialink.PipelineStats
	if pr, ok := esrc.(medialink.PipelineReporter); ok {
		st = pr.PipeStats()
	}
	t.Logf("DMABUF-ENC %d AUs in %v (~%.1f fps), %d bytes (avg %d), keyframes=%d firstKey=%v pliKey=%v",
		frames, dur.Round(time.Millisecond), fps, bytes, bytes/max1(frames), keyframes, sawFirstKey, pliKeyframeAfter)
	t.Logf("PipeStats: engine=%s outFps=%.1f capFps=%.1f dropped=%d zeroCopy=%v bytes/frame=%.0f node=%s",
		st.Encoder, st.OutFPS, st.CapFPS, st.Dropped, st.ZeroCopy, st.AUBytesPerFrame, st.DevPolicy)
	if !sawFirstKey {
		t.Error("route did not open with a keyframe")
	}
	if !pliKeyframeAfter {
		t.Error("RequestKeyframe (PLI) did not produce a keyframe")
	}
	if fps < 30 {
		t.Errorf("encode fps too low: %.1f", fps)
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

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
