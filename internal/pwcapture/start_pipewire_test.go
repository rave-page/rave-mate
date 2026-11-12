//go:build linux && pipewire

package pwcapture

import (
	"context"
	"os"
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
