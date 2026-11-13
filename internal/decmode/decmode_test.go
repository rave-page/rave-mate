package decmode

import (
	"testing"
	"time"
)

const hwNeed = 114 // learned 4K HEVC decode on the rig

var t0 = time.Unix(10_000_000, 0)

// in builds an input; HW is admitted when free >= hwNeed + 64 (stand-in for the budget's margin).
func in(now time.Time, free uint64) Input {
	return Input{Now: now, Present: true, FreeMB: free, HWAdmit: free >= hwNeed+64, HWNeedMB: hwNeed,
		CPUNeedMB: 16, DestOK: true}
}

// drive executes any switch immediately (ok) and returns it.
func drive(c *Controller, i Input) *Switch {
	sw := c.Tick(i)
	if sw != nil {
		c.Done(i.Now, true, sw.Reason)
	}
	return sw
}

func TestHWToCPUToHWWithDwell(t *testing.T) {
	c := New(Config{}, HW, t0, "start")
	if sw := drive(c, in(t0, 4000)); sw != nil {
		t.Fatalf("healthy HW must stay: %+v", sw)
	}
	// Resolume grabs VRAM: HW refused -> immediate (forced) demotion to CPU, residency ignored.
	sw := drive(c, in(t0.Add(time.Second), 150))
	if sw == nil || sw.To != CPU || !sw.Forced || c.Mode() != CPU {
		t.Fatalf("want forced HW->CPU, got %+v mode=%s", sw, c.Mode())
	}
	// Headroom returns but below HW need + buffer (114 + 512 = 626): stays CPU.
	now := t0.Add(2 * time.Minute)
	if sw := drive(c, in(now, 600)); sw != nil {
		t.Fatalf("below promote floor must stay CPU: %+v", sw)
	}
	// Above floor: must hold for the dwell before promotion.
	now = now.Add(time.Second)
	if sw := drive(c, in(now, 2000)); sw != nil {
		t.Fatal("promotion must wait the dwell")
	}
	if sw := drive(c, in(now.Add(44*time.Second), 2000)); sw != nil {
		t.Fatal("promotion before 45 s dwell")
	}
	sw = drive(c, in(now.Add(46*time.Second), 2000))
	if sw == nil || sw.To != HW || sw.Forced || c.Mode() != HW {
		t.Fatalf("want CPU->HW after dwell, got %+v", sw)
	}
}

// Headroom oscillating around the promotion floor never promotes (the dwell clock restarts on every dip)
// and HW refusals while in CPU cause no switches at all: no flapping.
func TestNoFlappingUnderOscillatingHeadroom(t *testing.T) {
	c := New(Config{}, CPU, t0, "start")
	switches := 0
	now := t0
	for i := 0; i < 600; i++ { // 10 min at 1 Hz, free alternates 30 s above / 10 s below the floor
		now = now.Add(time.Second)
		free := uint64(2000)
		if (i/10)%4 == 3 {
			free = 300
		}
		if drive(c, in(now, free)) != nil {
			switches++
		}
	}
	if switches != 0 {
		t.Fatalf("oscillating headroom caused %d switches", switches)
	}
}

// A failed promotion keeps CPU, doubles the dwell (capped) and restarts residency.
func TestFailedPromotionBacksOff(t *testing.T) {
	c := New(Config{}, CPU, t0, "start")
	now := t0.Add(time.Minute)
	c.Tick(in(now, 3000))
	now = now.Add(46 * time.Second)
	sw := c.Tick(in(now, 3000))
	if sw == nil || sw.To != HW {
		t.Fatalf("want promotion request, got %+v", sw)
	}
	c.Done(now, false, "HW open failed (VRAM)")
	if c.Mode() != CPU || c.Status().Dwell != 90*time.Second || c.Status().FailedPromo != 1 {
		t.Fatalf("failed promotion: mode=%s dwell=%s failed=%d", c.Mode(), c.Status().Dwell, c.Status().FailedPromo)
	}
	for i := 0; i < 10; i++ { // repeated failures cap at MaxDwell
		c.dwell *= 2
	}
	c.Done(now, true, "") // no pending: ignored
	if c.Status().Mode != CPU {
		t.Fatal("Done without pending must be a no-op")
	}
	// A further request is not issued before the (longer) dwell.
	c.Tick(in(now.Add(time.Minute), 3000))
	if sw := c.Tick(in(now.Add(time.Minute+80*time.Second), 3000)); sw != nil && c.Status().Dwell > 80*time.Second {
		t.Fatalf("promotion ignored the backed-off dwell: %+v", sw)
	}
}

