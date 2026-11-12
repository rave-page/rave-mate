package medialink

import "testing"

func approx(got, want, tolPct int) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d*100 <= want*tolPct
}

func TestQualityModelMatchesMeasuredPoints(t *testing.T) {
	// Seeded measured points (hardware, high-motion-ish "unknown"): 4K60 ~35/22 Mbps H.264/HEVC,
	// 1080p60 ~12/8 Mbps. Allow 10% tolerance.
	cases := []struct {
		codec           string
		w, h, fps, want int
	}{
		{"h264", 3840, 2160, 60, 35000},
		{"hevc", 3840, 2160, 60, 22000},
		{"h264", 1920, 1080, 60, 12000},
		{"hevc", 1920, 1080, 60, 8000},
	}
	for _, c := range cases {
		got := QualityTargetKbps(c.codec, "nvenc", c.w, c.h, c.fps, ContentUnknown)
		if !approx(got, c.want, 10) {
			t.Errorf("%s %dx%d@%d = %d kbps, want ~%d", c.codec, c.w, c.h, c.fps, got, c.want)
		}
	}
}

func TestQualityModelMonotonic(t *testing.T) {
	// More pixels => more bits; more fps => more bits.
	p1080 := QualityTargetKbps("h264", "nvenc", 1920, 1080, 60, ContentUnknown)
	p1440 := QualityTargetKbps("h264", "nvenc", 2560, 1440, 60, ContentUnknown)
	p2160 := QualityTargetKbps("h264", "nvenc", 3840, 2160, 60, ContentUnknown)
	if !(p1080 < p1440 && p1440 < p2160) {
		t.Fatalf("not monotonic in resolution: %d %d %d", p1080, p1440, p2160)
	}
	if QualityTargetKbps("h264", "nvenc", 1920, 1080, 30, ContentUnknown) >= p1080 {
		t.Fatal("30fps must be below 60fps")
	}
}

func TestQualityModelCodecAndFamilyOrdering(t *testing.T) {
	h264 := QualityTargetKbps("h264", "nvenc", 3840, 2160, 60, ContentUnknown)
	hevc := QualityTargetKbps("hevc", "nvenc", 3840, 2160, 60, ContentUnknown)
	av1 := QualityTargetKbps("av1", "nvenc", 3840, 2160, 60, ContentUnknown)
	if !(av1 < hevc && hevc < h264) {
		t.Fatalf("codec efficiency order wrong: av1=%d hevc=%d h264=%d", av1, hevc, h264)
	}
	sw := QualityTargetKbps("h264", "libx264", 1920, 1080, 60, ContentUnknown)
	hw := QualityTargetKbps("h264", "nvenc", 1920, 1080, 60, ContentUnknown)
	if sw <= hw {
		t.Fatalf("software x264 (%d) must need more than hardware (%d)", sw, hw)
	}
	lo := QualityTargetKbps("h264", "nvenc", 1920, 1080, 60, ContentLowMotion)
	hi := QualityTargetKbps("h264", "nvenc", 1920, 1080, 60, ContentHighMotion)
	if !(lo < hw && hw < hi) {
		t.Fatalf("content ordering wrong: low=%d unk=%d high=%d", lo, hw, hi)
	}
}

func TestQualityModelBounds(t *testing.T) {
	// Unknown inputs default to 1080p60 (NOT the floor) - the 2026-11-12 regression.
	if g := QualityTargetKbps("h264", "", 0, 0, 0, ContentUnknown); !approx(g, 12000, 10) {
		t.Fatalf("degenerate inputs must default to 1080p60 (~12000), got %d", g)
	}
	if g := QualityTargetKbps("h264", "", 7680, 4320, 120, ContentHighMotion); g != budgetCeilKbps {
		t.Fatalf("8K120 must clamp to ceiling %d, got %d", budgetCeilKbps, g)
	}
}

// TestQualityModelRobustToUnknownFPS: the live regression - a 4K source advertised at fps 0 (PipeWire
// before capture) must NOT collapse to the 1000-kbps floor; fps<=0 => nominal 60.
func TestQualityModelRobustToUnknownFPS(t *testing.T) {
	if g := QualityTargetKbps("h264", "nvenc", 3840, 2160, 0, ContentUnknown); !approx(g, 35000, 10) {
		t.Fatalf("4K@fps0 H264 = %d, want ~35000 (NOT the floor)", g)
	}
	if g := QualityTargetKbps("hevc", "nvenc", 3840, 2160, 0, ContentUnknown); !approx(g, 22000, 10) {
		t.Fatalf("4K@fps0 HEVC = %d, want ~22000", g)
	}
	// Unknown geometry AND fps => 1080p60 default, never the floor.
	if g := QualityTargetKbps("hevc", "", 0, 0, 0, ContentUnknown); g <= budgetFloorKbps {
		t.Fatalf("all-unknown must exceed the floor, got %d", g)
	}
}

