package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// Silence detection of ANY length (Go port of the fixed Electron probe). The old approach
// capped the scan at a fixed window so silence longer than the window read as exactly the
// window. Here:
//   - Leading: scan forward with no -t cap, stop the instant sound appears (first
//     silence_end closing a silence at t≈0) - only the silent prefix is decoded. If the
//     first detected silence isn't at t≈0, the audio opened with sound → 0.
//   - Trailing: ffmpeg can't stream backwards and areverse buffers the span, so seek to
//     duration-window and scan forward for the last unclosed (→EOF) silence; if it fills
//     the window, double the window and rescan - converges on any length, memory-bounded.

const (
	silenceInitialWindow = 30.0  // trailing scan starts here, then doubles
	silenceEdgeEpsilon   = 0.5   // a silence within this of an edge counts as edge silence
	silenceTimeout       = 180.0 // seconds per ffmpeg pass (audio decode is many× realtime)
)

type silenceIn struct {
	Path        string  `json:"path"`
	ThresholdDB float64 `json:"thresholdDb"` // e.g. -50
	MinSilence  float64 `json:"minSilence"`  // seconds, e.g. 2
}

func tcSilence(params json.RawMessage, _ EmitFunc) (json.RawMessage, error) {
	var in silenceIn
	if err := json.Unmarshal(params, &in); err != nil || in.Path == "" {
		return nil, fmt.Errorf("missing path")
	}
	if in.ThresholdDB == 0 {
		in.ThresholdDB = -50
	}
	if in.MinSilence == 0 {
		in.MinSilence = 2
	}
	bin, err := ffmpegBin()
	if err != nil {
		return nil, err
	}
	dur := probeDurationSec(in.Path)

	leading := probeLeadingSilence(bin, in.Path, in.ThresholdDB, in.MinSilence)
	trailing := 0.0
	if dur > 0 {
		trailing = probeTrailingSilence(bin, in.Path, in.ThresholdDB, in.MinSilence, dur)
	}
	return json.Marshal(map[string]any{
		"leadingSeconds":  leading,
		"trailingSeconds": trailing,
		"durationSeconds": dur,
	})
}

func probeDurationSec(path string) float64 {
	out, err := ffprobe("-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(trimLine(out), 64)
	return d
}

// probeLeadingSilence: unbounded forward scan, early-terminate.
func probeLeadingSilence(bin, path string, thresholdDB, minSilence float64) float64 {
	filter := fmt.Sprintf("silencedetect=noise=%gdB:d=%g", thresholdDB, minSilence)
	args := []string{"-hide_banner", "-nostats", "-vn", "-threads", "0", "-i", path, "-map", "0:a:0?", "-af", filter, "-f", "null", "-"}

	result := 0.0
	firstStart := -1.0
	hasFirst := false
	streamSilence(bin, args, func(ev string, t float64) bool {
		switch ev {
		case "start":
			if !hasFirst {
				hasFirst = true
				firstStart = t
				if firstStart > silenceEdgeEpsilon {
					return true // audio opened with sound → no leading silence
				}
			}
		case "end":
			if hasFirst && firstStart <= silenceEdgeEpsilon {
				result = max0(t)
				return true // sound appeared → leading silence is t
			}
		}
		return false
	})
	return result
}

// probeTrailingSilence: forward-from-seek + doubling window.
func probeTrailingSilence(bin, path string, thresholdDB, minSilence, duration float64) float64 {
	if duration <= 0 {
		return 0
	}
	filter := fmt.Sprintf("silencedetect=noise=%gdB:d=%g", thresholdDB, minSilence)
	window := silenceInitialWindow
	for {
		seek := duration - window
		if seek < 0 {
			seek = 0
		}
		args := []string{"-hide_banner", "-nostats", "-vn", "-threads", "0", "-ss", strconv.FormatFloat(seek, 'f', 3, 64), "-i", path, "-map", "0:a:0?", "-af", filter, "-f", "null", "-"}

		var starts, ends []float64
		streamSilence(bin, args, func(ev string, t float64) bool {
			if ev == "start" {
				starts = append(starts, t)
			} else {
				ends = append(ends, t)
			}
			return false // scan the whole bounded window
		})

		if len(starts) == 0 {
			return 0 // no silence near the end
		}
		if len(ends) >= len(starts) {
			return 0 // last silence closed → sound plays to EOF
		}
		lastStart := starts[len(starts)-1] // the unclosed (→EOF) silence
		if lastStart <= silenceEdgeEpsilon && seek > 0 {
			if window >= duration {
				return duration // entire file silent
			}
			window = min(window*2, duration)
			continue
		}
		return max0(duration - (seek + lastStart))
	}
}

// streamSilence runs ffmpeg and calls onEvent for each silence_start/silence_end; onEvent
// returns true to stop early (SIGKILL the process).
func streamSilence(bin string, args []string, onEvent func(ev string, t float64) bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(silenceTimeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	prepareCmd(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	sc := bufio.NewScanner(stderr)
	sc.Split(scanFFmpegLines)
	for sc.Scan() {
		line := sc.Text()
		if t, ok := matchSilence(line, "silence_start:"); ok {
			if onEvent("start", t) {
				_ = cmd.Process.Kill()
				break
			}
		} else if t, ok := matchSilence(line, "silence_end:"); ok {
			if onEvent("end", t) {
				_ = cmd.Process.Kill()
				break
			}
		}
	}
	_ = cmd.Wait()
}

func matchSilence(line, key string) (float64, bool) {
	i := indexOf(line, key)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(key):]
	rest = trimLeadSpace(rest)
	// read a float prefix
	j := 0
	for j < len(rest) {
		c := rest[j]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' || c == 'e' || c == 'E' {
			j++
		} else {
			break
		}
	}
	if j == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(rest[:j], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func max0(f float64) float64 {
	if f < 0 {
		return 0
	}
	return f
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func trimLeadSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	return s
}
