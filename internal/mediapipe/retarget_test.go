package mediapipe

import (
	"slices"
	"testing"
	"time"

	"rave.page/mate/internal/medialink"
)

var t0 = time.Unix(1_700_000_000, 0)

func TestRetargetSeedsAndArgs(t *testing.T) {
	for _, c := range []struct {
		enc       string
		wantScale int
	}{{"hevc_vaapi", 68}, {"h264_vaapi", 90}, {"h264_nvenc", 100}, {"libx264", 100}} {
		r := newRetarget(c.enc, 1916, 30, t0)
		if r.scalePct != c.wantScale {
			t.Errorf("%s: scale %d want %d", c.enc, r.scalePct, c.wantScale)
		}
	}
	// the offered ceiling reaches the vaapi argv pre-compensated: b:v == maxrate == 1916*0.68
	r := newRetarget("hevc_vaapi", 1916, 30, t0)
	w := r.want()
	spec := baseSpec("hevc_vaapi", medialink.CodecHEVC)
	spec.BitrateKbps, spec.FPS = w.kbps, w.fps
	args := encodeArgs(spec, false)
	if !hasPair(args, "-b:v", "1302k") || !hasPair(args, "-maxrate", "1302k") || !hasPair(args, "-rc_mode", "VBR") {
		t.Fatalf("vaapi argv does not carry the compensated ceiling: %v", args)
	}
	// fps cap lands in the declared input rate + GOP of the next child
	r.hint(1000, 15)
	w = r.want()
	spec.BitrateKbps, spec.FPS = w.kbps, w.fps
	args = encodeArgs(spec, false)
	if !hasPair(args, "-framerate", "15") || !hasPair(args, "-g", "30") || !hasPair(args, "-b:v", "680k") {
		t.Fatalf("capped spawn argv: %v", args)
	}
}

func hasPair(args []string, k, v string) bool {
	i := slices.Index(args, k)
	return i >= 0 && i+1 < len(args) && args[i+1] == v
}

func TestRetargetDecideMaterialAndDebounce(t *testing.T) {
	r := newRetarget("libx264", 2000, 30, t0)
	r.spawned(r.want(), t0)
	now := t0.Add(time.Second)
	r.hint(1800, 0) // -10%: not material
	if r.decide(now.Add(10 * time.Second)) {
		t.Fatal("10% bitrate change respawned")
	}
	r.hint(1000, 0) // -50%: material, but inside the debounce window
	if r.decide(t0.Add(2 * time.Second)) {
		t.Fatal("respawned inside the 5 s debounce")
	}
	at := t0.Add(retargetMinGap)
	if !r.decide(at) {
		t.Fatal("material change not applied after the debounce")
	}
	r.spawned(r.want(), at)
	if r.running.kbps != 1000 {
		t.Fatalf("running kbps %d", r.running.kbps)
	}
	// latest-wins inside a window: 1000 -> 3000 -> 1050 within 5 s => no respawn (1050 vs 1000 = 5%)
	r.hint(3000, 0)
	r.hint(1050, 0)
	if r.decide(at.Add(retargetMinGap)) {
		t.Fatal("non-material net change respawned")
	}
	// fps cap: 30 -> 15 material; 30 -> 29 not
	r.hint(0, 29)
	if r.decide(at.Add(2 * retargetMinGap)) {
		t.Fatal("1 fps change respawned")
	}
	r.hint(0, 15)
	if !r.decide(at.Add(3 * retargetMinGap)) {
		t.Fatal("fps cap 15 not applied")
	}
	if w := r.want(); w.fps != 15 || w.kbps != 1050 {
		t.Fatalf("want %+v", w)
	}
	r.hint(0, 0) // cleared: back to the source rate
	if w := r.want(); w.fps != 30 {
		t.Fatalf("cleared cap: fps %v", w.fps)
	}
	r.hint(0, 60) // cap above the source never raises it
	if w := r.want(); w.fps != 30 {
		t.Fatalf("cap above source: fps %v", w.fps)
	}
}

func TestRetargetClosedLoopOvershoot(t *testing.T) {
	r := newRetarget("h264_vaapi", 2000, 30, t0) // seed 90% -> spawn 1800
	r.spawned(r.want(), t0)
	r.observe(t0.Add(2*time.Second), 3300) // inside settle: ignored
	if r.scalePct != 90 {
		t.Fatalf("observed before settle: %d", r.scalePct)
	}
	now := t0.Add(retargetMinGap)
	r.observe(now, 3300) // 1.65x the ceiling (rig: ~3.2 Mbps vs 1916)
	if r.scalePct >= 90 || r.scalePct < 30 {
		t.Fatalf("overshoot not corrected: %d", r.scalePct)
	}
	if !r.decide(now) {
		t.Fatal("correction did not respawn")
	}
	r.spawned(r.want(), now)
	// predicted landing: measured scales with spawn => ~0.95x ceiling
	pred := 3300 * float64(r.running.kbps) / 1800
	if pred > 2000*1.0 || pred < 2000*0.85 {
		t.Fatalf("corrected spawn %d predicts %.0f kbps vs ceiling 2000", r.running.kbps, pred)
	}
	// static content far under the ceiling, not saturating its target: scale stays (no respawn churn)
	s := r.scalePct
	for i := 1; i <= 5; i++ {
		r.observe(now.Add(time.Duration(i)*retargetMinGap), 200)
	}
	if r.scalePct != s {
		t.Fatalf("static undershoot moved the scale %d -> %d", s, r.scalePct)
	}
	// saturating its (too low) target while under the ceiling for 3 ticks: give x1.2 back
	sat := float64(r.running.kbps)
	for i := 6; i <= 8; i++ {
		r.observe(now.Add(time.Duration(i)*retargetMinGap), sat)
	}
	if r.scalePct <= s {
		t.Fatalf("saturated-under did not raise: %d", r.scalePct)
	}
	// non-vaapi: open loop
	o := newRetarget("libx264", 2000, 30, t0)
	o.observe(t0.Add(time.Minute), 5000)
	if o.scalePct != 100 {
		t.Fatal("closed loop ran on a non-vaapi encoder")
	}
}

// fpsGate: a 30 fps source under cap 15 keeps exactly every other frame, evenly; cap above the source
// passes all; cleared passes all.
func TestFPSGate(t *testing.T) {
	run := func(srcFPS, capFPS, n int, jitter int64) (kept int, maxIv int64) {
		var g fpsGate
		period := int64(time.Second) / int64(srcFPS)
		var last int64
		for i := 1; i <= n; i++ {
			pts := int64(i)*period + int64(1e12)
			if jitter > 0 && i%2 == 0 {
				pts += jitter
			}
			if g.pass(pts, capFPS) {
				kept++
				if last > 0 && pts-last > maxIv {
					maxIv = pts - last
				}
				last = pts
			}
		}
		return
	}
	if k, iv := run(30, 15, 300, 0); k < 149 || k > 151 || iv > int64(67*time.Millisecond) {
		t.Fatalf("30->15: kept %d max iv %v", k, time.Duration(iv))
	}
	if k, _ := run(30, 15, 300, int64(4*time.Millisecond)); k < 148 || k > 152 {
		t.Fatalf("30->15 jittered: kept %d", k)
	}
	if k, _ := run(30, 20, 300, 0); k < 199 || k > 201 {
		t.Fatalf("30->20: kept %d", k)
	}
	if k, _ := run(30, 45, 300, 0); k != 300 {
		t.Fatalf("cap above source dropped: kept %d", k)
	}
	if k, _ := run(30, 0, 300, 0); k != 300 {
		t.Fatalf("no cap dropped: kept %d", k)
	}
}
