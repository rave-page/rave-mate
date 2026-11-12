//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/medialink"
)

// TestQualityPair records, from the SAME producer buffers, the streams under test (one per quality
// target in RPW_ENC_Q, e.g. "50 70 85", ceiling kbps) and a near-lossless reference (HEVC CQP 10, no
// ceiling) as concurrent read-only consumers - same frames for every q, so the driver's PSNR/SSIM
// (test vs reference ~= test vs source) compares levels on identical content. Also a GPU-contention
// data point (N+1 concurrent encodes). Set RPW_TEST_NODE + RPW_QPAIR; RPW_ENC_CODEC, RPW_ENC_KBPS,
// RPW_QPAIR_FRAMES (240), RPW_QPAIR_OUT (prefix: .test<q> / .ref).
func TestQualityPair(t *testing.T) {
	node := os.Getenv("RPW_TEST_NODE")
	if node == "" || os.Getenv("RPW_QPAIR") == "" {
		t.Skip("set RPW_TEST_NODE + RPW_QPAIR")
	}
	if !captureAvailable() || !VAEncodeAvailable() {
		t.Skip("pwcapture/VA not available")
	}
	n := 240
	if v, err := strconv.Atoi(os.Getenv("RPW_QPAIR_FRAMES")); err == nil && v > 0 {
		n = v
	}
	prefix := os.Getenv("RPW_QPAIR_OUT")
	if prefix == "" {
		prefix = "/tmp/pw4ktest/w2qpair"
	}
	var qs []int
	for _, f := range strings.Fields(os.Getenv("RPW_ENC_Q")) {
		if v, err := strconv.Atoi(f); err == nil {
			qs = append(qs, v)
		}
	}
	if len(qs) == 0 {
		qs = []int{medialink.QualityDefault}
	}
	kbps, _ := strconv.Atoi(os.Getenv("RPW_ENC_KBPS"))
	if kbps == 0 {
		kbps = 50000
	}
	encoder, codec := medialink.EncoderVAAPINativeH264, medialink.CodecH264
	if os.Getenv("RPW_ENC_CODEC") == "hevc" {
		encoder, codec = medialink.EncoderVAAPINativeHEVC, medialink.CodecHEVC
	}
	rnode := "/dev/dri/renderD128"
	encs := make([]medialink.Source, len(qs))
	for i, q := range qs {
		src, err := openCapture(context.Background(), nil, node)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = src.Close() }()
		e, ok, err := src.(medialink.DMABufEncodeSource).StartDMABufEncode(medialink.EncodeSpec{
			Encoder: encoder, Codec: codec, FPS: 60, BitrateKbps: kbps, QualityTarget: q, VAAPIRenderNode: rnode})
		if err != nil || !ok {
			t.Fatalf("q%d: started=%v err=%v", q, ok, err)
		}
		encs[i] = e
	}
	srcB, err := openCapture(context.Background(), nil, node)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srcB.Close() }()
	if !srcB.(*pwSource).startEncodeRawQP(1, 10, rnode) {
		t.Fatal("reference encoder unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(n/20+30)*time.Second)
	defer cancel()
	type res struct {
		bytes, maxWinKbps float64
		frames            int
		dur               time.Duration
		err               error
	}
	record := func(src medialink.Source, path string, out *res) {
		f, err := os.Create(path)
		if err != nil {
			out.err = err
			return
		}
		defer func() { _ = f.Close() }()
		pf, err := os.Create(path + ".pts") // one capture PTS per AU: the driver pairs frames by PTS (drops differ per consumer)
		if err != nil {
			out.err = err
			return
		}
		defer func() { _ = pf.Close() }()
		var start, win time.Time
		var winBytes int
		for i := 0; i < n; i++ {
			fr, err := src.Next(ctx)
			if err != nil {
				out.err = err
				return
			}
			now := time.Now()
			if i == 0 {
				start, win = now, now
			}
			_, _ = f.Write(fr.Payload)
			_, _ = pf.WriteString(strconv.FormatInt(fr.PTS, 10) + "\n")
			out.bytes += float64(len(fr.Payload))
			winBytes += len(fr.Payload)
			if el := now.Sub(win).Seconds(); el >= 1 {
				if k := float64(winBytes) * 8 / 1000 / el; k > out.maxWinKbps {
					out.maxWinKbps = k
				}
				winBytes, win = 0, now
			}
			out.frames++
		}
		out.dur = time.Since(start)
	}
	as := make([]res, len(qs))
	var b res
	var wg sync.WaitGroup
	for i := range qs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); record(encs[i], prefix+".test"+strconv.Itoa(qs[i]), &as[i]) }(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); record(srcB, prefix+".ref", &b) }()
	wg.Wait()
	if b.err != nil {
		t.Fatalf("record ref: %v", b.err)
	}
	t.Logf("QPAIR ref: %d AUs %.1f fps avg=%.0f kbps (HEVC CQP10)", b.frames, float64(b.frames)/b.dur.Seconds(), b.bytes*8/1000/b.dur.Seconds())
	for i, q := range qs {
		a := as[i]
		if a.err != nil {
			t.Fatalf("record q%d: %v", q, a.err)
		}
		st := encs[i].(medialink.PipelineReporter).PipeStats()
		t.Logf("QPAIR codec=%s q=%d targetQP=%d ceil=%d  test: %d AUs %.1f fps avg=%.0f kbps max1s=%.0f kbps avgQP=%.1f RL=%v",
			medialink.CodecName(codec), q, medialink.QualityToQP(codec, q), kbps,
			a.frames, float64(a.frames)/a.dur.Seconds(), a.bytes*8/1000/a.dur.Seconds(), a.maxWinKbps,
			st.Feedback.AvgQP, st.Feedback.RateLimited)
	}
}
