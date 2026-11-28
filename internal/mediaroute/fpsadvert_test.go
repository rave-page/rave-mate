package mediaroute

import (
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/medialink"
)

// TestScanAdvertisesMeasuredFPS: a sender publishing a Spout frame counter is advertised at the
// assumed 60 first, then at its MEASURED rate; a static spell keeps the rate; a counter-less sender
// stays at the assumed 60; the MediaLink fps cap still wins.
func TestScanAdvertisesMeasuredFPS(t *testing.T) {
	fr := newFakeRouter()
	m := testManager(fr, config.MediaLinkFeature{ShareVideo: true}, []string{"OBS", "Blind"})
	clock := time.Unix(1000, 0)
	m.now = func() time.Time { return clock }
	var obs int64 = 100
	m.senderFrame = func(n string) (int64, bool) {
		if n == "OBS" {
			return obs, true
		}
		return 0, false
	}
	adv := func(id string) float64 {
		fr.mu.Lock()
		defer fr.mu.Unlock()
		return fr.sources[id].FPS
	}
	step := func(frames int64) {
		clock = clock.Add(scanEvery)
		obs += frames
		m.scan()
	}
	m.scan()
	if adv("spout:OBS") != 60 || adv("spout:Blind") != 60 {
		t.Fatalf("initial advert: OBS=%v Blind=%v, want 60/60", adv("spout:OBS"), adv("spout:Blind"))
	}
	step(60) // 30 fps over the 2 s scan
	if got := adv("spout:OBS"); got != 30 {
		t.Fatalf("measured advert = %v, want 30", got)
	}
	step(0) // static picture: not evidence of a slow source
	if got := adv("spout:OBS"); got != 30 {
		t.Fatalf("static spell changed the advert to %v", got)
	}
	step(61) // 30.5 fps: inside hysteresis, no re-advert churn
	if got := adv("spout:OBS"); got != 30 {
		t.Fatalf("jitter re-advertised %v", got)
	}
	step(400) // 200 fps producer: clamped to the 60 ceiling
	if got := adv("spout:OBS"); got != 60 {
		t.Fatalf("fast producer advert = %v, want ceiling 60", got)
	}
	if got := adv("spout:Blind"); got != 60 {
		t.Fatalf("counter-less sender advert = %v, want assumed 60", got)
	}

	capped := testManager(newFakeRouter(), config.MediaLinkFeature{ShareVideo: true, MaxFPS: 24}, nil)
	capped.now = m.now
	capped.senderFrame = m.senderFrame
	fps, _, _ := capped.advertFPS("OBS", fr.sources["spout:OBS"], false)
	if fps != 24 {
		t.Fatalf("fps cap ignored: %v", fps)
	}
}

// TestScanAdvertisesCompareFPS: a counter-less sender is advertised at the rate the encoder child's
// GPU frame compare measures on a live route (no Spout setting needed); a static picture keeps it; a
// route saturated at a lower tick resets to the assumed ceiling instead of pinning the advert there.
func TestScanAdvertisesCompareFPS(t *testing.T) {
	fr := newFakeRouter()
	m := testManager(fr, config.MediaLinkFeature{ShareVideo: true}, []string{"Res"})
	clock := time.Unix(1000, 0)
	m.now = func() time.Time { return clock }
	m.senderFrame = func(string) (int64, bool) { return 0, false }
	adv := func() float64 {
		fr.mu.Lock()
		defer fr.mu.Unlock()
		return fr.sources["spout:Res"].FPS
	}
	route := func(src, tick float64) {
		fr.mu.Lock()
		fr.stats = []medialink.RouteStat{{Session: "s1", Direction: "send", Source: "spout:Res",
			Pipe: &medialink.PipelineStats{FrameDetect: "gpu-compare", SrcFPS: src, TickFPS: tick}}}
		fr.mu.Unlock()
	}
	step := func() {
		clock = clock.Add(scanEvery)
		m.scan()
	}
	m.scan()
	if got := adv(); got != 60 {
		t.Fatalf("initial advert %v, want assumed 60", got)
	}
	route(30.2, 60)
	step()
	if got := adv(); got != 30 {
		t.Fatalf("compare-measured advert %v, want 30", got)
	}
	route(0, 60) // static picture: no evidence of a slower source
	step()
	if got := adv(); got != 30 {
		t.Fatalf("static spell changed the advert to %v", got)
	}
	route(30, 30) // route ticking at 30 saw no duplicates: producer may be faster
	step()
	if got := adv(); got != 60 {
		t.Fatalf("saturated route pinned the advert at %v, want the 60 ceiling back", got)
	}
	fr.mu.Lock()
	fr.stats = []medialink.RouteStat{{Session: "s2", Direction: "send", Source: "spout:Other",
		Pipe: &medialink.PipelineStats{FrameDetect: "gpu-compare", SrcFPS: 20, TickFPS: 60}}}
	fr.mu.Unlock()
	step()
	if got := adv(); got != 60 {
		t.Fatalf("another sender's route changed the advert to %v", got)
	}
}
