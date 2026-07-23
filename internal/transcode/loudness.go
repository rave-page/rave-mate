package transcode

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Loudness normalization - music-first, fully transparent:
//
//	pass 1  measure the WHOLE track (ffmpeg loudnorm in analysis mode → EBU R128
//	        integrated loudness, true peak, LRA)
//	pass 2  apply ONE constant `volume=<g>dB` gain over the entire track
//
// That's it. No compression, no limiting, no dynamics processing - the mix is
// untouched, only scaled (like ReplayGain, baked into the file). The gain up is
// capped so the true peak never exceeds the ceiling: a track that can't reach the
// target without clipping is raised as far as the ceiling allows and reported as
// peak-capped, never squashed through a limiter.

// DefaultLoudnessTP is the true-peak ceiling (dBTP) used when a preset leaves it 0.
// −1 dBTP is the common streaming-delivery headroom recommendation.
const DefaultLoudnessTP = -1.0

// DefaultLoudnessI is the integrated target (LUFS) used when a preset or an override leaves it 0:
// the streaming target, i.e. the first entry of LoudnessTargets().
const DefaultLoudnessI = -14.0

// silenceGate: integrated loudness at/below this is treated as silence (skip).
const silenceGate = -70.0

// LoudnessTarget is a named integrated-loudness target with an industry hint.
type LoudnessTarget struct {
	Label string  // UI label incl. the LUFS value
	I     float64 // integrated target (LUFS)
	TP    float64 // suggested true-peak ceiling (dBTP)
}

// LoudnessTargets are the industry-standard integrated targets offered by the UI.
func LoudnessTargets() []LoudnessTarget {
	return []LoudnessTarget{
		{Label: "Streaming −14 LUFS (Spotify · YouTube · Tidal · Amazon)", I: -14, TP: -1},
		{Label: "Apple Music −16 LUFS", I: -16, TP: -1},
		{Label: "Deezer −15 LUFS", I: -15, TP: -1},
		{Label: "ReplayGain 2.0 −18 LUFS", I: -18, TP: -1},
		{Label: "EBU R128 broadcast −23 LUFS", I: -23, TP: -2},
		{Label: "Club / DJ master −8 LUFS (hot)", I: -8, TP: -0.3},
	}
}

// LoudnessAppliesTo reports whether normalization can actually run with this audio codec.
// Normalizing rewrites the samples, so it needs an audio re-encode: copy/none (and "" = no audio)
// drop it. NormalizePreset enforces this; UIs call it to warn instead of offering targets that
// silently do nothing.
func LoudnessAppliesTo(audioCodec string) bool {
	switch audioCodec {
	case "copy", "none", "":
		return false
	}
	return true
}

// ApplyLoudnessOverride overlays a per-run loudness override onto a resolved preset. Every
// override surface (automation actions, the recordings export) folds through here.
//
// Semantics (back-compat by construction):
//   - on == false → p returned untouched, so a preset that normalizes still normalizes exactly as
//     before. Anything decoded from before these fields existed carries on == false and behaves
//     identically.
//   - on == true → the override REPLACES the preset's loudness block wholesale (on/I/TP/raise-
//     only) rather than merging field-by-field: a half-overridden target (override I + preset TP)
//     is nobody's intent.
//
// There is no "force off": off means "don't override". To skip normalization, point at a preset
// that doesn't normalize.
//
// Zero values resolve to defaults: i → DefaultLoudnessI; tp stays 0 and EffectiveTP resolves it to
// DefaultLoudnessTP. NormalizePreset then clamps both to sane ranges and drops loudness entirely
// for copy/none audio codecs (see LoudnessAppliesTo).
func ApplyLoudnessOverride(p Preset, on bool, i, tp float64, raiseOnly bool) Preset {
	if !on {
		return p
	}
	p.LoudnessOn = true
	p.LoudnessI = i
	if p.LoudnessI == 0 {
		p.LoudnessI = DefaultLoudnessI
	}
	p.LoudnessTP = tp
	p.LoudnessRaiseOnly = raiseOnly
	return p
}

// IntegrateMomentary estimates integrated loudness (LUFS) over [startSec, endSec) from a
// momentary-loudness timeline (LUFS per step-second sample, e.g. the loudtl worker's Mom grid),
// using BS.1770-style two-stage gating: absolute −70 LUFS gate, then a relative gate 10 LU below
// the ungated mean. endSec <= 0 (or past the data) = to the end. ok=false when the window holds
// no audible samples. An ESTIMATE (the grid is max-resampled, not true 75%-overlap blocks) -
// exact numbers come from the ffmpeg loudnorm pass; this powers instant UI previews.
func IntegrateMomentary(mom []float64, step, startSec, endSec float64) (lufs float64, ok bool) {
	if step <= 0 || len(mom) == 0 {
		return 0, false
	}
	i0 := int(startSec / step)
	i1 := len(mom)
	if endSec > 0 {
		if n := int(math.Ceil(endSec / step)); n < i1 {
			i1 = n
		}
	}
	if i0 < 0 {
		i0 = 0
	}
	if i0 >= i1 {
		return 0, false
	}
	mean := func(gate float64) (float64, int) {
		sum, n := 0.0, 0
		for _, m := range mom[i0:i1] {
			if m > gate {
				sum += math.Pow(10, m/10)
				n++
			}
		}
		return sum, n
	}
	sum, n := mean(silenceGate)
	if n == 0 {
		return 0, false
	}
	relGate := 10*math.Log10(sum/float64(n)) - 10
	sum2, n2 := mean(relGate)
	if n2 == 0 {
		return 0, false
	}
	return 10 * math.Log10(sum2/float64(n2)), true
}

