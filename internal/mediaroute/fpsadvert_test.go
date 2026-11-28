package mediaroute

import (
	"testing"
	"time"

	"rave.page/mate/internal/config"
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