// TestAdaptiveBaseRobustAtOfferTime: the offer/governor path for a 4K60 HEVC route with a 50000
// ceiling must yield ~22 Mbps and NEVER below ~15 Mbps, even when fps is unknown at offer time.
func TestAdaptiveBaseRobustAtOfferTime(t *testing.T) {
	// Unknown fps (0) must use the nominal 60 => ~22 Mbps, never the floor (the regression). A known
	// LOW fps (30) legitimately targets fewer bits - that is correct, not a regression.
	for _, fps := range []int{0, 60} {
		g := AdaptiveBaseKbps(BitrateAuto, 50000, 50000, CodecHEVC, 3840, 2160, fps)
		if g < 15000 {
			t.Fatalf("4K HEVC auto (fps=%d, ceiling 50000) = %d, must never be < ~15000", fps, g)
		}
	}
	// fps 60: ~22 Mbps (below the 50 ceiling => the model binds, not the ceiling).
	if g := AdaptiveBaseKbps(BitrateAuto, 50000, 50000, CodecHEVC, 3840, 2160, 60); !approx(g, 22000, 12) {
		t.Fatalf("4K60 HEVC auto = %d, want ~22000", g)
	}
}

func TestStepFixedModeUnchanged(t *testing.T) {
	var ab AdaptiveBudget
	d := ab.Step(BudgetInputs{Mode: BitrateFixed, FixedKbps: 50000, NominalW: 3840, NominalH: 2160, FPS: 60})
	if d.TargetKbps != 50000 || d.Reason != "fixed" {
		t.Fatalf("fixed mode must return the configured value verbatim: %+v", d)
	}
	// Fixed with no value => the default (today's 20 Mbps).
	d2 := ab.Step(BudgetInputs{Mode: BitrateFixed})
	if d2.TargetKbps != defaultBudgetKbps {
		t.Fatalf("fixed default = %d, want %d", d2.TargetKbps, defaultBudgetKbps)
	}
}

func TestStepAutoModelAndCeilings(t *testing.T) {
	var ab AdaptiveBudget
	base := BudgetInputs{Mode: BitrateAuto, Codec: "h264", Family: "nvenc", NominalW: 3840, NominalH: 2160, FPS: 60}
	d := ab.Step(base)
	if d.Reason != "quality-model" || !approx(d.TargetKbps, 35000, 10) {
		t.Fatalf("auto model target wrong: %+v", d)
	}
	// User ceiling binds below the model.
	du := ab.Step(withUser(base, 20000))
	if du.Reason != "user" || du.TargetKbps != 20000 {
		t.Fatalf("user ceiling must bind: %+v", du)
	}
	// Link ceiling binds below both.
	dl := ab.Step(withLink(withUser(base, 20000), 12000))
	if dl.Reason != "link" || dl.TargetKbps != 12000 {
		t.Fatalf("link ceiling must bind: %+v", dl)
	}
}

func TestStepFollowsCodedSize(t *testing.T) {
	var ab AdaptiveBudget
	base := BudgetInputs{Mode: BitrateAuto, Codec: "h264", Family: "nvenc", NominalW: 3840, NominalH: 2160, FPS: 60}
	full := ab.Step(base).TargetKbps
	capped := ab.Step(withCoded(base, 1080)).TargetKbps // a MaxHeight cap engaged
	if capped >= full {
		t.Fatalf("a coded-size cap must lower the target: full=%d capped=%d", full, capped)
	}
	// ~1080p target, aspect-preserved.
	if !approx(capped, QualityTargetKbps("h264", "nvenc", 1920, 1080, 60, ContentUnknown), 10) {
		t.Fatalf("capped target should match 1080p model: %d", capped)
	}
}

func TestStepQPBumpHysteresis(t *testing.T) {
	var ab AdaptiveBudget
	in := BudgetInputs{Mode: BitrateAuto, Codec: "h264", Family: "nvenc", NominalW: 1920, NominalH: 1080, FPS: 60,
		UserCeilKbps: 40000} // headroom above the model so a bump can raise
	model := ab.Step(in).TargetKbps
	// Rate-limited but not yet sustained => still model.
	in.Feedback = EncoderFeedback{Present: true, RateLimited: true, AvgQP: 40}
	for i := 0; i < qpBumpHiStreak-1; i++ {
		if ab.Step(in).Reason != "quality-model" {
			t.Fatal("bumped too early")
		}
	}
	d := ab.Step(in) // now sustained
	if d.Reason != "quality-rate-limited" || d.TargetKbps <= model {
		t.Fatalf("sustained rate-limit must bump the target up: %+v (model %d)", d, model)
	}
	// Comfortable again: released after the lo streak.
	in.Feedback = EncoderFeedback{Present: true, RateLimited: false, AvgQP: 20}
	for i := 0; i < qpBumpLoStreak-1; i++ {
		if ab.Step(in).Reason != "quality-rate-limited" {
			t.Fatal("released too early")
		}
	}
	if ab.Step(in).Reason != "quality-model" {
		t.Fatal("comfortable QP must release the bump")
	}
}

