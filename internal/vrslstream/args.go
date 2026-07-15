package vrslstream

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
)

// args.go - pure ffmpeg argv + output-URL builders for the VRSL video-stream push.
//
// Pipeline: raw RGBA frames on stdin -> H.264 -> RTMP (flv) or WHIP. LINEAR, FULL range, NO gamma:
// the frames carry raw DMX bytes, so swscale must map byte v -> luma v (full range 0..255), and no
// colorspace/gamma -vf may run. Short GOP (-g=fps) + no B-frames so late-joiners lock within a GOP
// and P-frame drift can't corrupt channel bytes. Rate control is CBR-ish (VRCDN/Twitch expect it).

// encoderFor maps a config encoder token to the ffmpeg encoder name. "auto" falls back to libx264
// (hardware probing is a future enhancement; x264 is universally present with ffmpeg).
func encoderFor(tok string) string {
	switch tok {
	case "nvenc":
		return "h264_nvenc"
	case "qsv":
		return "h264_qsv"
	case "amf":
		return "h264_amf"
	default: // "x264" | "auto" | anything else
		return "libx264"
	}
}

// deriveBitrateKbps scales a 1080p30 grid budget (6000 kbps) by pixel rate. The grid is mostly flat
// but has sharp 16-px cell edges, so it must not be starved; clamped [2000, 20000].
func deriveBitrateKbps(w, h, fps int) int {
	if fps <= 0 {
		fps = 30
	}
	ref := 1920.0 * 1080 * 30
	kbps := int(6000 * (float64(w) * float64(h) * float64(fps)) / ref)
	if kbps < 2000 {
		kbps = 2000
	}
	if kbps > 20000 {
		kbps = 20000
	}
	return kbps
}

// ffmpegArgs builds the raw-RGBA-stdin -> H.264 -> RTMP/WHIP argv for one push. w/h are the composed
// frame dimensions.
func ffmpegArgs(cfg config.StreamFeature, w, h int) []string {
	fps := cfg.ResolvedFPS()
	kbps := cfg.ResolvedBitrate()
	if kbps == 0 {
		kbps = deriveBitrateKbps(w, h, fps)
	}
	fpsS := strconv.Itoa(fps)
	br := strconv.Itoa(kbps) + "k"
	vbv := strconv.Itoa(2*kbps) + "k"

	args := []string{
		"-hide_banner", "-loglevel", "warning", "-fflags", "nobuffer",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-framerate", fpsS,
		"-i", "-", "-an",
	}
	// Rate control shared by every encoder (CBR-ish, short GOP, no B-frames).
	rc := []string{
		"-b:v", br, "-maxrate", br, "-bufsize", vbv,
		"-g", fpsS, "-bf", "0",
	}
	switch encoderFor(cfg.ResolvedEncoder()) {
	case "h264_nvenc":
		args = append(args, "-c:v", "h264_nvenc", "-preset", "p1", "-tune", "ull", "-rc", "cbr")
	case "h264_qsv":
		args = append(args, "-c:v", "h264_qsv", "-preset", "veryfast", "-async_depth", "1")
	case "h264_amf":
		args = append(args, "-c:v", "h264_amf", "-usage", "ultralowlatency")
	default:
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency")
	}
	args = append(args, rc...)
	// Output: yuv420p (Quest AVPro / transcoders require it) + FULL range so byte v -> luma v.
	// NO colorspace/gamma -vf (load-bearing: the frame is raw DMX bytes, not a gamma-encoded image).
	args = append(args, "-pix_fmt", "yuv420p", "-color_range", "pc")

	switch cfg.ResolvedTransport() {
	case "whip":
		args = append(args, "-f", "whip", outputURL(cfg))
	default: // rtmp
		args = append(args, "-f", "flv", outputURL(cfg))
	}
	return args
}

// outputURL builds the push target. RTMP appends the stream key as a path segment; WHIP uses the URL
// verbatim (embed any auth token in the URL - the stream key field is RTMP-only).
func outputURL(cfg config.StreamFeature) string {
	url := strings.TrimSpace(cfg.URL)
	if cfg.ResolvedTransport() == "whip" {
		return url
	}
	key := strings.TrimSpace(cfg.StreamKey)
	if key == "" {
		return url
	}
	return strings.TrimRight(url, "/") + "/" + key
}
