package medialink

import "testing"

func TestCongestCleanLANStable(t *testing.T) {
	c := newCongestController(congestConfig{FloorKbps: 2000, CeilKbps: 40000, BaseFPS: 60, Stream: 1})
	// Start below ceiling to prove it climbs, then holds without oscillation.
	c.target = 10000
	var owd int64 = 5_000_000 // flat 5 ms one-way delay, zero loss
	var last int
	for i := 0; i < 200; i++ {
		h := c.update(0.1, 0, owd)
		last = h.MaxBitrateKbps
	}
	if last != 40000 {
		t.Fatalf("clean LAN did not hold the ceiling: %d", last)
	}
	// Once at the ceiling it must not oscillate: every further step stays at the ceiling.
	for i := 0; i < 50; i++ {
		if h := c.update(0.1, 0, owd); h.MaxBitrateKbps != 40000 {
			t.Fatalf("oscillation at ceiling: %d", h.MaxBitrateKbps)
		}
	}
}

func TestCongest2PercentLossNoBackoff(t *testing.T) {
	c := newCongestController(congestConfig{FloorKbps: 2000, CeilKbps: 40000, BaseFPS: 60, Stream: 1})
	var owd int64 = 5_000_000
	var last int
	for i := 0; i < 200; i++ {
		last = c.update(0.1, 0.02, owd).MaxBitrateKbps // 2% loss = NACK territory
	}
	if last != 40000 {
		t.Fatalf("2%% loss backed the bitrate off (should be NACK territory): %d", last)
	}
}

func TestCongestLossBackoff(t *testing.T) {
	c := newCongestController(congestConfig{FloorKbps: 2000, CeilKbps: 40000, BaseFPS: 60, Stream: 1})
	start := c.update(0.1, 0, 5_000_000).MaxBitrateKbps
	var last int
	for i := 0; i < 10; i++ {
		last = c.update(0.1, 0.25, 5_000_000).MaxBitrateKbps // heavy loss
	}
	if last >= start {
		t.Fatalf("heavy loss did not reduce the bitrate: start=%d last=%d", start, last)
	}
}

func TestCongestDelayGradientBackoffAndRecover(t *testing.T) {
	c := newCongestController(congestConfig{FloorKbps: 2000, CeilKbps: 40000, BaseFPS: 60, Stream: 1})
	// Warm up at the ceiling.
	for i := 0; i < 50; i++ {
		c.update(0.1, 0, 5_000_000)
	}
	// A rising one-way delay (queue building toward a bandwidth cap) → over-use → decrease.
	owd := int64(5_000_000)
	for i := 0; i < 20; i++ {
		owd += 10_000_000 // +10 ms/sample climb
		c.update(0.1, 0, owd)
	}
	capped := c.Target()
	if capped >= 40000 {
		t.Fatalf("rising delay did not trigger a decrease: %d", capped)
	}
	// Delay flattens at the cap → target stabilizes (no further big drops).
	for i := 0; i < 20; i++ {
		c.update(0.1, 0, owd)
	}
	stable := c.Target()
	if stable < c.cfg.FloorKbps {
		t.Fatalf("target fell below floor: %d", stable)
	}
	// Cap lifts: delay flat (and low) again → climbs back toward the ceiling within a few seconds.
	for i := 0; i < 60; i++ {
		c.update(0.1, 0, owd)
	}
	if c.Target() <= stable {
		t.Fatalf("did not recover after the delay flattened: stable=%d now=%d", stable, c.Target())
	}
}

func TestCongestBitrateBeforeFps(t *testing.T) {
	c := newCongestController(congestConfig{FloorKbps: 2000, CeilKbps: 40000, BaseFPS: 60, Stream: 1})
	// Above the floor, congestion caps bitrate but NOT fps.
	h := c.update(0.1, 0.25, 5_000_000)
	if h.MaxFPS != 0 {
		t.Fatalf("fps reduced while bitrate was still above the floor: %d", h.MaxFPS)
	}
	// Drive to the floor with sustained heavy loss, then fps rung engages.
	var hh RateHint
	for i := 0; i < 100; i++ {
		hh = c.update(0.1, 0.5, 5_000_000)
	}
	if hh.MaxBitrateKbps != 2000 {
		t.Fatalf("bitrate did not pin at the floor: %d", hh.MaxBitrateKbps)
	}
	if hh.MaxFPS == 0 || hh.MaxFPS >= 60 {
		t.Fatalf("fps rung did not engage at the bitrate floor: %d", hh.MaxFPS)
	}
	if hh.MaxHeight != 0 {
		t.Fatal("congestion controller must never touch resolution (VRAM governor's last resort)")
	}
}

func TestMinRateHint(t *testing.T) {
	cc := RateHint{Stream: 1, MaxBitrateKbps: 15000, MaxFPS: 0}
	vram := RateHint{Stream: 1, MaxBitrateKbps: 20000, MaxFPS: 30, MaxHeight: 1080}
	got := minRateHint(cc, vram)
	if got.MaxBitrateKbps != 15000 { // cc more restrictive
		t.Fatalf("bitrate min = %d want 15000", got.MaxBitrateKbps)
	}
	if got.MaxFPS != 30 { // only vram capped fps
		t.Fatalf("fps min = %d want 30", got.MaxFPS)
	}
	if got.MaxHeight != 1080 { // only vram capped height (cc never touches it)
		t.Fatalf("height min = %d want 1080", got.MaxHeight)
	}
}