func TestStepNoFeedbackNeverBumps(t *testing.T) {
	var ab AdaptiveBudget
	in := BudgetInputs{Mode: BitrateAuto, Codec: "h264", Family: "nvenc", NominalW: 1920, NominalH: 1080, FPS: 60, UserCeilKbps: 40000}
	for i := 0; i < 10; i++ {
		if ab.Step(in).Reason != "quality-model" {
			t.Fatal("must not bump without encoder feedback")
		}
	}
}

func TestSplitLinkCeiling(t *testing.T) {
	// Two routes, 4K and 1080p: the 4K route (more pixels) gets the larger share, sum <= total.
	rates := []int64{3840 * 2160 * 60, 1920 * 1080 * 60}
	got := SplitLinkCeiling(60000, rates)
	if got[0] <= got[1] {
		t.Fatalf("4K route must get the larger share: %v", got)
	}
	if got[0]+got[1] > 60000 {
		t.Fatalf("split exceeds the link ceiling: %v", got)
	}
	// No pixel info => equal split.
	eq := SplitLinkCeiling(60000, []int64{0, 0})
	if eq[0] != eq[1] {
		t.Fatalf("equal split expected: %v", eq)
	}
	// No ceiling => zeros (no static split; the congestion controller probes).
	if z := SplitLinkCeiling(0, rates); z[0] != 0 || z[1] != 0 {
		t.Fatalf("no ceiling must yield zeros: %v", z)
	}
}

func TestCapBudgetToCoded(t *testing.T) {
	// budget: model(1080)=12000, model(2160)=35000 (stand-in).
	budget := func(codedH int) int {
		if codedH > 0 && codedH <= 1080 {
			return 12000
		}
		return 35000
	}
	// No cap (MaxHeight 0): bitrate untouched.
	h := RateHint{MaxBitrateKbps: 35000}
	capBudgetToCoded(&h, budget)
	if h.MaxBitrateKbps != 35000 {
		t.Fatalf("no cap must not change bitrate: %d", h.MaxBitrateKbps)
	}
	// Cap to 1080: bitrate drops to the 1080 model (12000).
	h = RateHint{MaxHeight: 1080, MaxBitrateKbps: 35000}
	capBudgetToCoded(&h, budget)
	if h.MaxBitrateKbps != 12000 {
		t.Fatalf("coded-size cap must lower bitrate to the model: %d", h.MaxBitrateKbps)
	}
	// Never raises above an already-lower hint (congestion/VRAM trim wins).
	h = RateHint{MaxHeight: 1080, MaxBitrateKbps: 5000}
	capBudgetToCoded(&h, budget)
	if h.MaxBitrateKbps != 5000 {
		t.Fatalf("must not raise a lower cap: %d", h.MaxBitrateKbps)
	}
}

func TestNormalizeBitrateModeMigration(t *testing.T) {
	// Empty (existing configs with only bitrateKbps) => auto; explicit fixed preserved.
	if NormalizeBitrateMode("") != BitrateAuto {
		t.Fatal("empty mode must migrate to auto")
	}
	if NormalizeBitrateMode("fixed") != BitrateFixed {
		t.Fatal("fixed must be preserved")
	}
	if NormalizeBitrateMode("auto") != BitrateAuto || NormalizeBitrateMode("junk") != BitrateAuto {
		t.Fatal("auto/unknown must be auto")
	}
}

func TestAdaptiveBaseKbps(t *testing.T) {
	// Fixed mode: exactly the configured value (old behaviour).
	if got := AdaptiveBaseKbps(BitrateFixed, 0, 50000, CodecH264, 3840, 2160, 60); got != 50000 {
		t.Fatalf("fixed base = %d, want 50000", got)
	}
	// Auto, no user ceiling: the pure model target (~35 Mbps H.264 4K60).
	if got := AdaptiveBaseKbps(BitrateAuto, 0, 20000, CodecH264, 3840, 2160, 60); !approx(got, 35000, 10) {
		t.Fatalf("auto base = %d, want ~35000", got)
	}
	// Migration: old config (bitrateKbps 50000, mode "") => auto with 50000 as CEILING. The 4K H.264
	// model (~35k) is below it, so the target is the model, not the ceiling.
	if got := AdaptiveBaseKbps(NormalizeBitrateMode(""), 50000, 50000, CodecH264, 3840, 2160, 60); got >= 50000 {
		t.Fatalf("migrated auto must use the model below the 50000 ceiling, got %d", got)
	}
	// User ceiling binds a low cap.
	if got := AdaptiveBaseKbps(BitrateAuto, 10000, 20000, CodecH264, 3840, 2160, 60); got != 10000 {
		t.Fatalf("user ceiling must bind: %d", got)
	}
}

func withUser(in BudgetInputs, k int) BudgetInputs  { in.UserCeilKbps = k; return in }
func withLink(in BudgetInputs, k int) BudgetInputs  { in.LinkCeilKbps = k; return in }
func withCoded(in BudgetInputs, h int) BudgetInputs { in.CodedH = h; return in }
