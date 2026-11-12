package medialink

import "strings"

// adaptbudget.go - the AUTO per-route bitrate budget (owner: "best quality value per system").
// Replaces the flat config bitrate with a continuous target = min of: the quality-MODEL bitrate
// (bits-per-pixel, follows the CODED size so a VRAM/network MaxHeight cap lowers it and a restore
// raises it), the USER ceiling, and a static LINK ceiling. The congestion controller (congest.go)
// probes up to this target and backs off on loss/delay; the DECODER-VRAM ceiling composes via the
// VRAM rateLadder's min() in the governor. The sender's encoder runs constant-quality-capped rate
// control (EncodeSpec.QualityTarget), so simple content undershoots and complex content spends up to
// the ceiling - the "fall when not needed, rise when needed" refinement for free; the QP feedback
// (EncoderFeedback) then bumps the ceiling toward the user/link limit when even the model target is
// rate-limited. Pure + hysteretic; every rule is table-tested, no GPU.

// BitrateMode selects how a route's bitrate budget is chosen.
type BitrateMode string

const (
	BitrateAuto  BitrateMode = "auto"  // adaptive: best quality for what the systems + link sustain
	BitrateFixed BitrateMode = "fixed" // exactly today: the configured bitrate is the target
)

// NormalizeBitrateMode maps a config value to a known mode ("" / junk => auto, the new default).
func NormalizeBitrateMode(s string) BitrateMode {
	if s == string(BitrateFixed) {
		return BitrateFixed
	}
	return BitrateAuto
}

// ContentClass hints how demanding the content is (scales bits-per-pixel).
type ContentClass int

const (
	ContentUnknown    ContentClass = iota // mid bpp (default)
	ContentLowMotion                      // slides / static desktop: fewer bits
	ContentHighMotion                     // VJ / game / VRChat: more bits
)

// Budget bounds + defaults.
const (
	budgetFloorKbps   = 1000  // never target below this (a useful picture)
	budgetCeilKbps    = 80000 // never target above this (sanity ceiling for 8K-class)
	defaultBudgetKbps = 20000 // fixed-mode default (matches config.Bitrate)
	qpBumpFactor      = 1.5   // rate-limited at the model target => raise the ceiling toward it
	qpBumpHiStreak    = 3     // sustained rate-limited ticks before bumping (anti-flap)
	qpBumpLoStreak    = 5     // sustained comfortable ticks before releasing the bump
)

// QualityTargetKbps is the model "good quality" bitrate for a coded geometry, in bits-per-pixel terms
// seeded from measured 4K60/1080p60 points (H.264 ~35/HEVC ~22 Mbps at 4K60; ~12/8 at 1080p60).
// Monotonic in pixel rate and fps; codec/family/content scale the bpp. Clamped to sane bounds.
func QualityTargetKbps(codec, family string, w, h, fps int, content ContentClass) int {
	// Robust to unknown inputs BY CONSTRUCTION (regression 2026-11-12: a PipeWire source advertises
	// 3840x2160@0 before a capture is active, and fps=0 collapsed the model to the 1000-kbps floor on
	// a live 4K route). An auto budget must NEVER drop below a quality-safe value for the geometry on
	// its own account - only a user ceiling / link / VRAM / congestion may, with a visible reason.
	if fps <= 0 {
		fps = 60 // unknown / variable fps (advert before capture) => nominal 60, not the floor
	}
	if w <= 0 || h <= 0 {
		w, h = 1920, 1080 // unknown geometry => a sane 1080p default, not the floor
	}
	// Unknown codec already maps to h264 (least efficient = highest bitrate) in codecBppFactor.
	bpp := bppBase(h) * codecBppFactor(codec) * familyBppFactor(family) * contentBppFactor(content)
	kbps := int(bpp * float64(w) * float64(h) * float64(fps) / 1000)
	return clampInt(kbps, budgetFloorKbps, budgetCeilKbps)
}

