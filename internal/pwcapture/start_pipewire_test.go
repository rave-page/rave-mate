//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/medialink"
)

// TestConcurrentStartDMABufOffer is the regression test for the "bar route ~1 fps after a daemon
// restart" bug: routes started concurrently (daemon restart => every remembered route at once) raced the
// unguarded one-time EGL/VA loaders - the loser saw "loading" as "unavailable", built a SHM-only offer,
// and a dmabuf-only producer (libfunnel/Spout2PW) then failed every link with "no more input formats"
// (reconnect churn, 0 frames, forever). Every concurrently started capture must build the dmabuf offer
// and see VA. Needs a GPU render node; the target node need not exist. Set RPW_CONCURRENT_START.
func TestConcurrentStartDMABufOffer(t *testing.T) {
	if os.Getenv("RPW_CONCURRENT_START") == "" {
		t.Skip("set RPW_CONCURRENT_START (needs /dev/dri + libEGL/libva)")
	}
	if !captureAvailable() {
		t.Skip("libpipewire not available")
	}
	const n = 8
	var wg sync.WaitGroup
	srcs := make([]medialink.Source, n)
	vaOK := make([]bool, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			vaOK[i] = VAEncodeAvailable() // first-ever VA load, raced by all goroutines
			srcs[i], errs[i] = openCapture(context.Background(), nil, "page.rave.mate.testprod.w2none")
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("capture %d: %v", i, errs[i])
		}
		ps := srcs[i].(*pwSource)
		if !ps.eglReady() {
			t.Errorf("capture %d: SHM-only offer (EGL load raced) - a dmabuf producer can never link", i)
		}
		if !vaOK[i] {
			t.Errorf("capture %d: VA reported unavailable (VA load raced)", i)
		}
		_ = srcs[i].Close()
	}
}

// TestReconnectBackoff: a link that can never negotiate (RPW_SHM_ONLY = the SHM-only offer the load race
// produced, vs a dmabuf-only producer) must back off instead of churning ~40 reconnects/s (the live bar
// route logged 100k+ reconnects and flooded the PipeWire journal). Set RPW_TEST_NODE (a running dmabuf
// producer) + RPW_SHM_ONLY.
func TestReconnectBackoff(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_SHM_ONLY") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_SHM_ONLY")
	}
	if !captureAvailable() {
		t.Skip("libpipewire not available")
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	frames := 0
	for ctx.Err() == nil {
		if _, err := src.Next(ctx); err == nil {
			frames++
		}
	}
	rc := src.(*pwSource).reconnectCount()
	t.Logf("BACKOFF 8s: reconnects=%d frames=%d", rc, frames)
	if frames != 0 {
		t.Errorf("SHM-only offer captured %d frames from a dmabuf-only producer", frames)
	}
	if rc > 15 { // 50ms doubling to a 2s cap: ~9 in 8s; unbounded churn was ~300
		t.Errorf("reconnect churn: %d reconnects in 8s", rc)
	}
	if rc < 2 {
		t.Errorf("no reconnect attempts (%d): a failed link must keep retrying", rc)
	}
}

// TestVACaps logs the VA encode/VPP capabilities (RGB input to the encoder, quality-level range, context
// priority) on the rig's render node. Set RPW_VA_CAPS.
func TestVACaps(t *testing.T) {
	if os.Getenv("RPW_VA_CAPS") == "" || !VAEncodeAvailable() {
		t.Skip("set RPW_VA_CAPS (needs libva)")
	}
	logVACaps("/dev/dri/renderD128")
}

