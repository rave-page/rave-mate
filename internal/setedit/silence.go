// Package setedit detects trim points for a recorded set: leading/trailing silence (ffmpeg
// silencedetect) and track-based bounds. Used by the Publish-tab trim editor to auto-set IN/OUT.
package setedit

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// Silence is the detected music region of a recording: music starts at LeadEndSec (end of any
// leading silence) and the trailing dead-air starts at TailStartSec (== total when there is none).
type Silence struct {
	LeadEndSec   float64
	TailStartSec float64
}

const (
	// Only the head/tail windows are decoded (a full 1-hour scan would be ~1 min); leading/trailing
	// silence is all a trim needs. Widened if the set is shorter than a window.
	leadWindowSec = 5 * 60.0
	tailWindowSec = 12 * 60.0
	noiseFloor    = "-50dB" // below this = silence (broadcast dead-air floor)
	leadMinSil    = "0.4"   // min leading-silence duration to report (s)
	tailMinSil    = "1.5"   // min trailing dead-air to report (s) - avoids clipping a quiet outro
)

var (
	reSilStart = regexp.MustCompile(`silence_start:\s*(-?[0-9.]+)`)
	reSilEnd   = regexp.MustCompile(`silence_end:\s*(-?[0-9.]+)`)
)

// DetectSilence finds the music region of path. durSec is the known total (from ffprobe / the
// recording) used to place the tail window; pass 0 to skip trailing detection. Windows are decoded
// with ffmpeg silencedetect - head for leading silence, tail for trailing dead-air.
func DetectSilence(ctx context.Context, path string, durSec float64) (Silence, error) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return Silence{}, fmt.Errorf("ffmpeg not found")
	}
	if durSec <= 0 {
		durSec = probeDuration(ctx, path) // unknown → read it from the container header (fast)
	}
	out := Silence{LeadEndSec: 0, TailStartSec: durSec}

	// Leading: decode [0, leadWindow]. If silence starts at ~0, music begins at its silence_end.
	lead, err := runSilence(ctx, ffmpeg, path, 0, leadWindowSec, leadMinSil)
	if err != nil {
		return Silence{}, err
	}
	if len(lead) > 0 && lead[0].start <= 0.5 && lead[0].end > 0 {
		out.LeadEndSec = lead[0].end
	}

	// Trailing: decode the last tailWindow. The last silence_start with NO silence_end after it =
	// dead-air running to EOF → the music/content ends there. durSec<=0 → skip (unknown length).
	if durSec > 0 {
		base := durSec - tailWindowSec
		if base < out.LeadEndSec {
			base = out.LeadEndSec // short set: don't overlap the head window
		}
		tail, err := runSilence(ctx, ffmpeg, path, base, 0, tailMinSil)
		if err != nil {
			return Silence{}, err
		}
		// Trailing dead-air = the LAST silence span that reaches EOF. ffmpeg closes trailing silence
		// with a silence_end at the stream end, so accept either an unterminated span or one whose end
		// lands within ~0.75s of the total; a silence span that ends well before EOF has audio after it
		// (not trailing) and is ignored.
		if n := len(tail); n > 0 {
			last := tail[n-1]
			endAbs := durSec
			if last.end >= 0 {
				endAbs = base + last.end
			}
			startAbs := base + last.start
			if endAbs >= durSec-0.75 && startAbs > out.LeadEndSec && startAbs < durSec {
				out.TailStartSec = startAbs
			}
		}
	}
	return out, nil
}

// probeDuration reads the media duration (seconds) from the container header via ffprobe; 0 if
// unknown/unavailable.
func probeDuration(ctx context.Context, path string) float64 {
	probe, ok := mediatools.Resolve("ffprobe")
	if !ok {
		return 0
	}
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	sec, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if sec < 0 {
		return 0
	}
	return sec
}

// silSpan is one detected silence; end<0 means it ran to the end of the analyzed window (EOF).
type silSpan struct {
	start float64
	end   float64
}

// runSilence runs ffmpeg silencedetect over [ss, ss+dur] (dur<=0 = to EOF) and returns the spans in
// order, with times relative to ss.
func runSilence(ctx context.Context, ffmpeg, path string, ss, dur float64, minSil string) ([]silSpan, error) {
	args := []string{"-hide_banner", "-nostats"}
	if ss > 0 {
		args = append(args, "-ss", strconv.FormatFloat(ss, 'f', 3, 64))
	}
	args = append(args, "-i", path)
	if dur > 0 {
		args = append(args, "-t", strconv.FormatFloat(dur, 'f', 3, 64))
	}
	args = append(args, "-map", "a:0", "-af", "silencedetect=noise="+noiseFloor+":d="+minSil, "-f", "null", "-")
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	sysexec.Hide(cmd)
	sysexec.LowPriority(cmd)
	stderr, err := cmd.StderrPipe() // silencedetect logs to stderr
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var spans []silSpan
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := reSilStart.FindStringSubmatch(line); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			spans = append(spans, silSpan{start: v, end: -1})
		} else if m := reSilEnd.FindStringSubmatch(line); m != nil && len(spans) > 0 {
			v, _ := strconv.ParseFloat(m[1], 64)
			spans[len(spans)-1].end = v
		}
	}
	if err := cmd.Wait(); err != nil {
		// ffmpeg exits non-zero on some benign muxer quirks; the parsed spans are still valid if any.
		if len(spans) == 0 && !strings.Contains(err.Error(), "exit status") {
			return nil, err
		}
	}
	return spans, nil
}
