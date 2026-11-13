//go:build linux && pipewire

package pwcapture

import (
	"context"
	"math/rand"
	"os"
	"sort"
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
	encoder, codec := medialink.EncoderVAAPINativeH264, medialink.CodecH264
	if os.Getenv("RPW_ENC_CODEC") == "hevc" {
		encoder, codec = medialink.EncoderVAAPINativeHEVC, medialink.CodecHEVC
	}
	// Adaptive budget: RPW_ENC_Q (1..100) engages constant-quality-with-ceiling (BitrateKbps = the
	// ceiling). 0/unset = legacy rate-controlled. RPW_RATEHINT_Q applies a live quality change mid-run.
	quality, _ := strconv.Atoi(os.Getenv("RPW_ENC_Q"))
	hintQ, _ := strconv.Atoi(os.Getenv("RPW_RATEHINT_Q"))
	spec := medialink.EncodeSpec{Encoder: encoder, Codec: codec,
		Width: w, Height: h, FPS: 60, BitrateKbps: kbps, MaxHeight: maxh, VAAPIRenderNode: node4,
		QualityTarget: quality}
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
	pliLatencyFrames := -1
	// 1s-window bitrate tracking (max window = the congestion-relevant peak).
	var winBytes int
	var winStart time.Time
	var maxWinKbps, lastWinKbps float64
	// Quality feedback sampling (adaptive budget): avg QP + the fraction of samples rate-limited.
	var fbSamples, fbRateLimited int
	var fbQPSum float64
	// Optional mid-run SetRateHint (RPW_RATEHINT_KBPS): drop the budget at the midpoint; lastWinKbps
	// then reflects the post-hint window to prove it took effect within ~1s.
	hintKbps, _ := strconv.Atoi(os.Getenv("RPW_RATEHINT_KBPS"))
	hintAt := warm + n/2
	// Optional live MaxHeight (RPW_MAXH): apply at warm+n/3, restore (0) at warm+2n/3, to prove the
	// coded size changes and restores on the native path (nominal geometry unchanged).
	maxh2, _ := strconv.Atoi(os.Getenv("RPW_MAXH2"))
	capAt, restoreAt := warm+n/3, warm+2*n/3
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
			winStart = start
		}
		if hintKbps > 0 && i == hintAt {
			if rc, ok := esrc.(medialink.RateControlSource); ok {
				rc.SetRateHint(medialink.RateHint{MaxBitrateKbps: hintKbps})
			}
		}
		if hintQ > 0 && i == hintAt { // live quality-level change (no reopen)
			if rc, ok := esrc.(medialink.RateControlSource); ok {
				rc.SetRateHint(medialink.RateHint{QualityTarget: hintQ})
			}
		}
		if quality > 0 && i >= warm && (i-warm)%30 == 0 { // ~1s feedback sampling
			if pr, ok := esrc.(medialink.PipelineReporter); ok {
				fb := pr.PipeStats().Feedback
				if fb.Present {
					fbSamples++
					fbQPSum += fb.AvgQP
					if fb.RateLimited {
						fbRateLimited++
					}
				}
			}
		}
		if maxh2 > 0 {
			if rc, ok := esrc.(medialink.RateControlSource); ok {
				if i == capAt {
					rc.SetRateHint(medialink.RateHint{MaxHeight: maxh2})
				} else if i == restoreAt {
					rc.SetRateHint(medialink.RateHint{MaxHeight: 0})
				}
			}
		}
		if i >= warm {
			winBytes += len(fr.Payload)
			if el := time.Since(winStart).Seconds(); el >= 1.0 {
				k := float64(winBytes) * 8 / 1000 / el
				if k > maxWinKbps {
					maxWinKbps = k
				}
				lastWinKbps = k
				winBytes = 0
				winStart = time.Now()
			}
		}
		if i == pliAt {
			if ks, ok := esrc.(medialink.KeyframeSource); ok {
				ks.RequestKeyframe()
			}
		}
		if i > pliAt && i <= pliAt+15 && fr.Keyframe() {
			if !pliKeyframeAfter {
				pliLatencyFrames = i - pliAt // PLI request -> first IDR, in frames
			}
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
	avgKbps := float64(bytes) * 8 / 1000 / dur.Seconds()
	budget := kbps
	t.Logf("DMABUF-ENC %d AUs in %v (~%.1f fps), %d bytes (avg %d), keyframes=%d firstKey=%v pliKey=%v",
		frames, dur.Round(time.Millisecond), fps, bytes, bytes/max1(frames), keyframes, sawFirstKey, pliKeyframeAfter)
	t.Logf("BITRATE budget=%d kbps  avg=%.0f kbps  max1s=%.0f kbps  lastWin=%.0f kbps  (ceiling test: max1s <= budget*1.1)",
		budget, avgKbps, maxWinKbps, lastWinKbps)
	t.Logf("PLI->IDR latency: %d frames", pliLatencyFrames)
	t.Logf("PipeStats: engine=%s outFps=%.1f capFps=%.1f dropped=%d zeroCopy=%v bytes/frame=%.0f node=%s",
		st.Encoder, st.OutFPS, st.CapFPS, st.Dropped, st.ZeroCopy, st.AUBytesPerFrame, st.DevPolicy)
	if quality > 0 {
		t.Logf("QUALITY q=%d targetQP=%d budget=%d  avgQP=%.1f  rateLimited=%d/%d (%.0f%%)  present=%v finalQP=%.1f finalRL=%v",
			quality, medialink.QualityToQP(codec, quality), st.BudgetKbps,
			fbQPSum/float64(max1(fbSamples)), fbRateLimited, fbSamples,
			100*float64(fbRateLimited)/float64(max1(fbSamples)), st.Feedback.Present,
			st.Feedback.AvgQP, st.Feedback.RateLimited)
		if !st.Feedback.Present {
			t.Error("quality mode but the encoder reported no feedback (Present=false)")
		}
		if st.QualityTarget != quality {
			t.Errorf("PipeStats.QualityTarget = %d, want %d", st.QualityTarget, quality)
		}
	}
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

// TestNativeTXSend drives the native realtime send path end-to-end on the SENDER side: capture -> VA
// encode -> AttachNativeTX -> rm_tx_submit_au (no Go pump). It verifies the C+Go wiring + the native
// sender attach/submit/stats on real hardware. Datagrams go to a dummy peer (no receiver => no decode
// check; end-to-end decode needs a paired UDP receiver, which the owner's TCP route doesn't provide).
// Set RPW_TEST_NODE + RPW_NATIVE_TX; RPW_NATIVE_PEER (default 127.0.0.1:48999), RPW_ENC_CODEC/KBPS.
func TestNativeTXSend(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_NATIVE_TX") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_NATIVE_TX")
	}
	if !captureAvailable() || !VAEncodeAvailable() {
		t.Skip("pwcapture/VA not available")
	}
	secs := 6
	if v, err := strconv.Atoi(os.Getenv("RPW_NATIVE_TX_SECS")); err == nil && v > 0 {
		secs = v
	}
	peer := os.Getenv("RPW_NATIVE_PEER")
	if peer == "" {
		peer = "127.0.0.1:48999"
	}
	node4 := os.Getenv("RPW_EGL_DEVICE")
	if node4 == "" {
		node4 = "/dev/dri/renderD128"
	}
	kbps, _ := strconv.Atoi(os.Getenv("RPW_ENC_KBPS"))
	if kbps == 0 {
		kbps = 8000
	}
	encoder, codec := medialink.EncoderVAAPINativeH264, medialink.CodecH264
	if os.Getenv("RPW_ENC_CODEC") == "hevc" {
		encoder, codec = medialink.EncoderVAAPINativeHEVC, medialink.CodecHEVC
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	de := src.(medialink.DMABufEncodeSource)
	spec := medialink.EncodeSpec{Encoder: encoder, Codec: codec, FPS: 60,
		BitrateKbps: kbps, QualityTarget: medialink.QualityDefault, VAAPIRenderNode: node4}
	esrc, started, err := de.StartDMABufEncode(spec)
	if err != nil || !started {
		t.Fatalf("StartDMABufEncode started=%v err=%v", started, err)
	}
	ntxs, ok := esrc.(medialink.NativeTXSource)
	if !ok {
		t.Fatal("source is not a NativeTXSource")
	}
	var sk, rk [32]byte // plaintext test (Encrypt=false): AEAD keys unused
	p := medialink.NativeTXParams{
		Transport: medialink.TransportUDP, Codec: codec, Stream: 1, BodyMax: 0,
		PeerAddr: peer, SocketFD: -1, Encrypt: false,
		SendKey: sk, SendPrefix: 1, RecvKey: rk, RecvPrefix: 2, Token: 0x1234,
		BitrateKbps: kbps, FPS: 60,
	}
	tx, err := ntxs.AttachNativeTX(p)
	if err != nil {
		t.Fatalf("AttachNativeTX: %v", err)
	}
	defer func() { _ = tx.Close() }()
	var st medialink.NativeTXStats
	for i := 0; i < secs; i++ {
		select {
		case <-tx.Done():
			t.Fatalf("native sender ended early (%ds): %v", i, tx.Err())
		case <-time.After(time.Second):
		}
		st = tx.Stats()
		t.Logf("NATIVE-TX t=%ds submitted=%d sent=%d dropped=%d datagrams=%d bytes=%d qdepth=%d dead=%v",
			i+1, st.AUsSubmitted, st.AUsSent, st.AUsDropped, st.DatagramsSent, st.BytesSent, st.QueueDepth, st.Dead)
	}
	// Native-path telemetry must be truthful (the Go pump that normally feeds it does not run).
	ps := esrc.(medialink.PipelineReporter).PipeStats()
	t.Logf("NATIVE-TX telemetry: outFps=%.1f capFps=%.1f bytes/frame=%.0f queue=%d dropped=%d keyframes=%d",
		ps.OutFPS, ps.CapFPS, ps.AUBytesPerFrame, ps.QueueDepth, ps.Dropped, st.Keyframes)
	t.Logf("NATIVE-TX stage enc: n=%d p50=%.0f p99=%.0f max=%.1f mean=%.2f >25/50/100=%d/%d/%d",
		st.Enc.Count, st.Enc.P50Ms, st.Enc.P99Ms, st.Enc.MaxMs, st.Enc.MeanMs, st.Enc.Over25, st.Enc.Over50, st.Enc.Over100)
	t.Logf("NATIVE-TX stage tx:  n=%d p50=%.0f p99=%.0f max=%.1f mean=%.2f >25/50/100=%d/%d/%d",
		st.Tx.Count, st.Tx.P50Ms, st.Tx.P99Ms, st.Tx.MaxMs, st.Tx.MeanMs, st.Tx.Over25, st.Tx.Over50, st.Tx.Over100)
	if st.AUsSubmitted < uint64(secs*40) {
		t.Errorf("too few AUs submitted (%d) in %ds - native submit path not flowing", st.AUsSubmitted, secs)
	}
	if ps.OutFPS < 40 || ps.CapFPS < 40 {
		t.Errorf("native route fps telemetry zero/low: out=%.1f cap=%.1f", ps.OutFPS, ps.CapFPS)
	}
	if st.Enc.Count < uint64(secs*40) || st.Tx.Count < uint64(secs*40) || st.Keyframes == 0 {
		t.Errorf("native stages/keyframes not populated: enc=%d tx=%d kf=%d", st.Enc.Count, st.Tx.Count, st.Keyframes)
	}
	if st.Dead {
		t.Error("native sender dead")
	}
}