func TestDwellCapAndStableReset(t *testing.T) {
	c := New(Config{Dwell: time.Minute, MaxDwell: 3 * time.Minute}, CPU, t0, "start")
	now := t0
	for i := 0; i < 4; i++ {
		now = now.Add(time.Hour)
		c.Tick(in(now, 3000))
		now = now.Add(c.Status().Dwell + time.Second)
		if sw := c.Tick(in(now, 3000)); sw == nil {
			t.Fatalf("iteration %d: no promotion request", i)
		}
		c.Done(now, false, "fail")
	}
	if c.Status().Dwell != 3*time.Minute {
		t.Fatalf("dwell must cap at MaxDwell, got %s", c.Status().Dwell)
	}
	now = now.Add(time.Hour)
	c.Tick(in(now, 3000))
	now = now.Add(3*time.Minute + time.Second)
	if sw := c.Tick(in(now, 3000)); sw == nil {
		t.Fatal("no promotion")
	}
	c.Done(now, true, "ok")
	c.Tick(in(now.Add(61*time.Second), 3000)) // HW stable past StableReset
	if c.Status().Dwell != time.Minute || c.Status().FailedPromo != 0 {
		t.Fatalf("stable HW must reset back-off: dwell=%s failed=%d", c.Status().Dwell, c.Status().FailedPromo)
	}
}

// HELD only when the destination cannot exist; a lost destination forces HELD from any mode, and it
// comes back to CPU (HW refused) or HW (admitted).
func TestHeldOnlyWithoutDestination(t *testing.T) {
	c := New(Config{}, HW, t0, "start")
	i := in(t0.Add(time.Second), 20) // nearly nothing free, but the destination is kept
	if sw := drive(c, i); sw != nil && sw.To == Held {
		t.Fatal("a kept destination must never be HELD")
	}
	i = in(t0.Add(2*time.Second), 20)
	i.DestOK = false
	if sw := drive(c, i); sw == nil || sw.To != Held {
		t.Fatalf("no destination => HELD, got %+v", sw)
	}
	if sw := drive(c, in(t0.Add(3*time.Second), 150)); sw == nil || sw.To != CPU {
		t.Fatalf("destination back, HW refused => CPU, got %+v", sw)
	}
	c2 := New(Config{}, Held, t0, "start")
	if sw := drive(c2, in(t0, 4000)); sw == nil || sw.To != HW {
		t.Fatalf("destination back, HW admitted => HW, got %+v", sw)
	}
}

// No headroom reading => fail open: never demote on a blind read.
func TestUnknownHeadroomNeverDemotes(t *testing.T) {
	c := New(Config{}, HW, t0, "start")
	if sw := c.Tick(Input{Now: t0.Add(time.Hour), Present: false, DestOK: true}); sw != nil {
		t.Fatalf("blind read demoted: %+v", sw)
	}
}

func TestPendingBlocksNewSwitches(t *testing.T) {
	c := New(Config{}, HW, t0, "start")
	if c.Tick(in(t0, 100)) == nil {
		t.Fatal("expected demotion request")
	}
	if c.Tick(in(t0.Add(time.Second), 100)) != nil {
		t.Fatal("no second request while one is pending")
	}
	if c.Status().Pending != CPU {
		t.Fatal("pending not visible")
	}
}

// A fresh decoder is never fed a P-frame: the gate opens on the first keyframe.
func TestFeedGateKeyframeAligned(t *testing.T) {
	var g FeedGate
	seq := []bool{false, false, true, false, false, true, false}
	want := []bool{false, false, true, true, true, true, true}
	for i, key := range seq {
		if got := g.Admit(key); got != want[i] {
			t.Fatalf("AU %d key=%v admitted=%v want %v", i, key, got, want[i])
		}
	}
}

func TestCapHeight(t *testing.T) {
	if h := CapHeight(HW, 60, nil); h != 0 {
		t.Fatalf("HW must not cap, got %d", h)
	}
	if h := CapHeight(CPU, 60, nil); h != 720 {
		t.Fatalf("unmeasured CPU cap = %d, want 720", h)
	}
	if h := CapHeight(CPU, 60, map[int]float64{1080: 90, 720: 200}); h != 1080 {
		t.Fatalf("1080 sustains 75 fps need: got %d", h)
	}
	if h := CapHeight(CPU, 60, map[int]float64{1080: 70, 720: 140}); h != 720 {
		t.Fatalf("1080 short of 75: got %d", h)
	}
	if h := CapHeight(CPU, 60, map[int]float64{1080: 20, 720: 40, 540: 60}); h != 540 {
		t.Fatalf("all short: lowest, got %d", h)
	}
}