// QualitySanityFloorKbps is the lowest an AUTO target may fall purely on the quality model's account
// (25% of the model). Below this with no binding external reason (user/link/VRAM/congestion) is a bug
// - governors assert + log it. External reasons may still push lower (with the reason shown).
func QualitySanityFloorKbps(codec, family string, w, h, fps int, content ContentClass) int {
	return QualityTargetKbps(codec, family, w, h, fps, content) / 4
}

// bppBase is the H.264-hardware bits-per-pixel anchor, interpolated by coded height. bpp DROPS with
// resolution (compression efficiency rises), so total bits still grow with pixels - monotonic.
func bppBase(h int) float64 {
	type pt struct {
		h   int
		bpp float64
	}
	a := []pt{{720, 0.110}, {1080, 0.0965}, {1440, 0.082}, {2160, 0.0703}}
	if h <= a[0].h {
		return a[0].bpp
	}
	if h >= a[len(a)-1].h {
		return a[len(a)-1].bpp
	}
	for i := 1; i < len(a); i++ {
		if h <= a[i].h {
			f := float64(h-a[i-1].h) / float64(a[i].h-a[i-1].h)
			return a[i-1].bpp + f*(a[i].bpp-a[i-1].bpp)
		}
	}
	return a[len(a)-1].bpp
}

func codecBppFactor(codec string) float64 {
	switch codecToken(codec) {
	case "hevc":
		return 0.63 // ~37% fewer bits for equal quality (matches 22/35 at 4K)
	case "av1":
		return 0.50
	case "mjpeg":
		return 3.0 // intra-only: far more bits
	default: // h264
		return 1.0
	}
}

// familyBppFactor: a software x264 needs more bits than hardware for equal quality (brief).
func familyBppFactor(family string) float64 {
	switch {
	case containsAny(family, "libx264", "libx265", "x264", "x265", "software", "sw"):
		return 1.3
	default:
		return 1.0
	}
}

func contentBppFactor(c ContentClass) float64 {
	switch c {
	case ContentLowMotion:
		return 0.6
	case ContentHighMotion:
		return 1.3
	default:
		return 1.0
	}
}

// CodecName maps a wire codec to the quality model's codec token.
func CodecName(c Codec) string {
	switch c {
	case CodecHEVC:
		return "hevc"
	case CodecAV1:
		return "av1"
	case CodecJPEG:
		return "mjpeg"
	default:
		return "h264"
	}
}

// AdaptiveBaseKbps is the per-route BASE bitrate ceiling the governors + the offer use: the fixed
// value in fixed mode, else the quality-model target at the nominal geometry capped by the user
// ceiling. Pure; the continuous link + VRAM adaptation happens BELOW this in the governors (congest
// controller probes up to it; the VRAM ladder trims down from it).
func AdaptiveBaseKbps(mode BitrateMode, userCeilKbps, fixedKbps int, codec Codec, w, h, fps int) int {
	if NormalizeBitrateMode(string(mode)) == BitrateFixed {
		return posOr(fixedKbps, defaultBudgetKbps)
	}
	var ab AdaptiveBudget
	return ab.Step(BudgetInputs{Mode: BitrateAuto, Codec: CodecName(codec),
		NominalW: w, NominalH: h, FPS: fps, UserCeilKbps: userCeilKbps}).TargetKbps
}

// adaptiveBase is the governor's per-route base ceiling: fixed value in fixed mode, else the quality
// model at the route geometry capped by the user ceiling. The congest controller probes up to this
// and the VRAM ladder trims from it (so a resolution cap lowers the effective bitrate too).
func (rm *RouteManager) adaptiveBase(codec Codec, w, h, fps int) int {
	mode := ""
	if rm.budgetMode != nil {
		mode = rm.budgetMode()
	}
	fixed := defaultBudgetKbps
	if rm.govBaseKbps != nil {
		fixed = rm.govBaseKbps()
	}
	user := 0
	if rm.budgetUserK != nil {
		user = rm.budgetUserK()
	}
	return AdaptiveBaseKbps(BitrateMode(mode), user, fixed, codec, w, h, fps)
}