// TestDMABufEncodeHistogram measures the per-frame DELIVERY cadence of the capture+encode engine as a
// read-only second consumer: inter-AU interval p50/p99/max + counts over 25/50 ms, newest-wins drops,
// capture/encode fps. This is the stutter oracle - an even source should yield a tight histogram; a
// pipeline that overruns its PipeWire cycle under load shows interval spikes and climbing drops. Set
// RPW_TEST_NODE + RPW_HIST; RPW_HIST_SECS (default 15), RPW_ENC_CODEC, RPW_ENC_KBPS, RPW_ENC_Q.
func TestDMABufEncodeHistogram(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_HIST") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_HIST")
	}
	if !captureAvailable() || !VAEncodeAvailable() {
		t.Skip("pwcapture/VA not available")
	}
	secs := 15
	if v, err := strconv.Atoi(os.Getenv("RPW_HIST_SECS")); err == nil && v > 0 {
		secs = v
	}
	node4 := os.Getenv("RPW_EGL_DEVICE")
	if node4 == "" {
		node4 = "/dev/dri/renderD128"
	}
	kbps, _ := strconv.Atoi(os.Getenv("RPW_ENC_KBPS"))
	if kbps == 0 {
		kbps = 25000
	}
	quality, _ := strconv.Atoi(os.Getenv("RPW_ENC_Q"))
	encoder, codec := medialink.EncoderVAAPINativeH264, medialink.CodecH264
	if os.Getenv("RPW_ENC_CODEC") == "hevc" {
		encoder, codec = medialink.EncoderVAAPINativeHEVC, medialink.CodecHEVC
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	de := src.(medialink.DMABufEncodeSource)
	maxH, _ := strconv.Atoi(os.Getenv("RPW_ENC_MAXH")) // coded-height cap (0 = native)
	spec := medialink.EncodeSpec{Encoder: encoder, Codec: codec, FPS: 60,
		BitrateKbps: kbps, QualityTarget: quality, VAAPIRenderNode: node4, MaxHeight: maxH}
	esrc, started, err := de.StartDMABufEncode(spec)
	if err != nil || !started {
		t.Fatalf("StartDMABufEncode started=%v err=%v", started, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+30)*time.Second)
	defer cancel()
	pr, _ := esrc.(medialink.PipelineReporter)
	// RPW_JITTER_MS>0 simulates a jittery downstream (packetize/pace/send): sleep a random 0..N ms
	// after each AU. The ring must absorb jitter up to its depth with no drops/keyframe storms.
	jitterMs, _ := strconv.Atoi(os.Getenv("RPW_JITTER_MS"))
	// RPW_STALL_MS>0 simulates a downstream STALL (consumer blocked: receiver hiccup on TCP, native sender
	// queue): one sleep of STALL_MS every RPW_STALL_EVERY_S seconds (default 60). Expect: no encoder
	// stall, bounded ring, at most one forced IDR per stall, no keyframe storm.
	stallMs, _ := strconv.Atoi(os.Getenv("RPW_STALL_MS"))
	stallMax, _ := strconv.Atoi(os.Getenv("RPW_STALL_MAX_MS"))
	stallEvery, _ := strconv.Atoi(os.Getenv("RPW_STALL_EVERY_S"))
	if stallEvery <= 0 {
		stallEvery = 60
	}
	var stalls int
	var nextStall time.Time
	warm := 60
	var intervals []float64 // ms between successive AUs
	var last, winStart time.Time
	var dropAtWarm uint64
	var keyframes int
	for i := 0; ; i++ {
		fr, err := esrc.Next(ctx)
		if err != nil {
			t.Fatalf("Next after %d: %v", i, err)
		}
		now := time.Now()
		if i > warm && fr.Keyframe() {
			keyframes++
		}
		if i == warm {
			winStart = now
			last = now
			if pr != nil {
				dropAtWarm = pr.PipeStats().Dropped
			}
		} else if i > warm {
			intervals = append(intervals, now.Sub(last).Seconds()*1000)
			last = now
			if now.Sub(winStart) >= time.Duration(secs)*time.Second {
				break
			}
		}
		if jitterMs > 0 && rand.Intn(3) == 0 { // spike on ~1/3 of frames (avg stays under the frame
			time.Sleep(time.Duration(rand.Intn(jitterMs+1)) * time.Millisecond) // period: the ring absorbs it)
		}
		if stallMs > 0 && i >= warm {
			if nextStall.IsZero() {
				nextStall = now.Add(time.Duration(stallEvery) * time.Second / 2)
			} else if now.After(nextStall) {
				d := stallMs
				if stallMax > stallMs { // RPW_STALL_MAX_MS: uniform in [STALL_MS, STALL_MAX_MS]
					d += rand.Intn(stallMax - stallMs + 1)
				}
				time.Sleep(time.Duration(d) * time.Millisecond)
				stalls++
				nextStall = time.Now().Add(time.Duration(stallEvery) * time.Second)
				last = time.Now() // the stall itself is not a delivery gap of the engine
			}
		}
	}
	sort.Float64s(intervals)
	pct := func(p float64) float64 {
		if len(intervals) == 0 {
			return 0
		}
		idx := int(p / 100 * float64(len(intervals)))
		if idx >= len(intervals) {
			idx = len(intervals) - 1
		}
		return intervals[idx]
	}
	var over25, over50 int
	var sum float64
	for _, v := range intervals {
		sum += v
		if v > 25 {
			over25++
		}
		if v > 50 {
			over50++
		}
	}
	var st medialink.PipelineStats
	if pr != nil {
		st = pr.PipeStats()
	}
	fps := float64(len(intervals)) / time.Since(winStart).Seconds()
	drops := st.Dropped - dropAtWarm
	t.Logf("HIST node=%s codec=%s q=%d ceil=%d secs=%d frames=%d fps=%.2f",
		node, os.Getenv("RPW_ENC_CODEC"), quality, kbps, secs, len(intervals), fps)
	t.Logf("HIST interval ms: mean=%.2f p50=%.2f p90=%.2f p99=%.2f max=%.2f  >25ms=%d >50ms=%d",
		sum/float64(max1(len(intervals))), pct(50), pct(90), pct(99), pct(100), over25, over50)
	t.Logf("HIST engine: newest-wins drops(window)=%d capFps=%.2f outFps=%.2f zeroCopy=%v bytes/frame=%.0f",
		drops, st.CapFPS, st.OutFPS, st.ZeroCopy, st.AUBytesPerFrame)
	var forced int64
	if ps, ok := esrc.(*pwSource); ok {
		depth, hiwater, overflow, capDrops, gridDrops := ps.ringStats()
		repeats, skips, holds, fidr := ps.robustStats()
		forced = fidr
		t.Logf("HIST ring: depth=%d hiwater=%d overflow(total)=%d capDrops(total)=%d gridDrops(total)=%d  keyframes(window)=%d jitterMs=%d",
			depth, hiwater, overflow, capDrops, gridDrops, keyframes, jitterMs)
		t.Logf("HIST robust: repeats=%d ringSkips=%d startHolds=%d forcedIDRs=%d stalls=%d stallMs=%d",
			repeats, skips, holds, fidr, stalls, stallMs)
		capF, capD := ps.capFramesCount(), capDrops
		t.Logf("HIST loss: capFrames=%d capDrops=%d (%.2f%%) ptsSkipped=%d", capF, capD,
			100*float64(capD)/float64(max1(int(capF+capD))), ps.ptsSkipped())
		for i, st := range ps.stepStats() {
			if st.Count > 0 {
				t.Logf("HIST step %-6s n=%d p50=%.2f p99=%.2f max=%.2f mean=%.2f ms", stepNames[i], st.Count, st.P50Ms, st.P99Ms, st.MaxMs, st.MeanMs)
			}
		}
	}
	if len(intervals) < secs*20 {
		t.Errorf("far too few frames (%d) in %ds - capture stalled", len(intervals), secs)
	}
	// Optional regression gates (rig runs): RPW_HIST_MAXMS = max delivery interval, RPW_HIST_MINFPS.
	if v, err := strconv.ParseFloat(os.Getenv("RPW_HIST_MAXMS"), 64); err == nil && pct(100) > v {
		t.Errorf("max interval %.1f ms > %.1f ms", pct(100), v)
	}
	if v, err := strconv.ParseFloat(os.Getenv("RPW_HIST_MINFPS"), 64); err == nil && fps < v {
		t.Errorf("fps %.2f < %.2f", fps, v)
	}
	if stalls > 0 {
		if forced > int64(stalls) {
			t.Errorf("forced IDRs %d > stalls %d (keyframe storm)", forced, stalls)
		}
		gopIDRs := secs/2 + 2 // GOP = 2 s
		if keyframes > gopIDRs+stalls {
			t.Errorf("keyframes %d > GOP %d + stalls %d (keyframe storm)", keyframes, gopIDRs, stalls)
		}
	}
}