func TestThreadsBudget(t *testing.T) {
	cases := []struct{ cfg, cpus, want int }{{0, 16, 4}, {12, 16, 8}, {0, 4, 2}, {0, 1, 1}, {3, 32, 3}}
	for _, c := range cases {
		if got := Threads(c.cfg, c.cpus); got != c.want {
			t.Fatalf("Threads(%d,%d)=%d want %d", c.cfg, c.cpus, got, c.want)
		}
	}
}

func TestPromoteFloor(t *testing.T) {
	if f := (Config{}).PromoteFloorMB(114); f != 114+512 {
		t.Fatalf("auto buffer floor = %d", f)
	}
	if f := (Config{}).PromoteFloorMB(400); f != 400+800 {
		t.Fatalf("2x need buffer floor = %d", f)
	}
	if f := (Config{PromoteBufferMB: 100}).PromoteFloorMB(114); f != 214 {
		t.Fatalf("configured buffer floor = %d", f)
	}
}

// Incident 2026-11-13: a running HW decoder yields when OTHER apps starve. Free VRAM oscillating every
// second between 150 and 700 MB (averaged below the 700 MB floor) demotes within the starve dwell -
// before any failure - and the route then stays in CPU (no flapping) until floor + need + buffer holds.
func TestYieldToStarvedNeighboursThenPromoteAboveFloor(t *testing.T) {
	cfg := Config{SystemFloorMB: 700}
	c := New(cfg, HW, t0, "start")
	now := t0
	tick := func(free uint64) *Switch {
		now = now.Add(2 * time.Second) // watchDest cadence
		i := in(now, free)
		i.HWAdmit = true // resident pipeline: kept by admission; only the yield rule may demote it
		return drive(c, i)
	}
	var demotedAt time.Duration
	for k := 0; k < 30 && c.Mode() == HW; k++ {
		free := uint64(150)
		if k%2 == 1 {
			free = 700
		}
		if sw := tick(free); sw != nil {
			if sw.To != CPU || !sw.Forced {
				t.Fatalf("want forced yield to CPU, got %+v", sw)
			}
			demotedAt = now.Sub(t0)
		}
	}
	if c.Mode() != CPU || demotedAt > 20*time.Second {
		t.Fatalf("must demote within the starve dwell (+avg window), mode=%s after %s", c.Mode(), demotedAt)
	}
	// Same oscillation for 10 min: never promotes, never flaps.
	switches := 0
	for k := 0; k < 300; k++ {
		free := uint64(150)
		if k%2 == 1 {
			free = 700
		}
		now = now.Add(2 * time.Second)
		if drive(c, in(now, free)) != nil {
			switches++
		}
	}
	if switches != 0 {
		t.Fatalf("oscillating full card caused %d switches", switches)
	}
	// Promotion floor = 700 + 114 + max(256,114) = 1070: 1000 never promotes, 1200 after the dwell.
	if f := cfg.PromoteFloorMB(hwNeed); f != 1070 {
		t.Fatalf("promote floor %d, want 1070", f)
	}
	for k := 0; k < 60; k++ {
		now = now.Add(2 * time.Second)
		if sw := drive(c, in(now, 1000)); sw != nil {
			t.Fatalf("promoted below floor+need+buffer: %+v", sw)
		}
	}
	var promoted bool
	for k := 0; k < 40 && !promoted; k++ {
		now = now.Add(2 * time.Second)
		if sw := drive(c, in(now, 1200)); sw != nil && sw.To == HW {
			promoted = true
		}
	}
	if !promoted {
		t.Fatal("headroom above floor+need+buffer for the dwell must promote")
	}
}

// A brief dip below the floor (shorter than the starve dwell, averaged) does not demote.
func TestNoYieldOnBriefDip(t *testing.T) {
	c := New(Config{SystemFloorMB: 700}, HW, t0, "start")
	now := t0
	for k := 0; k < 60; k++ {
		now = now.Add(2 * time.Second)
		free := uint64(2000)
		if k%20 == 0 {
			free = 100 // one 2 s sample far below, average stays high
		}
		i := in(now, free)
		i.HWAdmit = true
		if sw := drive(c, i); sw != nil {
			t.Fatalf("brief dip demoted: %+v (avg %d)", sw, c.Status().AvgFreeMB)
		}
	}
}
