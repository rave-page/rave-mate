package webcam

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Linux enumeration (V4L2). Same shape as the dshow path: a device is addressed by its friendly
// name (stable across replug, unlike /dev/videoN) and resolved to a node at capture time. Modes
// come from `v4l2-ctl --list-formats-ext` (real per-size fps); without v4l-utils, from ffmpeg's
// `-list_formats` (sizes only, fps assumed). Parsers are pure + fixture-tested on every OS.

const v4l2SysDir = "/sys/class/video4linux"

// v4l2Node is one capture-capable video node.
type v4l2Node struct {
	Name string // friendly name, " #N" suffixed when several devices share one
	Path string // /dev/videoN
}

// v4l2Nodes lists capture nodes from sysfs. A UVC camera exposes several nodes (capture +
// metadata); `index` 0 is the capture one. Sorted by node number so " #N" suffixes are stable.
func v4l2Nodes(sysDir string) []v4l2Node {
	ents, err := os.ReadDir(sysDir)
	if err != nil {
		return nil
	}
	type raw struct {
		n    int
		name string
	}
	var rs []raw
	for _, e := range ents {
		base := e.Name()
		if !strings.HasPrefix(base, "video") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(base, "video"))
		if err != nil {
			continue
		}
		if idx, err := os.ReadFile(filepath.Join(sysDir, base, "index")); err != nil || strings.TrimSpace(string(idx)) != "0" {
			continue
		}
		name, err := os.ReadFile(filepath.Join(sysDir, base, "name"))
		if err != nil || strings.TrimSpace(string(name)) == "" {
			continue
		}
		rs = append(rs, raw{n: n, name: strings.TrimSpace(string(name))})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].n < rs[j].n })
	seen := map[string]int{}
	out := make([]v4l2Node, 0, len(rs))
	for _, r := range rs {
		seen[r.name]++
		name := r.name
		if c := seen[r.name]; c > 1 {
			name = fmt.Sprintf("%s #%d", r.name, c)
		}
		out = append(out, v4l2Node{Name: name, Path: "/dev/video" + strconv.Itoa(r.n)})
	}
	return out
}

// v4l2Path resolves a device name (or a literal /dev path) to its current capture node.
func v4l2Path(device string) (string, error) {
	if strings.HasPrefix(device, "/dev/") {
		return device, nil
	}
	for _, n := range v4l2Nodes(v4l2SysDir) {
		if n.Name == device {
			return n.Path, nil
		}
	}
	return "", fmt.Errorf("webcam: device %q not connected", device)
}

// enumerateV4L2 lists V4L2 capture devices with their modes. Nodes without a usable mode
// (metadata-only, busy beyond query) are dropped - they could never start.
func enumerateV4L2(ctx context.Context, ffmpeg string) []DeviceInfo {
	v4l2ctl, _ := exec.LookPath("v4l2-ctl")
	nodes := v4l2Nodes(v4l2SysDir)
	out := make([]DeviceInfo, 0, len(nodes))
	for _, n := range nodes {
		var modes []Mode
		if v4l2ctl != "" {
			modes = parseV4L2CtlFormats(runStdout(ctx, v4l2ctl, "-d", n.Path, "--list-formats-ext"))
		}
		if len(modes) == 0 {
			modes = parseV4L2FFmpegFormats(runFFStderr(ctx, ffmpeg,
				"-hide_banner", "-f", "v4l2", "-list_formats", "all", "-i", n.Path))
		}
		if len(modes) > 0 {
			out = append(out, DeviceInfo{Name: n.Name, Modes: modes})
		}
	}
	return out
}

// runStdout runs a tool and returns its stdout ("" on failure).
func runStdout(ctx context.Context, bin string, args ...string) string {
	cctx, cancel := context.WithTimeout(ctx, enumTimeout/2)
	defer cancel()
	b, _ := exec.CommandContext(cctx, bin, args...).Output()
	return string(b)
}