// budgetKbpsFn returns the per-route closure the governors call each tick: the adaptive model
// bitrate for a CODED height (aspect-preserved, capped by the user ceiling / fixed value), further
// capped by the static link ceiling (item 4; 0 = none). codedH<=0 or >=nominal => nominal. Lets the
// governor's bitrate follow a live resolution cap (item 2) and never exceed the link fraction.
func (rm *RouteManager) budgetKbpsFn(codec Codec, w, h, fps, linkCeilKbps int) func(codedH int) int {
	return func(codedH int) int {
		ch, cw := h, w
		if codedH > 0 && codedH < h {
			ch = codedH
			if h > 0 {
				cw = w * codedH / h
			}
		}
		b := rm.adaptiveBase(codec, cw, ch, fps)
		if linkCeilKbps > 0 && linkCeilKbps < b {
			b = linkCeilKbps
		}
		return b
	}
}

// AdaptiveBudget holds one route's QP-bump hysteresis state (not concurrency-safe: one per route,
// stepped by that route's governor goroutine).
type AdaptiveBudget struct {
	bumped   bool
	hiStreak int
	loStreak int
}

// BudgetInputs are one governor tick's inputs. CodedH follows the active MaxHeight cap (0 = nominal).
type BudgetInputs struct {
	Mode                    BitrateMode
	Codec, Family           string
	NominalW, NominalH, FPS int
	CodedH                  int // active coded height (<= NominalH under a cap); 0 = nominal
	Content                 ContentClass
	UserCeilKbps            int // config ceiling (0 = none)
	LinkCeilKbps            int // static link-speed ceiling (0 = none)
	FixedKbps               int // the configured target (fixed mode)
	Feedback                EncoderFeedback
}

// BudgetDecision is the controller output (also drives ctl explainability).
type BudgetDecision struct {
	TargetKbps int
	Reason     string // binding: "fixed" | "user" | "link" | "quality-rate-limited" | "quality-model"
	ModelKbps  int
	Mode       BitrateMode
	Feedback   EncoderFeedback
}

// Step computes this tick's bitrate target. Fixed mode returns the configured value (today's
// behaviour). Auto mode = min(user, link, model-or-bumped), with the binding ceiling as the reason.
func (ab *AdaptiveBudget) Step(in BudgetInputs) BudgetDecision {
	if NormalizeBitrateMode(string(in.Mode)) == BitrateFixed {
		return BudgetDecision{TargetKbps: posOr(in.FixedKbps, defaultBudgetKbps), Reason: "fixed", Mode: BitrateFixed}
	}
	codedH := in.CodedH
	if codedH <= 0 || codedH > in.NominalH {
		codedH = in.NominalH
	}
	codedW := in.NominalW
	if in.NominalH > 0 && codedH < in.NominalH {
		codedW = evenInt(in.NominalW * codedH / in.NominalH) // aspect-preserve, even dims
	}
	model := QualityTargetKbps(in.Codec, in.Family, codedW, codedH, in.FPS, in.Content)

	ab.updateBump(in.Feedback)
	target, reason := model, "quality-model"
	if ab.bumped {
		target, reason = int(float64(model)*qpBumpFactor), "quality-rate-limited"
	}
	if c := in.LinkCeilKbps; c > 0 && c < target {
		target, reason = c, "link"
	}
	if c := in.UserCeilKbps; c > 0 && c < target {
		target, reason = c, "user"
	}
	target = clampInt(target, budgetFloorKbps, budgetCeilKbps)
	return BudgetDecision{TargetKbps: target, Reason: reason, ModelKbps: model, Mode: BitrateAuto, Feedback: in.Feedback}
}

