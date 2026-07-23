package transcode

import (
	"strings"
	"testing"
)

func TestPlanGain(t *testing.T) {
	m := Measurement{I: -20, TP: -8}
	if p := PlanGain(m, -14, -1, false); p.GainDB != 6 || p.PeakCapped || p.Skipped {
		t.Errorf("raise: %+v", p)
	}
	// Lowering a hot track - peaks can't clip on the way down, never capped.
	if p := PlanGain(Measurement{I: -8, TP: -0.2}, -14, -1, false); p.GainDB != -6 || p.PeakCapped {
		t.Errorf("lower: %+v", p)
	}
	// Wanted +6 but peak headroom is only +3 → capped, no limiter.
	if p := PlanGain(Measurement{I: -20, TP: -4}, -14, -1, false); p.GainDB != 3 || !p.PeakCapped {
		t.Errorf("cap: %+v", p)
	}
	// Peak already above ceiling: left alone (gain 0), never turned down by the cap.
	if p := PlanGain(Measurement{I: -20, TP: -0.5}, -14, -1, false); p.GainDB != 0 || !p.PeakCapped {
		t.Errorf("over-ceiling: %+v", p)
	}
	// Raise-only skips tracks already at/above target.
	if p := PlanGain(Measurement{I: -10, TP: -3}, -14, -1, true); !p.Skipped {
		t.Errorf("raise-only skip: %+v", p)
	}
	if p := PlanGain(Measurement{I: -20, TP: -8}, -14, -1, true); p.Skipped || p.GainDB != 6 {
		t.Errorf("raise-only raise: %+v", p)
	}
	// Silence skips.
	if p := PlanGain(Measurement{I: -99, TP: -99}, -14, -1, false); !p.Skipped {
		t.Errorf("silence: %+v", p)
	}
	// ceiling 0 defaults to DefaultLoudnessTP (-1).
	if p := PlanGain(Measurement{I: -20, TP: -4}, -14, 0, false); p.GainDB != 3 || !p.PeakCapped {
		t.Errorf("default ceiling: %+v", p)
	}
}

func TestParseLoudnormJSON(t *testing.T) {
	out := `frame=  100 fps=0.0 ...
[Parsed_loudnorm_0 @ 000001fa3]
{
	"input_i" : "-9.81",
	"input_tp" : "-0.22",
	"input_lra" : "5.30",
	"input_thresh" : "-20.01",
	"output_i" : "-24.10",
	"normalization_type" : "dynamic",
	"target_offset" : "0.10"
}`
	m, ok := ParseLoudnormJSON(out)
	if !ok || m.I != -9.81 || m.TP != -0.22 || m.LRA != 5.3 || m.Thresh != -20.01 {
		t.Fatalf("parse: %v %+v", ok, m)
	}
	// Silence prints -inf - clamps to the JSON-safe -99 sentinel.
	m, ok = ParseLoudnormJSON(`{"input_i" : "-inf", "input_tp" : "-inf", "input_lra" : "0.00", "input_thresh" : "-inf"}`)
	if !ok || m.I != -99 || m.TP != -99 {
		t.Fatalf("silence parse: %v %+v", ok, m)
	}
	if _, ok := ParseLoudnormJSON("no json here"); ok {
		t.Fatal("garbage should not parse")
	}
	if _, ok := ParseLoudnormJSON(`{"output_i" : "-14"}`); ok {
		t.Fatal("block without input_i should not parse")
	}
}

func TestMigrateLoudness(t *testing.T) {
	p := NormalizePreset(Preset{Container: "mp3", AudioCodec: "mp3", AudioBitrateK: 320, Loudness: "music-stream"})
	if !p.LoudnessOn || p.LoudnessI != -14 || p.LoudnessTP != -1 || p.Loudness != "" {
		t.Errorf("music-stream migration: %+v", p)
	}
	p = NormalizePreset(Preset{Container: "mp3", AudioCodec: "mp3", AudioBitrateK: 320, Loudness: "broadcast"})
	if !p.LoudnessOn || p.LoudnessI != -23 || p.LoudnessTP != -2 {
		t.Errorf("broadcast migration: %+v", p)
	}
	// Already-structured presets win over a stray legacy string.
	p = NormalizePreset(Preset{Container: "mp3", AudioCodec: "mp3", AudioBitrateK: 320,
		Loudness: "music-stream", LoudnessOn: true, LoudnessI: -9})
	if p.LoudnessI != -9 {
		t.Errorf("structured wins: %+v", p)
	}
}

