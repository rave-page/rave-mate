package mediapipe

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/medialink"
)

// args.go - pure ffmpeg argv builders for the encode/decode children (§3.2 LL settings: no
// B-frames, zerolatency-class tuning, ~2 s GOP, capped VBV).

// defaultBitrateKbps scales the §3.1 1080p60-HEVC-class budget (20 Mbps) by pixel rate,
// clamped to [2, 80] Mbps.
func defaultBitrateKbps(w, h int, fps float64) int {
	if fps <= 0 {
		fps = 30
	}
	ref := 1920.0 * 1080 * 60
	kbps := int(20_000 * (float64(w) * float64(h) * fps) / ref)
	if kbps < 2_000 {
		kbps = 2_000
	}
	if kbps > 80_000 {
		kbps = 80_000
	}
	return kbps
}

// gopFrames is the §3.2 ~2 s GOP.
func gopFrames(fps float64) int {
	if fps <= 0 {
		fps = 30
	}
	g := int(2 * fps)
	if g < 2 {
		g = 2
	}
	return g
}

// encodeArgs builds the raw-RGBA-stdin → bitstream-stdout child argv.
func encodeArgs(spec medialink.EncodeSpec) []string {
	fps := spec.FPS
	if fps <= 0 {
		fps = 30
	}
	kbps := spec.BitrateKbps
	if kbps <= 0 {
		kbps = defaultBitrateKbps(spec.Width, spec.Height, fps)
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-fflags", "nobuffer",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"-framerate", trimFloat(fps),
		"-i", "-", "-an",
	}
	g := strconv.Itoa(gopFrames(fps))
	br := strconv.Itoa(kbps) + "k"
	vbv := strconv.Itoa(kbps/2) + "k"
	rc := []string{"-b:v", br, "-maxrate", br, "-bufsize", vbv, "-g", g, "-bf", "0"}
	switch spec.Encoder {
	case "libx264":
		args = append(args, "-c:v", "libx264", "-preset", "superfast", "-tune", "zerolatency")
		args = append(args, rc...)
	case "h264_nvenc", "hevc_nvenc":
		args = append(args, "-c:v", spec.Encoder, "-preset", "p1", "-tune", "ull", "-delay", "0")
		args = append(args, rc...)
	case "h264_qsv", "hevc_qsv":
		args = append(args, "-c:v", spec.Encoder, "-preset", "veryfast", "-async_depth", "1")
		args = append(args, rc...)
	case "h264_amf":
		// AMF omits SPS/PPS in-band by default → the bitstream filter/decoder starves. header_spacing
		// repeats them every GOP; forced_idr makes the -g keyframes true IDRs.
		args = append(args, "-c:v", spec.Encoder, "-usage", "ultralowlatency",
			"-forced_idr", "1", "-header_spacing", g)
		args = append(args, rc...)
	case "hevc_amf":
		// AMF omits VPS/SPS/PPS by default (default header_insertion_mode -1) - the "VPS id 0 not
		// available" encode-child crash. Insert parameter sets at every IDR so the stream is
		// self-contained and the metadata bsf can parse it.
		args = append(args, "-c:v", spec.Encoder, "-usage", "ultralowlatency",
			"-forced_idr", "1", "-header_insertion_mode", "idr")
		args = append(args, rc...)
	case "mjpeg":
		args = append(args, "-c:v", "mjpeg", "-q:v", "6", "-pix_fmt", "yuvj422p")
	default: // unknown encoder: pass through with the generic rc knobs
		args = append(args, "-c:v", spec.Encoder)
		args = append(args, rc...)
	}
	// Output framing: parameter sets repeated on every keyframe (dump_extra) so a decoder can
	// (re)join mid-stream. Non-AMF also gets the {codec}_metadata filter for AUD insertion. AMF is
	// EXCLUDED from that filter: its encoder emits an imperfect elementary stream (parameter sets
	// only via -header_insertion_mode, no clean in-band VPS), and the metadata filter's strict CBS
	// parser hard-crashes the child on it ("VPS id 0 not available" → zero packets). dump_extra
	// alone just prepends extradata (no parse), so AMF frames flow. MJPEG is self-framing (SOI/EOI).
	amf := strings.HasSuffix(spec.Encoder, "_amf")
	switch spec.Codec {
	case medialink.CodecH264:
		if amf {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe", "-f", "h264")
		} else {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe,h264_metadata=aud=insert", "-f", "h264")
		}
	case medialink.CodecHEVC:
		if amf {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe", "-f", "hevc")
		} else {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe,hevc_metadata=aud=insert", "-f", "hevc")
		}
	case medialink.CodecJPEG:
		args = append(args, "-f", "mjpeg")
	}
	return append(args, "-flush_packets", "1", "-")
}

// decodeArgs builds the bitstream-stdin → raw-RGBA-stdout child argv. hwaccel "" = software.
// The explicit output size pins the rawvideo pipe framing (advert dims == coded dims → no-op).
func decodeArgs(spec medialink.DecodeSpec, hwaccel string) []string {
	// probesize 32 (the minimum) + analyzeduration 0: the format is forced (-f), so probing
	// must not stall waiting for pipe bytes - stream params come from the in-band SPS/PPS.
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-probesize", "32", "-analyzeduration", "0",
	}
	if hwaccel != "" {
		args = append(args, "-hwaccel", hwaccel)
	}
	switch spec.Codec {
	case medialink.CodecH264:
		args = append(args, "-f", "h264")
	case medialink.CodecHEVC:
		args = append(args, "-f", "hevc")
	case medialink.CodecJPEG:
		args = append(args, "-f", "mjpeg")
	}
	args = append(args, "-i", "-", "-an",
		"-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"-f", "rawvideo", "-flush_packets", "1", "-")
	return args
}

// trimFloat renders fps without trailing zeros ("60", "29.97").
func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