// TestAutoCap: the auto coded-size controller under a simulated overload (RPW_ENC_SLOW_US: encode cost
// proportional to coded pixels, e.g. 20000 = 20 ms at native 4K -> 13.9 ms at 1800p). Expect: step down
// to a sustainable coded size within ~win_s, even 60 fps there, and no flapping (the up-probe that
// overloads again doubles the dwell). Set RPW_TEST_NODE + RPW_AUTOCAP_TEST (+ RPW_ENC_SLOW_US,
// RPW_AUTOCAP_UP_S); RPW_AUTOCAP_SECS (40).
func TestAutoCap(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_AUTOCAP_TEST") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_AUTOCAP_TEST")
	}
	if !captureAvailable() || !VAEncodeAvailable() {
		t.Skip("pwcapture/VA not available")
	}
	secs := 40
	if v, err := strconv.Atoi(os.Getenv("RPW_AUTOCAP_SECS")); err == nil && v > 0 {
		secs = v
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	esrc, ok, err := src.(medialink.DMABufEncodeSource).StartDMABufEncode(medialink.EncodeSpec{
		Encoder: medialink.EncoderVAAPINativeHEVC, Codec: medialink.CodecHEVC, FPS: 60, BitrateKbps: 25000,
		VAAPIRenderNode: "/dev/dri/renderD128"})
	if err != nil || !ok {
		t.Fatalf("StartDMABufEncode ok=%v err=%v", ok, err)
	}
	ps := esrc.(*pwSource)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+20)*time.Second)
	defer cancel()
	start := time.Now()
	lastGen, frames, lastSec := 0, 0, 0
	// fps while held at a reduced level (excluding 2 s after each step: encoder rebuild + IDR)
	var lowFrames int
	var lowStart, settleUntil time.Time
	var lowDur time.Duration
	lowSkip := int64(0)
	var lowSkip0 int64
	for time.Since(start) < time.Duration(secs)*time.Second {
		if _, err := esrc.Next(ctx); err != nil {
			t.Fatal(err)
		}
		frames++
		now := time.Now()
		el := int(now.Sub(start).Seconds())
		lvl, h, gen, why := ps.autocapState()
		if gen != lastGen {
			lastGen = gen
			t.Logf("AUTOCAP t=%ds level=%d codedH=%d  %s", el, lvl, h, why)
			if !lowStart.IsZero() {
				lowDur += now.Sub(lowStart)
				lowSkip += ps.ptsSkipped() - lowSkip0
				lowStart = time.Time{}
			}
			settleUntil = now.Add(2 * time.Second)
		}
		if lvl > 0 && now.After(settleUntil) {
			if lowStart.IsZero() {
				lowStart, lowSkip0 = now, ps.ptsSkipped()
			} else {
				lowFrames++
			}
		}
		_ = lastSec
	}
	if !lowStart.IsZero() {
		lowDur += time.Since(lowStart)
		lowSkip += ps.ptsSkipped() - lowSkip0
	}
	lvl, h, gen, why := ps.autocapState()
	st := ps.PipeStats()
	lowFps := float64(lowFrames) / lowDur.Seconds()
	t.Logf("AUTOCAP end: level=%d codedH=%d steps=%d reason=%q  reduced-level time=%.1fs fps=%.1f ptsSkipped=%d  capDrops(total)=%d degraded=%q",
		lvl, h, gen, why, lowDur.Seconds(), lowFps, lowSkip, ps.capDropsCount(), st.DegradeReason)
	if gen < 1 {
		t.Errorf("controller never stepped down under overload")
	}
	if gen > 6 {
		t.Errorf("flapping: %d steps in %ds", gen, secs)
	}
	if lowDur > 5*time.Second && lowFps < 58 {
		t.Errorf("not even at the reduced size: %.1f fps", lowFps)
	}
}

// TestSourceFreeze drives the frozen-picture detector: the driver freezes the SOURCE PICTURE (SIGSTOP the
// ffmpeg feeding funnel-producer -stdin: the producer keeps presenting the held frame = a VRChat-style
// hitch) and/or stalls the producer (SIGSTOP it: no buffers, cadence repeats fill). Logs the detector
// state; RPW_FREEZE_EXPECT=n asserts >= n events. Set RPW_TEST_NODE + RPW_FREEZE_TEST; RPW_FREEZE_SECS (20).
func TestSourceFreeze(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_FREEZE_TEST") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_FREEZE_TEST")
	}
	if !captureAvailable() || !VAEncodeAvailable() {
		t.Skip("pwcapture/VA not available")
	}
	secs := 20
	if v, err := strconv.Atoi(os.Getenv("RPW_FREEZE_SECS")); err == nil && v > 0 {
		secs = v
	}
	src, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	esrc, ok, err := src.(medialink.DMABufEncodeSource).StartDMABufEncode(medialink.EncodeSpec{
		Encoder: medialink.EncoderVAAPINativeHEVC, Codec: medialink.CodecHEVC, FPS: 60, BitrateKbps: 50000,
		VAAPIRenderNode: "/dev/dri/renderD128"})
	if err != nil || !ok {
		t.Fatalf("StartDMABufEncode ok=%v err=%v", ok, err)
	}
	ps := esrc.(*pwSource)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+20)*time.Second)
	defer cancel()
	var lastN int64
	start := time.Now()
	for time.Since(start) < time.Duration(secs)*time.Second {
		if _, err := esrc.Next(ctx); err != nil {
			t.Fatal(err)
		}
		if n, last, _, at, dv, rep := ps.freezeStats(); n != lastN {
			lastN = n
			t.Logf("FREEZE #%d %d ms at %s delivered=%v repeats=%d", n, last, at.Format("15:04:05.000"), dv, rep)
		}
	}
	t.Logf("FREEZE end: %q", ps.freezeNote())
	if v, err := strconv.Atoi(os.Getenv("RPW_FREEZE_EXPECT")); err == nil && lastN < int64(v) {
		t.Errorf("freezes detected %d < expected %d", lastN, v)
	}
}