// updateBump drives the hysteretic QP bump: raise the ceiling toward the user/link limit after a
// sustained rate-limited run (content wants more than the model), release it after a sustained
// comfortable run. No feedback (Present=false) => never bump (the model target stands).
func (ab *AdaptiveBudget) updateBump(fb EncoderFeedback) {
	if !fb.Present {
		ab.hiStreak, ab.loStreak, ab.bumped = 0, 0, false
		return
	}
	if fb.RateLimited {
		ab.hiStreak++
		ab.loStreak = 0
		if ab.hiStreak >= qpBumpHiStreak {
			ab.bumped = true
		}
		return
	}
	ab.loStreak++
	ab.hiStreak = 0
	if ab.loStreak >= qpBumpLoStreak {
		ab.bumped = false
	}
}

// SplitLinkCeiling divides a per-link aggregate ceiling across concurrent routes by pixel-rate need
// (not N x the single-route budget). pixelRates are each route's W*H*fps; the result is each route's
// link ceiling kbps, summing to <= totalKbps. A zero/absent total returns zeros (no static split).
func SplitLinkCeiling(totalKbps int, pixelRates []int64) []int {
	out := make([]int, len(pixelRates))
	if totalKbps <= 0 || len(pixelRates) == 0 {
		return out
	}
	var sum int64
	for _, p := range pixelRates {
		if p > 0 {
			sum += p
		}
	}
	if sum <= 0 {
		share := totalKbps / len(pixelRates) // equal split when no pixel info
		for i := range out {
			out[i] = share
		}
		return out
	}
	for i, p := range pixelRates {
		if p > 0 {
			out[i] = int(int64(totalKbps) * p / sum)
		}
	}
	return out
}

// assertBudgetSane logs once (at governor start) if the model target is implausibly low for a
// compressed video route (< 2000 kbps; any >=720p route models far above this). Catches a model
// collapse like the fps=0 regression - an auto budget must never go this low on its own account.
func assertBudgetSane(rm *RouteManager, stream uint16, baseKbps int) {
	if baseKbps < 2000 {
		rm.warnf("adaptive budget: implausibly low auto target - route inputs may be unknown (fps/geometry)",
			map[string]any{"stream": stream, "baseKbps": baseKbps})
	}
}

// budgetModeStr is the route's bitrate mode for ctl ("auto"/"fixed").
func (rm *RouteManager) budgetModeStr() string {
	if rm.budgetMode != nil {
		return string(NormalizeBitrateMode(rm.budgetMode()))
	}
	return string(BitrateFixed)
}

// budgetReason names the binding constraint on the emitted hint, for ctl explainability (coarse).
func budgetReason(h RateHint, nominalKbps, baseFPS int) string {
	switch {
	case h.MaxHeight > 0:
		return "resolution-cap (vram/network)"
	case h.MaxFPS > 0 && baseFPS > 0 && h.MaxFPS < baseFPS:
		return "fps-cap (vram/network)"
	case nominalKbps > 0 && h.MaxBitrateKbps > 0 && h.MaxBitrateKbps < nominalKbps:
		return "congestion/vram (below quality target)"
	default:
		return "quality-model (target met)"
	}
}

// capBudgetToCoded lowers a RateHint's bitrate to the quality model's value for the CODED height when
// a resolution cap is active (the budget follows the coded size: a smaller coded frame needs fewer
// bits, a restore raises it). No-op when MaxHeight==0 (nominal) or budget is nil.
func capBudgetToCoded(h *RateHint, budget func(codedH int) int) {
	if h.MaxHeight <= 0 || budget == nil {
		return
	}
	if m := budget(h.MaxHeight); m > 0 && (h.MaxBitrateKbps <= 0 || m < h.MaxBitrateKbps) {
		h.MaxBitrateKbps = m
	}
}

// ── helpers ──

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func posOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func evenInt(v int) int {
	if v < 2 {
		return 2
	}
	return v &^ 1
}

func codecToken(codec string) string {
	c := strings.ToLower(codec)
	switch {
	case strings.Contains(c, "hevc"), strings.Contains(c, "265"):
		return "hevc"
	case strings.Contains(c, "av1"):
		return "av1"
	case strings.Contains(c, "mjpeg"), strings.Contains(c, "jpeg"):
		return "mjpeg"
	default:
		return "h264"
	}
}

func containsAny(s string, subs ...string) bool {
	l := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(l, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
