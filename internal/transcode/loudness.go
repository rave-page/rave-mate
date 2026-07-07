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
// same trim window the encode will use, print loudnorm stats, write nothing.
func MeasureArgs(input string, trimStart, trimEnd float64) []string {
	a := []string{"-hide_banner", "-nostats"}
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