// v4l2InputFormat maps a V4L2 fourcc to the ffmpeg v4l2 -input_format token ("" = unsupported).
func v4l2InputFormat(fourcc string) string {
	switch fourcc {
	case "MJPG":
		return "mjpeg"
	case "YUYV":
		return "yuyv422"
	case "UYVY":
		return "uyvy422"
	case "NV12":
		return "nv12"
	case "H264":
		return "h264"
	}
	return ""
}

var (
	v4l2FmtRe  = regexp.MustCompile(`^\s*\[\d+\]:\s*'(.{4})'`)
	v4l2SizeRe = regexp.MustCompile(`^\s*Size:\s*Discrete\s+(\d+)x(\d+)`)
	v4l2FpsRe  = regexp.MustCompile(`\(([0-9.]+)\s*fps\)`)
	// ffmpeg -list_formats line: `Compressed:  mjpeg : Motion-JPEG : 640x480 160x90 …`
	v4l2FFRe = regexp.MustCompile(`(?:Raw|Compressed)\s*:\s*(\S+)\s*:.*:\s*((?:\d+x\d+\s*)+)$`)
)

// modeSet keeps the best mode per size: MJPEG beats anything else (sustains its advertised fps
// over USB-2, see parseDshowOptions); within the same class the fastest wins.
type modeSet map[[2]int]Mode

func (s modeSet) offer(w, h int, fps float64, inFmt string) {
	if w <= 0 || h <= 0 || fps <= 0 || inFmt == "" {
		return
	}
	k := [2]int{w, h}
	c, ok := s[k]
	newMJ, oldMJ := inFmt == "mjpeg", c.InputFormat == "mjpeg"
	if !ok || (newMJ && !oldMJ) || (newMJ == oldMJ && fps > c.FPS) {
		s[k] = Mode{W: w, H: h, FPS: fps, InputFormat: inFmt}
	}
}

// sorted returns the modes largest-first then fastest-first (the order pickMode relies on).
func (s modeSet) sorted() []Mode {
	out := make([]Mode, 0, len(s))
	for _, m := range s {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].W*out[i].H != out[j].W*out[j].H {
			return out[i].W*out[i].H > out[j].W*out[j].H
		}
		if out[i].FPS != out[j].FPS {
			return out[i].FPS > out[j].FPS
		}
		return out[i].W > out[j].W
	})
	return out
}

// parseV4L2CtlFormats parses `v4l2-ctl --list-formats-ext` (format → size → interval nesting).
func parseV4L2CtlFormats(stdout string) []Mode {
	set := modeSet{}
	inFmt := ""
	w, h := 0, 0
	for _, line := range strings.Split(stdout, "\n") {
		if m := v4l2FmtRe.FindStringSubmatch(line); m != nil {
			inFmt, w, h = v4l2InputFormat(m[1]), 0, 0
			continue
		}
		if m := v4l2SizeRe.FindStringSubmatch(line); m != nil {
			w, _ = strconv.Atoi(m[1])
			h, _ = strconv.Atoi(m[2])
			continue
		}
		if m := v4l2FpsRe.FindStringSubmatch(line); m != nil && strings.Contains(line, "Interval") {
			fps, _ := strconv.ParseFloat(m[1], 64)
			set.offer(w, h, fps, inFmt)
		}
	}
	return set.sorted()
}

// parseV4L2FFmpegFormats parses ffmpeg `-f v4l2 -list_formats all` stderr. It lists no frame
// rates, so every size gets defaultFPS - MJPEG still wins per size, which is the mode that holds it.
func parseV4L2FFmpegFormats(stderr string) []Mode {
	set := modeSet{}
	for _, line := range strings.Split(stderr, "\n") {
		m := v4l2FFRe.FindStringSubmatch(strings.TrimRight(line, "\r "))
		if m == nil {
			continue
		}
		for _, sz := range strings.Fields(m[2]) {
			wh := strings.SplitN(sz, "x", 2)
			w, _ := strconv.Atoi(wh[0])
			h, _ := strconv.Atoi(wh[1])
			set.offer(w, h, defaultFPS, m[1])
		}
	}
	return set.sorted()
}