func TestNormalizeLoudnessClamps(t *testing.T) {
	// Copy audio can't be normalized.
	p := NormalizePreset(Preset{Container: "mkv", VideoCodec: "copy", AudioCodec: "copy", LoudnessOn: true, LoudnessI: -14})
	if p.LoudnessOn {
		t.Errorf("copy audio must clear loudness: %+v", p)
	}
	// Defaults + clamps.
	p = NormalizePreset(Preset{Container: "flac", AudioCodec: "flac", LoudnessOn: true})
	if p.LoudnessI != -14 {
		t.Errorf("default target: %+v", p)
	}
	p = NormalizePreset(Preset{Container: "flac", AudioCodec: "flac", LoudnessOn: true, LoudnessI: -90, LoudnessTP: -50})
	if p.LoudnessI != -36 || p.LoudnessTP != -9 {
		t.Errorf("clamps: %+v", p)
	}
	if p.EffectiveTP() != -9 {
		t.Errorf("effective TP: %v", p.EffectiveTP())
	}
	if (Preset{LoudnessOn: true}).EffectiveTP() != DefaultLoudnessTP {
		t.Errorf("default TP")
	}
}

func TestMeasureArgs(t *testing.T) {
	g := strings.Join(MeasureArgs("in.flac", 5, 65), " ")
	for _, want := range []string{"-ss 5.000", "-i in.flac", "-t 60.000", "-map 0:a:0", "loudnorm=print_format=json", "-f null"} {
		if !strings.Contains(g, want) {
			t.Errorf("missing %q: %s", want, g)
		}
	}
	if g := strings.Join(MeasureArgs("in.flac", 0, 0), " "); strings.Contains(g, "-ss") || strings.Contains(g, "-t ") {
		t.Errorf("no trim args expected: %s", g)
	}
}

func TestIntegrateMomentary(t *testing.T) {
	// Uniform -20 momentary → integrated ≈ -20 whatever the window.
	mom := make([]float64, 100)
	for i := range mom {
		mom[i] = -20
	}
	if v, ok := IntegrateMomentary(mom, 0.5, 0, 0); !ok || v < -20.01 || v > -19.99 {
		t.Errorf("uniform: %v %v", v, ok)
	}
	// Window selection: first half -30, second half -10 → full ≈ dominated by the loud half;
	// a window over the quiet half alone must read ≈ -30.
	for i := range mom {
		if i < 50 {
			mom[i] = -30
		} else {
			mom[i] = -10
		}
	}
	if v, ok := IntegrateMomentary(mom, 0.5, 0, 25); !ok || v < -30.01 || v > -29.99 {
		t.Errorf("quiet window: %v %v", v, ok)
	}
	if v, ok := IntegrateMomentary(mom, 0.5, 25, 0); !ok || v < -10.5 || v > -9.99 {
		t.Errorf("loud window: %v %v", v, ok)
	}
	// Relative gating: sparse loud content over silence-floor samples → the floor is gated out.
	for i := range mom {
		mom[i] = -70
	}
	mom[10], mom[11], mom[12] = -12, -12, -12
	if v, ok := IntegrateMomentary(mom, 0.5, 0, 0); !ok || v < -12.5 || v > -11.5 {
		t.Errorf("gated: %v %v", v, ok)
	}
	// All silence → no result.
	for i := range mom {
		mom[i] = -70
	}
	if _, ok := IntegrateMomentary(mom, 0.5, 0, 0); ok {
		t.Errorf("silence must not integrate")
	}
	if _, ok := IntegrateMomentary(nil, 0.5, 0, 0); ok {
		t.Errorf("empty must not integrate")
	}
}
