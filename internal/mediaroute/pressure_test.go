package mediaroute

import (
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gpumem"
	"rave.page/mate/internal/logbus"
)

// pressureManager: floor 700 MB (exit 956), headroom driven by *free, clock by *now.
func pressureManager(sf *sinkFactory, cfg *config.MediaLinkFeature, free *uint64, now *time.Time) *Manager {
	m := New(Options{
		Log: logbus.New(64), Router: newFakeRouter(),
		Cfg:      func() config.MediaLinkFeature { return *cfg },
		OpenSink: sf.open,
		Headroom: func() gpumem.Headroom { return gpumem.Headroom{Present: true, FreeMB: *free, BudgetMB: 12288} },
	})
	m.now = func() time.Time { return *now }
	m.pressureFloor = func() uint64 { return 700 }
	return m
}

// parkAt parks a fresh sender named linkPrefix+name at *now. 8192x8192 RGBA = 256 MiB est. texture.
func parkAt(m *Manager, sf *sinkFactory, name string, w, h int) int {
	inner, _ := sf.open(name, w, h)
	m.parkInner(linkPrefix+name, inner, w, h, m.isRemembered(linkPrefix+name))
	return len(sf.sinks) - 1
}

// tick advances the clock one scan interval and runs the release.
func tick(m *Manager, now *time.Time) {
	*now = now.Add(scanEvery)
	m.relievePressure()
}

func closed(sf *sinkFactory, i int) bool {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.sinks[i].closed > 0
}

func TestPressureNoReleaseAboveFloor(t *testing.T) {
	sf := &sinkFactory{shared: true}
	cfg := &config.MediaLinkFeature{}
	free, now := uint64(701), time.Unix(8_000_000, 0)
	m := pressureManager(sf, cfg, &free, &now)
	a := parkAt(m, sf, "A", 8192, 8192)
	now = now.Add(time.Hour)
	for i := 0; i < 20; i++ {
		tick(m, &now)
	}
	if closed(sf, a) {
		t.Fatal("free above the floor must never release a parked sender")
	}
	// Headroom unknown (governor off / no sampler): fail open, nothing released even at 0 MB.
	m.headroom = func() gpumem.Headroom { return gpumem.Headroom{} }
	for i := 0; i < 20; i++ {
		tick(m, &now)
	}
	if closed(sf, a) {
		t.Fatal("unknown headroom must not release")
	}
}

func TestPressureReleasesOldestFirstUntilExit(t *testing.T) {
	sf := &sinkFactory{shared: true}
	cfg := &config.MediaLinkFeature{}
	free, now := uint64(2000), time.Unix(9_000_000, 0)
	m := pressureManager(sf, cfg, &free, &now)
	old := parkAt(m, sf, "old", 8192, 8192) // 256 MiB est.
	now = now.Add(time.Minute)
	mid := parkAt(m, sf, "mid", 8192, 8192)
	now = now.Add(time.Minute)
	young := parkAt(m, sf, "young", 8192, 8192)
	now = now.Add(time.Minute)

	free = 650               // 306 MB short of exit 956: two 256 MiB releases needed by estimate
	for i := 0; i < 4; i++ { // dwell 6 s: first sample + 3 ticks
		tick(m, &now)
	}
	if !closed(sf, old) || !closed(sf, mid) || closed(sf, young) {
		t.Fatalf("want oldest two released (estimate reaches exit), got old=%v mid=%v young=%v",
			closed(sf, old), closed(sf, mid), closed(sf, young))
	}
	// Driver free still lags: within pressureSettle nothing more goes.
	tick(m, &now)
	if closed(sf, young) {
		t.Fatal("release inside pressureSettle must wait")
	}
	// Recovered above exit: nothing more released, state clears.
	free = 1000
	for i := 0; i < 5; i++ {
		tick(m, &now)
	}
	if closed(sf, young) || m.pressure.active {
		t.Fatal("headroom above exit must stop releases and clear pressure")
	}
	m.mu.Lock()
	_, kept := m.parked[linkPrefix+"young"]
	m.mu.Unlock()
	if !kept {
		t.Fatal("unreleased sender must stay parked")
	}
}

