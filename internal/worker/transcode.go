package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/transcode"
)

// transcodeHandlers serves the "transcode" worker type: process-isolated ffmpeg runs so a
// crash or runaway encode can't take down the daemon. Output is always a NEW file.
func transcodeHandlers() map[string]Handler {
	return map[string]Handler{
		"transcode.ping":       tcPing,
		"transcode.encoders":   tcEncoders,
		"transcode.detect":     tcDetect,
		"transcode.silence":    tcSilence,
		"transcode.measure":    tcMeasure,
		"transcode.loudtl":     tcLoudTimeline,
		"transcode.run":        tcRun,
		"transcode.gendivider": tcGenDivider,
	}
}

type tcGenDividerIn struct {
	Output  string  `json:"output"`
	Seconds float64 `json:"seconds"`
}

// tcGenDivider synthesizes a short quiet white-noise clip (playlist group divider) via
// ffmpeg lavfi anoisesrc. Faded in/out to avoid clicks; MP3 for maximum DJ-software compat.
func tcGenDivider(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var in tcGenDividerIn
	if err := json.Unmarshal(params, &in); err != nil || in.Output == "" {
		return nil, fmt.Errorf("missing output")
	}
	if in.Seconds <= 0 || in.Seconds > 10 {
		in.Seconds = 2
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(in.Output), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-nostats", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=d=%.2f:c=white:a=0.05:r=44100", in.Seconds),
		"-af", fmt.Sprintf("volume=-24dB,afade=t=in:d=0.05,afade=t=out:st=%.2f:d=0.05", in.Seconds-0.05),
		"-c:a", "libmp3lame", "-b:a", "96k", in.Output)
	prepareCmd(cmd)
	if out, rerr := cmd.CombinedOutput(); rerr != nil {
		return nil, fmt.Errorf("ffmpeg divider: %v: %s", rerr, lastLine(string(out)))
	}
	return json.Marshal(map[string]any{"ok": true, "output": in.Output})
}

func tcPing(_ json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"pong": true, "pid": os.Getpid()})
}

func ffmpegBin() (string, error) {
	if b, ok := mediatools.Resolve("ffmpeg"); ok {
		return b, nil
	}
	return "", fmt.Errorf("ffmpeg not found (install it from Settings → Transcode, or add it to PATH)")
}

// tcEncoders reports which relevant encoders this ffmpeg build has (incl. HW).
func tcEncoders(_ json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-encoders")
	prepareCmd(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg -encoders: %w", err)
	}
	s := string(out)
	want := []string{"libx264", "libx265", "aac", "libopus", "libvorbis", "h264_nvenc", "hevc_nvenc", "av1_nvenc", "h264_qsv", "hevc_qsv", "h264_amf"}
	avail := make(map[string]bool, len(want))
	for _, w := range want {
		avail[w] = strings.Contains(s, " "+w+" ")
	}
	hw := avail["h264_nvenc"] || avail["h264_qsv"] || avail["h264_amf"]
	return json.Marshal(map[string]any{"available": avail, "hwAccel": hw})
}

type tcRunIn struct {
	Input     string            `json:"input"`
	Output    string            `json:"output"`
	PresetID  string            `json:"presetId"`
	Preset    *transcode.Preset `json:"preset,omitempty"` // resolved preset (custom/builder); wins over PresetID
	TrimStart float64           `json:"trimStart"`
	TrimEnd   float64           `json:"trimEnd"`
}