// EffectiveTP returns the preset's true-peak ceiling, defaulting 0 → DefaultLoudnessTP.
func (p Preset) EffectiveTP() float64 {
	if p.LoudnessTP == 0 {
		return DefaultLoudnessTP
	}
	return p.LoudnessTP
}

// MigrateLoudness maps the legacy one-pass loudnorm profile string onto the linear
// loudness fields. The old profiles ran ffmpeg's dynamic loudnorm - which compresses;
// the migrated presets hit the same targets with a single whole-track gain instead.
func MigrateLoudness(p Preset) Preset {
	if p.Loudness != "" && !p.LoudnessOn {
		switch p.Loudness {
		case "music-stream":
			p.LoudnessOn, p.LoudnessI, p.LoudnessTP = true, -14, -1
		case "music-master":
			p.LoudnessOn, p.LoudnessI, p.LoudnessTP = true, -16, -1
		case "broadcast":
			p.LoudnessOn, p.LoudnessI, p.LoudnessTP = true, -23, -2
		case "speech":
			p.LoudnessOn, p.LoudnessI, p.LoudnessTP = true, -16, -1.5
		}
	}
	p.Loudness = ""
	return p
}

// Measurement is the pass-1 EBU R128 result for a whole file (loudnorm analysis JSON).
type Measurement struct {
	I      float64 `json:"i"`      // integrated loudness (LUFS); -99 = silence
	TP     float64 `json:"tp"`     // true peak (dBTP)
	LRA    float64 `json:"lra"`    // loudness range (LU)
	Thresh float64 `json:"thresh"` // gating threshold (LUFS)
}

// GainPlan is the uniform gain derived from a measurement + target. Pure math -
// inspectable by the UI before any encode runs.
type GainPlan struct {
	GainDB     float64 // the single constant gain applied to the whole track
	PeakCapped bool    // wanted more gain, capped so true peak stays ≤ ceiling
	Skipped    bool    // silence, or raise-only and already at/above target
}

// PlanGain computes the whole-track gain: target − measured integrated loudness.
// Positive gain is capped at (ceiling − measured true peak) so the peak never goes
// over - less gain, never a limiter. raiseOnly skips tracks already at/above target.
func PlanGain(m Measurement, targetI, ceilingTP float64, raiseOnly bool) GainPlan {
	if ceilingTP == 0 {
		ceilingTP = DefaultLoudnessTP
	}
	if m.I <= silenceGate || math.IsNaN(m.I) {
		return GainPlan{Skipped: true}
	}
	g := targetI - m.I
	if raiseOnly && g <= 0 {
		return GainPlan{Skipped: true}
	}
	plan := GainPlan{GainDB: g}
	if g > 0 {
		if maxG := ceilingTP - m.TP; g > maxG {
			// Already-over-ceiling sources are left as-is (gain 0), not turned down.
			plan.GainDB, plan.PeakCapped = math.Max(maxG, 0), true
		}
	}
	return plan
}

// MeasureArgs builds the pass-1 ffmpeg args: decode the first audio stream over the
// same trim window the encode will use, print loudnorm stats, write nothing. Stats stay
// on: the worker parses the time= stream into measure-pass progress (a 2h set decodes
// for minutes - silent 0% read as a hang).
func MeasureArgs(input string, trimStart, trimEnd float64) []string {
	a := []string{"-hide_banner"}
	if trimStart > 0 {
		a = append(a, "-ss", ftoa(trimStart))
	}
	a = append(a, "-i", input)
	if trimEnd > trimStart {
		a = append(a, "-t", ftoa(trimEnd-trimStart))
	}
	return append(a, "-map", "0:a:0", "-af", "loudnorm=print_format=json", "-vn", "-sn", "-f", "null", "-")
}

// ParseLoudnormJSON extracts the loudnorm analysis block from ffmpeg stderr output.
// loudnorm prints values as strings ("-9.81", "-inf"); -inf maps to -99 (silence).
func ParseLoudnormJSON(out string) (Measurement, bool) {
	i := strings.LastIndex(out, "{")
	j := strings.LastIndex(out, "}")
	if i < 0 || j < i {
		return Measurement{}, false
	}
	var raw map[string]string
	if json.Unmarshal([]byte(out[i:j+1]), &raw) != nil {
		return Measurement{}, false
	}
	if _, ok := raw["input_i"]; !ok {
		return Measurement{}, false
	}
	return Measurement{
		I:      lufsVal(raw["input_i"]),
		TP:     lufsVal(raw["input_tp"]),
		LRA:    lufsVal(raw["input_lra"]),
		Thresh: lufsVal(raw["input_thresh"]),
	}, true
}

// lufsVal parses one loudnorm string value; ±inf clamp to ∓99 (JSON-safe sentinels).
func lufsVal(s string) float64 {
	switch strings.TrimSpace(s) {
	case "-inf":
		return -99
	case "inf":
		return 99
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
