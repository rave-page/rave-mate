package transcode

import "strings"

// Encoder selection: maps a logical codec + acceleration choice to a concrete ffmpeg
// encoder, gated on what actually works on this system (the transcode.detect result). The
// UI owns detection and resolves before dispatch; the worker stays stateless.

// vendorByAccel maps an accel choice to the encoder-name suffix ffmpeg uses.
var accelSuffix = map[string]string{
	"nvenc":        "_nvenc",
	"qsv":          "_qsv",
	"amf":          "_amf",
	"videotoolbox": "_videotoolbox",
	"vaapi":        "_vaapi",
}

// codecPrefix maps a logical codec to the ffmpeg encoder-name stem used by HW encoders.
var codecHWStem = map[string]string{
	"h264": "h264",
	"h265": "hevc",
	"av1":  "av1",
}

// softwareEncoder returns the libavcodec software encoder for a logical codec.
func softwareEncoder(codec string) string {
	switch codec {
	case "h264":
		return "libx264"
	case "h265":
		return "libx265"
	case "vp9":
		return "libvpx-vp9"
	case "av1":
		return "libsvtav1"
	default:
		return ""
	}
}

// autoVendorOrder is the HW-accel preference when Accel="auto".
var autoVendorOrder = []string{"nvenc", "qsv", "amf", "videotoolbox", "vaapi"}

// ResolveEncoder picks the concrete ffmpeg video encoder for codec×accel, gated by the
// working set (encoder-name → ok, from transcode.detect). working may be nil (then HW is
// assumed unavailable and software is used). Returns ("",false) for copy/none codecs.
func ResolveEncoder(codec, accel string, working map[string]bool) (string, bool) {
	if codec == "" || codec == "copy" || codec == "none" {
		return "", false
	}
	ok := func(name string) bool {
		if name == "" {
			return false
		}
		if working == nil {
			// No detection: trust only software encoders.
			return !isHWEncoder(name)
		}
		return working[name]
	}
	sw := softwareEncoder(codec)

	switch accel {
	case "", "auto":
		// VP9 has no common HW encoder we target → software.
		if stem := codecHWStem[codec]; stem != "" {
			for _, v := range autoVendorOrder {
				cand := stem + accelSuffix[v]
				if ok(cand) {
					return cand, true
				}
			}
		}
		if ok(sw) {
			return sw, true
		}
		return sw, sw != "" // fall back to software name even if undetected
	case "software":
		return sw, sw != ""
	default:
		stem := codecHWStem[codec]
		if stem == "" {
			return sw, sw != "" // codec has no HW path (e.g. vp9)
		}
		cand := stem + accelSuffix[accel]
		if ok(cand) {
			return cand, true
		}
		// Requested HW not working → software fallback.
		return sw, sw != ""
	}
}

// isHWEncoder reports whether an encoder name is a hardware encoder.
func isHWEncoder(name string) bool {
	for _, suf := range []string{"_nvenc", "_qsv", "_amf", "_videotoolbox", "_vaapi"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// audioEncoder returns the ffmpeg audio encoder for a logical audio codec.
func audioEncoder(codec string) string {
	switch codec {
	case "aac":
		return "aac"
	case "opus":
		return "libopus"
	case "mp3":
		return "libmp3lame"
	case "vorbis":
		return "libvorbis"
	case "flac":
		return "flac"
	case "pcm-s16le":
		return "pcm_s16le"
	case "pcm-s16be":
		return "pcm_s16be"
	default:
		return ""
	}
}