// tcRun executes one transcode to completion, emitting "progress" events (percent +
// timeSec) parsed from ffmpeg's stderr. Long-running; the supervisor keeps the worker
// busy (not idle-reaped) for the duration.
func tcRun(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in tcRunIn
	if err := json.Unmarshal(params, &in); err != nil || in.Input == "" || in.Output == "" {
		return nil, fmt.Errorf("missing input/output")
	}
	var preset transcode.Preset
	switch {
	case in.Preset != nil:
		preset = *in.Preset // resolved preset from the UI builder (HW encoder already chosen)
	case in.PresetID != "":
		p, ok := transcode.Find(in.PresetID)
		if !ok {
			return nil, fmt.Errorf("unknown preset %q", in.PresetID)
		}
		preset = p
	default:
		return nil, fmt.Errorf("missing preset/presetId")
	}
	preset = transcode.NormalizePreset(preset) // migrates legacy loudness profiles + clamps targets
	// Resolve the concrete HW/SW encoder when the caller didn't pre-resolve (the studio UI
	// does; automations / peer control / trim don't) so "auto" actually uses this machine's
	// hardware instead of silently encoding in software.
	if preset.EncoderOverride == "" {
		if enc := resolveWorkerEncoder(preset.VideoCodec, preset.Accel); enc != "" {
			preset.EncoderOverride = enc
		}
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(in.Output), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}
	job := transcode.Job{Input: in.Input, Output: in.Output, Preset: preset, TrimStart: in.TrimStart, TrimEnd: in.TrimEnd}

	// Pass 1 (loudness): measure the whole clip, plan ONE constant gain. The plan is
	// emitted before the encode so callers can show exactly what will be applied.
	var loud map[string]any
	if preset.LoudnessOn {
		m, err := measureLoudness(bin, in.Input, in.TrimStart, in.TrimEnd)
		if err != nil {
			return nil, fmt.Errorf("loudness measure: %w", err)
		}
		plan := transcode.PlanGain(m, preset.LoudnessI, preset.EffectiveTP(), preset.LoudnessRaiseOnly)
		if !plan.Skipped {
			g := plan.GainDB
			job.GainDB = &g
		}
		loud = map[string]any{
			"inputI": m.I, "inputTP": m.TP, "inputLRA": m.LRA,
			"targetI": preset.LoudnessI, "ceilingTP": preset.EffectiveTP(),
			"gainDB": plan.GainDB, "peakCapped": plan.PeakCapped, "skipped": plan.Skipped,
		}
		emit("loudness", loud)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, job.Args()...)
	prepareCmd(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	var dur, clipDur float64
	if in.TrimEnd > in.TrimStart {
		clipDur = in.TrimEnd - in.TrimStart
	}
	lastErr := ""
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	sc.Split(scanFFmpegLines)
	for sc.Scan() {
		line := sc.Text()
		if t := strings.TrimSpace(line); t != "" {
			lastErr = t
		}
		if dur == 0 {
			if d, ok := parseHMS(line, reDuration); ok {
				dur = d
			}
		}
		total := dur
		if clipDur > 0 {
			total = clipDur
		}
		if t, ok := parseHMS(line, reTime); ok && total > 0 {
			pct := t / total * 100
			if pct > 100 {
				pct = 100
			}
			emit("progress", map[string]any{"percent": pct, "timeSec": t})
		}
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg failed: %s", lastErr)
	}
	emit("progress", map[string]any{"percent": 100})
	var size int64
	if fi, _ := os.Stat(in.Output); fi != nil {
		size = fi.Size()
	}
	res := map[string]any{"ok": true, "output": in.Output, "sizeBytes": size}
	if loud != nil {
		res["loudness"] = loud
	}
	return json.Marshal(res)
}

type tcMeasureIn struct {
	Input     string  `json:"input"`
	TrimStart float64 `json:"trimStart"`
	TrimEnd   float64 `json:"trimEnd"`
}

// tcMeasure runs only the loudness measurement pass (UI source detection).
func tcMeasure(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var in tcMeasureIn
	if err := json.Unmarshal(params, &in); err != nil || in.Input == "" {
		return nil, fmt.Errorf("missing input")
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	m, err := measureLoudness(bin, in.Input, in.TrimStart, in.TrimEnd)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// measureLoudness decodes the clip's first audio stream and parses loudnorm's EBU R128
// analysis JSON from stderr. Decode-only - much faster than the encode pass.
func measureLoudness(bin, input string, trimS, trimE float64) (transcode.Measurement, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, transcode.MeasureArgs(input, trimS, trimE)...)
	prepareCmd(cmd)
	out, runErr := cmd.CombinedOutput()
	m, ok := transcode.ParseLoudnormJSON(string(out))
	if !ok {
		if runErr != nil {
			return m, fmt.Errorf("ffmpeg: %v: %s", runErr, lastLine(string(out)))
		}
		return m, fmt.Errorf("no loudnorm stats in ffmpeg output")
	}
	return m, nil
}

// ── loudness timeline (ebur128) ──

type tcLoudTLIn struct {
	Input string `json:"input"`
}

// LoudTimeline is one full-file EBU R128 pass: summary (I/LRA/TP) + the momentary
// (400ms window) loudness resampled onto a fixed grid for playhead/hover readouts.
type LoudTimeline struct {
	I    float64   `json:"i"`    // integrated loudness (LUFS)
	TP   float64   `json:"tp"`   // true peak (dBTP)
	LRA  float64   `json:"lra"`  // loudness range (LU)
	Step float64   `json:"step"` // seconds per Mom sample
	Mom  []float64 `json:"mom"`  // momentary LUFS per step (floored at -70)
}

// loudTLMaxSamples bounds Mom (grid widens for long files): 2h at 0.5s. ~14k floats
// ≈ 100KB JSON worst case - one-shot result, store-cached, not a streaming buffer.
const loudTLMaxSamples = 14400

var reEbur128Line = regexp.MustCompile(`t:\s*(-?\d+(?:\.\d+)?).*?M:\s*(-?\d+(?:\.\d+)?)`)

// tcLoudTimeline runs ffmpeg -af ebur128 over the first audio stream: per-100ms
// momentary lines from stderr + the final summary block.
func tcLoudTimeline(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var in tcLoudTLIn
	if err := json.Unmarshal(params, &in); err != nil || in.Input == "" {
		return nil, fmt.Errorf("missing input")
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-hide_banner", "-nostats", "-i", in.Input,
		"-map", "0:a:0", "-af", "ebur128=peak=true", "-vn", "-sn", "-f", "null", "-")
	prepareCmd(cmd)
	out, runErr := cmd.CombinedOutput()
	tl, ok := parseEbur128(string(out))
	if !ok {
		if runErr != nil {
			return nil, fmt.Errorf("ffmpeg: %v: %s", runErr, lastLine(string(out)))
		}
		return nil, fmt.Errorf("no ebur128 stats in ffmpeg output")
	}
	return json.Marshal(tl)
}

// parseEbur128 extracts momentary samples + the summary from ebur128 stderr.
func parseEbur128(out string) (LoudTimeline, bool) {
	tl := LoudTimeline{I: -99, TP: -99}
	var ts, ms []float64
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	inSummary := false
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Summary:") {
			inSummary = true
		}
		if inSummary {
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) >= 2 {
				switch f[0] {
				case "I:":
					tl.I, _ = strconv.ParseFloat(f[1], 64)
				case "LRA:":
					// summary has LRA: value both as range + low/high; first wins
					if tl.LRA == 0 {
						tl.LRA, _ = strconv.ParseFloat(f[1], 64)
					}
				case "Peak:":
					tl.TP, _ = strconv.ParseFloat(f[1], 64)
				}
			}
			continue
		}
		if m := reEbur128Line.FindStringSubmatch(line); m != nil {
			t, _ := strconv.ParseFloat(m[1], 64)
			mv, _ := strconv.ParseFloat(m[2], 64)
			ts = append(ts, t)
			ms = append(ms, math.Max(mv, -70))
		}
	}
	if len(ts) == 0 {
		return tl, tl.I > -99
	}
	dur := ts[len(ts)-1]
	step := 0.5
	if n := dur / step; n > loudTLMaxSamples {
		step = dur / loudTLMaxSamples
	}
	n := int(dur/step) + 1
	mom := make([]float64, n)
	for i := range mom {
		mom[i] = -70
	}
	for i, t := range ts {
		if b := int(t / step); b >= 0 && b < n && ms[i] > mom[b] {
			mom[b] = ms[i]
		}
	}
	for i := range mom {
		mom[i] = math.Round(mom[i]*10) / 10
	}
	tl.Step, tl.Mom = step, mom
	return tl, true
}

// lastLine returns the last non-empty line of s (ffmpeg's error usually).
func lastLine(s string) string {
	lines := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

var (
	reDuration = regexp.MustCompile(`Duration:\s*(\d+):(\d+):(\d+(?:\.\d+)?)`)
	reTime     = regexp.MustCompile(`time=\s*(\d+):(\d+):(\d+(?:\.\d+)?)`)
)

// parseHMS extracts a H:M:S timestamp matched by re from an ffmpeg stderr line.
func parseHMS(line string, re *regexp.Regexp) (float64, bool) {
	m := re.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	h, _ := strconv.ParseFloat(m[1], 64)
	mi, _ := strconv.ParseFloat(m[2], 64)
	s, _ := strconv.ParseFloat(m[3], 64)
	return h*3600 + mi*60 + s, true
}

// scanFFmpegLines splits on \n OR \r (ffmpeg writes progress updates with a bare \r).
func scanFFmpegLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}
