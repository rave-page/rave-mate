package webcam

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// Device enumeration via ffmpeg dshow (precedent: audiorec.Devices). ffmpeg prints device +
// option lists to stderr and exits non-zero - expected; we parse stderr regardless. The pure
// parsers below are unit-tested against captured fixtures.

// enumerateDevices lists dshow video devices with their capture modes (Windows-only).
func enumerateDevices(ctx context.Context) ([]DeviceInfo, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("webcam: device enumeration is Windows-only")
	}
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, fmt.Errorf("webcam: ffmpeg not found (install it in Settings → Library & media, or add to PATH)")
	}
	names := parseDshowVideoDevices(runFFStderr(ctx, ffmpeg,
		"-hide_banner", "-list_devices", "true", "-f", "dshow", "-i", "dummy"))
	out := make([]DeviceInfo, 0, len(names))
	for _, n := range names {
		modes := parseDshowOptions(runFFStderr(ctx, ffmpeg,
			"-hide_banner", "-list_options", "true", "-f", "dshow", "-i", "video="+n))
		out = append(out, DeviceInfo{Name: n, Modes: modes})
	}
	return out, nil
}

// runFFStderr runs ffmpeg and returns its stderr (non-zero exit expected for list commands).
func runFFStderr(ctx context.Context, ffmpeg string, args ...string) string {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ffmpeg, args...)
	sysexec.Hide(cmd)
	var buf strings.Builder
	cmd.Stderr = &buf
	_ = cmd.Run()
	return buf.String()
}

var (
	dshowTaggedRe  = regexp.MustCompile(`"([^"]+)"\s*\(([^)]*)\)\s*$`) // ffmpeg ≥4.3: `"Name" (video)` / `(audio, video)`
	dshowUntagRe   = regexp.MustCompile(`"([^"]+)"\s*$`)               // older: bare `"Name"` under a section header
	dshowOptModeRe = regexp.MustCompile(`min s=(\d+)x(\d+) fps=([0-9.]+)\s+max s=(\d+)x(\d+) fps=([0-9.]+)`)
)

// parseDshowVideoDevices extracts deduped video device names from `-list_devices` stderr.
// Handles both the tagged format (`"Name" (video)`) and the older sectioned one
// ("DirectShow video devices" header, bare quoted names). "Alternative name" lines are skipped.
func parseDshowVideoDevices(stderr string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	inVideoSection := false
	for _, line := range strings.Split(stderr, "\n") {
		low := strings.ToLower(line)
		switch {
		case strings.Contains(low, "directshow video devices"):
			inVideoSection = true
			continue
		case strings.Contains(low, "directshow audio devices"):
			inVideoSection = false
			continue
		case strings.Contains(low, "alternative name"):
			continue
		}
		if m := dshowTaggedRe.FindStringSubmatch(line); m != nil {
			if strings.Contains(m[2], "video") {
				add(m[1])
			}
			continue
		}
		if inVideoSection {
			if m := dshowUntagRe.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
				add(m[1])
			}
		}
	}
	return out
}

// parseDshowOptions extracts deduped capture modes (size + max fps) from `-list_options` stderr,
// sorted largest-first then fastest-first.
func parseDshowOptions(stderr string) []Mode {
	type key struct{ w, h int }
	best := map[key]float64{}
	for _, m := range dshowOptModeRe.FindAllStringSubmatch(stderr, -1) {
		w, _ := strconv.Atoi(m[4]) // max size (min==max on every real driver line)
		h, _ := strconv.Atoi(m[5])
		fps, _ := strconv.ParseFloat(m[6], 64)
		if w <= 0 || h <= 0 || fps <= 0 {
			continue
		}
		k := key{w, h}
		if fps > best[k] {
			best[k] = fps
		}
	}
	out := make([]Mode, 0, len(best))
	for k, fps := range best {
		out = append(out, Mode{W: k.w, H: k.h, FPS: fps})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].W*out[i].H != out[j].W*out[j].H {
			return out[i].W*out[i].H > out[j].W*out[j].H
		}
		return out[i].FPS > out[j].FPS
	})
	return out
}