// TestStartupClean: route start-up must be clean - the consumer opens late (cold TCP / receiver decoder:
// RPW_START_DELAY_MS, default 600), yet the first AU is an IDR, nothing is dropped (frames during the
// hold are skipped pre-encode, not dropped), at most one IDR + GOP in the first 2 s, and once the
// consumer runs the delivery is even (no stale burst). Set RPW_TEST_NODE + RPW_STARTUP.
func TestStartupClean(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_STARTUP") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_STARTUP")
	}
	if !captureAvailable() || !VAEncodeAvailable() {
		t.Skip("pwcapture/VA not available")
	}
	delay := 600
	if v, err := strconv.Atoi(os.Getenv("RPW_START_DELAY_MS")); err == nil && v >= 0 {
		delay = v
	}
	codec, encoder := medialink.CodecH264, medialink.EncoderVAAPINativeH264
	if os.Getenv("RPW_ENC_CODEC") == "hevc" {
		encoder, codec = medialink.EncoderVAAPINativeHEVC, medialink.CodecHEVC
	}
	t0 := time.Now()
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	esrc, started, err := src.(medialink.DMABufEncodeSource).StartDMABufEncode(medialink.EncodeSpec{
		Encoder: encoder, Codec: codec, FPS: 60, BitrateKbps: 25000, VAAPIRenderNode: "/dev/dri/renderD128"})
	if err != nil || !started {
		t.Fatalf("StartDMABufEncode started=%v err=%v", started, err)
	}
	time.Sleep(time.Duration(delay) * time.Millisecond) // cold consumer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var first time.Time
	var ivs []float64
	var last time.Time
	keyframes := 0
	for i := 0; ; i++ {
		fr, err := esrc.Next(ctx)
		if err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
		now := time.Now()
		if i == 0 {
			first = now
			if !fr.Keyframe() {
				t.Error("first AU is not an IDR")
			}
		} else {
			ivs = append(ivs, now.Sub(last).Seconds()*1000)
		}
		if fr.Keyframe() {
			keyframes++
		}
		last = now
		if now.Sub(first) >= 2*time.Second {
			break
		}
	}
	ps := esrc.(*pwSource)
	_, skips, holds, fidr := ps.robustStats()
	st := ps.PipeStats()
	var over25, burst int
	var maxIv float64
	for i, v := range ivs {
		if v > 25 {
			over25++
		}
		if v > maxIv {
			maxIv = v
		}
		if i >= 1 && v < 2 { // back-to-back AUs after the first two = a stale burst
			burst++
		}
	}
	t.Logf("STARTUP open->firstAU=%v (consumer delay %dms) AUs=%d keyframes=%d maxIv=%.1fms >25ms=%d burst=%d holds=%d skips=%d forcedIDR=%d dropped=%d",
		first.Sub(t0).Round(time.Millisecond), delay, len(ivs)+1, keyframes, maxIv, over25, burst, holds, skips, fidr, st.Dropped)
	if skips != 0 || st.Dropped != 0 || fidr != 0 {
		t.Errorf("start-up dropped frames: skips=%d dropped=%d forcedIDR=%d", skips, st.Dropped, fidr)
	}
	if keyframes > 2 {
		t.Errorf("keyframe storm at start-up: %d IDRs in 2 s", keyframes)
	}
	if burst > 3 {
		t.Errorf("stale burst at start-up: %d back-to-back AUs", burst)
	}
	if len(ivs) < 100 {
		t.Errorf("too few AUs in the first 2 s: %d", len(ivs)+1)
	}
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