func TestPressureHysteresis(t *testing.T) {
	sf := &sinkFactory{shared: true}
	cfg := &config.MediaLinkFeature{}
	free, now := uint64(2000), time.Unix(10_000_000, 0)
	m := pressureManager(sf, cfg, &free, &now)
	a := parkAt(m, sf, "A", 64, 48) // tiny: one release never reaches exit by estimate
	now = now.Add(time.Minute)
	b := parkAt(m, sf, "B", 64, 48)
	now = now.Add(time.Minute)

	// Card hovering at the floor: dips shorter than the dwell never release.
	for i := 0; i < 10; i++ {
		free = 690
		tick(m, &now)
		tick(m, &now) // 4 s below
		free = 720
		tick(m, &now) // back inside the band: dwell restarts
	}
	if closed(sf, a) || closed(sf, b) || m.pressure.active {
		t.Fatal("dips shorter than pressureDwell must not enter pressure")
	}
	// Sustained below the floor: enter, release one round (oldest), then settle.
	free = 690
	for i := 0; i < 4; i++ {
		tick(m, &now)
	}
	if !closed(sf, a) {
		t.Fatal("sustained pressure must release the oldest")
	}
	// Inside the band (above floor, below exit) the state HOLDS: releasing continues after settle.
	free = 800
	tick(m, &now)
	tick(m, &now)
	if !closed(sf, b) {
		t.Fatal("pressure must hold inside the hysteresis band until free >= floor + hysteresis")
	}
	// Clears only at exit; re-entering needs a fresh dwell.
	free = 956
	tick(m, &now)
	if m.pressure.active {
		t.Fatal("free >= exit must clear pressure")
	}
	c := parkAt(m, sf, "C", 64, 48)
	now = now.Add(time.Minute)
	free = 690
	tick(m, &now)
	tick(m, &now)
	if closed(sf, c) {
		t.Fatal("re-entry must wait out a new dwell")
	}
}

func TestPressureSparesActiveRouteRememberedAndYoung(t *testing.T) {
	sf := &sinkFactory{shared: true}
	cfg := &config.MediaLinkFeature{RememberedReceives: []config.RememberedReceive{
		{Peer: "sus", SourceID: "spout:VRC", Name: "VRC", W: 64, H: 48}}}
	free, now := uint64(2000), time.Unix(11_000_000, 0)
	m := pressureManager(sf, cfg, &free, &now)
	busy := parkAt(m, sf, "busy", 64, 48)
	rem := parkAt(m, sf, "VRC", 64, 48)
	now = now.Add(time.Minute)
	m.mu.Lock()
	// A live route for "busy" (its sink about to re-adopt the parked sender) + one still establishing.
	m.receives["s1"] = Receive{Session: "s1", Peer: "sus", Name: "busy", Since: now.Add(-time.Minute), Active: true, EverUp: true}
	m.mu.Unlock()
	young := parkAt(m, sf, "young", 64, 48) // parked < pressureMinIdle

	free = 100
	for i := 0; i < 10; i++ { // 20 s: "young" stays under pressureMinIdle
		tick(m, &now)
	}
	if closed(sf, busy) {
		t.Fatal("a sender with an active route must never be released")
	}
	if closed(sf, rem) {
		t.Fatal("a remembered receive's destination must never be released under pressure")
	}
	if closed(sf, young) {
		t.Fatal("a sender parked < pressureMinIdle must be kept")
	}
	now = now.Add(pressureMinIdle)
	tick(m, &now)
	if !closed(sf, young) {
		t.Fatal("an idle unremembered sender must be released under sustained pressure")
	}
}

// Park publishes the transparent frame from ONE shared buffer: no w*h*4 blank per parked sender.
func TestParkBlankShared(t *testing.T) {
	sf := &sinkFactory{shared: true}
	m := keptManager(sf)
	s1, _ := m.openReceiveSink(linkPrefix+"A", 8, 4)
	s2, _ := m.openReceiveSink(linkPrefix+"B", 4, 2)
	_ = s1.Close()
	_ = s2.Close()
	w1, w2 := sf.sinks[0].writes[0], sf.sinks[1].writes[0]
	if len(w1) != 8*4*4 || len(w2) != 4*2*4 {
		t.Fatalf("blank lens %d/%d", len(w1), len(w2))
	}
	m.zeroMu.Lock()
	n := len(m.zero)
	m.zeroMu.Unlock()
	if n != 8*4*4 {
		t.Fatalf("shared blank len %d, want the largest sender's %d", n, 8*4*4)
	}
	if z1, z2 := m.zeroFrame(16), m.zeroFrame(64); &z1[0] != &z2[0] {
		t.Fatal("park blanks must share one buffer")
	}
	if m.zeroFrame(zeroFrameCap+1) != nil {
		t.Fatal("blank above the cap must not be allocated")
	}
}
