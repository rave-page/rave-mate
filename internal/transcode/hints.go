package transcode

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Source-aware preset hints - ports the web Local Studio PresetEditor heuristics
// (app/src/components/desktop/PresetEditor.tsx): derive a sensible target bitrate from the
// source's measured resolution/fps/bitrate, and warn when a preset would up-encode (bigger
// file, no quality gain). Pure functions over a probed SourceInfo so they're unit-testable
// and reusable by both the studio panel and (future) batch flows.

// SourceInfo is the measured shape of an input file (from ffprobe -show_streams -show_format).
type SourceInfo struct {
	HasVideo   bool
	VideoCodec string // h264|hevc|vp9|av1|…
	Width      int
	Height     int
	FPS        float64
	VideoKbps  int // measured video bitrate (kbps), 0 = unknown

	HasAudio   bool
	AudioCodec string // aac|opus|mp3|flac|…
	AudioKbps  int    // measured audio bitrate (kbps), 0 = unknown
	SampleRate int
	Channels   int

	DurationSec float64
}

// ytBaseKbps is the H.264 base bitrate (kbps) per resolution tier - YouTube's recommended
// upload ladder. Higher codecs scale down via codecFactor.
func ytBaseKbps(height int) int {
	switch {
	case height >= 2000:
		return 40000 // 4K
	case height >= 1300:
		return 16000 // 1440p
	case height >= 950:
		return 8000 // 1080p
	case height >= 650:
		return 5000 // 720p
	default:
		return 2500 // 480p
	}
}

// codecFactor scales the H.264 base by a codec's relative efficiency.
func codecFactor(codec string) float64 {
	switch codec {
	case "h265", "vp9":
		return 0.6
	case "av1":
		return 0.5
	default:
		return 1.0
	}
}

// profileFactor scales the tier base per quality profile.
func profileFactor(profile string) float64 {
	switch profile {
	case "streaming":
		return 0.8
	case "master":
		return 3.0
	case "mobile":
		return 0.4
	default: // youtube-hq / match-source / custom
		return 1.0
	}
}

// RecommendVideoBitrateK derives a target video bitrate (kbps) for a profile given the codec
// and the target/source resolution + fps. Caps at ~source bitrate (no point up-encoding)
// except for the headroom-seeking master profile. Returns 0 for copy/none.
func RecommendVideoBitrateK(profile, codec string, height int, fps float64, srcKbps int) int {
	if codec == "" || codec == "none" || codec == "copy" {
		return 0
	}
	if profile == "match-source" {
		return srcKbps
	}
	h := height
	if h <= 0 {
		h = 1080
	}
	kbps := float64(ytBaseKbps(h))
	if fps > 30 {
		kbps *= 1.5
	}
	kbps *= codecFactor(codec)
	kbps *= profileFactor(profile)
	if srcKbps > 0 && profile != "master" {
		if capK := float64(srcKbps) * 1.05; kbps > capK {
			kbps = capK
		}
	}
	return int(kbps + 0.5)
}

// audioLadders is the per-codec selectable bitrate ladder (kbps); the last entry is the cap.
var audioLadders = map[string][]int{
	"aac":    {64, 96, 128, 160, 192, 256, 320},
	"opus":   {64, 96, 128, 160, 192, 256, 320, 384, 448, 510},
	"mp3":    {96, 128, 160, 192, 256, 320},
	"vorbis": {64, 96, 128, 160, 192, 256, 320, 384, 448, 500},
}

// AudioBitrateLadder returns the selectable bitrate ladder for a codec (nil for lossless).
func AudioBitrateLadder(codec string) []int { return audioLadders[codec] }

// RecommendAudioBitrateK suggests an audio bitrate (kbps): the ladder entry closest to the
// source bitrate, capped at the codec max; else a transparent default. 0 for lossless/copy.
func RecommendAudioBitrateK(codec string, srcKbps int) int {
	ladder := audioLadders[codec]
	if len(ladder) == 0 {
		return 0
	}
	cap := ladder[len(ladder)-1]
	if srcKbps > 0 {
		best := ladder[0]
		for _, b := range ladder {
			if abs(b-srcKbps) < abs(best-srcKbps) {
				best = b
			}
		}
		return min(best, cap)
	}
	def := 192
	if codec == "opus" || codec == "vorbis" {
		def = 256
	}
	return min(def, cap)
}

// Warning flags a preset/source mismatch. Severity is "warn" (up-encode, wasted bytes) or
// "info" (a better option exists, e.g. remux).
type Warning struct {
	Severity string
	Message  string
}

// CompareQuality warns when p would up-encode src (no quality gain) or when a remux would be
// better. Conservative - only fires on confident comparisons.
func CompareQuality(p Preset, src SourceInfo) []Warning {
	var w []Warning
	if src.HasVideo && p.VideoCodec != "" && p.VideoCodec != "none" && p.VideoCodec != "copy" {
		if p.Width > 0 && p.Height > 0 && src.Width > 0 && src.Height > 0 &&
			(p.Width > src.Width || p.Height > src.Height) {
			w = append(w, Warning{"warn", fmt.Sprintf("Upscaling %d×%d → %d×%d adds no quality.",
				src.Width, src.Height, p.Width, p.Height)})
		} else if p.Height > 0 && src.Height > 0 && p.Width == 0 && p.Height > src.Height {
			w = append(w, Warning{"warn", fmt.Sprintf("Upscaling height %dp → %dp adds no quality.",
				src.Height, p.Height)})
		}
		if p.RateMode == "bitrate" && p.BitrateK > 0 && src.VideoKbps > 0 &&
			p.BitrateK > int(float64(src.VideoKbps)*1.1) {
			w = append(w, Warning{"warn", fmt.Sprintf("Video bitrate %dk exceeds source %dk - bigger file, no gain.",
				p.BitrateK, src.VideoKbps)})
		}
		if sameVideoCodec(p.VideoCodec, src.VideoCodec) {
			w = append(w, Warning{"info", fmt.Sprintf("Source is already %s - the Lossless Remux preset keeps quality and just changes container.",
				strings.ToUpper(src.VideoCodec))})
		}
	}
	if src.HasAudio && p.AudioCodec != "" && p.AudioCodec != "none" && p.AudioCodec != "copy" {
		if p.SampleRate > 0 && src.SampleRate > 0 && p.SampleRate > src.SampleRate {
			w = append(w, Warning{"warn", fmt.Sprintf("Upsampling audio %d → %d Hz adds no quality.",
				src.SampleRate, p.SampleRate)})
		}
		if p.AudioBitrateK > 0 && src.AudioKbps > 0 && p.AudioBitrateK > int(float64(src.AudioKbps)*1.1) {
			w = append(w, Warning{"warn", fmt.Sprintf("Audio bitrate %dk exceeds source %dk.",
				p.AudioBitrateK, src.AudioKbps)})
		}
	}
	return w
}

// sameVideoCodec compares a preset codec (h265) to a probed codec name (hevc).
func sameVideoCodec(preset, probed string) bool {
	probed = strings.ToLower(probed)
	switch preset {
	case "h264":
		return probed == "h264" || probed == "avc"
	case "h265":
		return probed == "hevc" || probed == "h265"
	case "vp9":
		return probed == "vp9"
	case "av1":
		return probed == "av1"
	}
	return false
}

// ApplyProfileSrc fills rate-control + bitrate on p for a quality profile, using the source's
// measured resolution/fps/bitrate when known for an accurate target. Falls back to the
// resolution-only ApplyProfile when src is nil. Mirrors the web profile picker.
func ApplyProfileSrc(p *Preset, profile string, src *SourceInfo) {
	switch profile {
	case "master":
		p.RateMode = "crf"
		p.CRF = clampCRF(p.VideoCodec, 16)
		return
	case "custom", "":
		return
	}
	if src == nil {
		ApplyProfile(p, profile)
		return
	}
	if profile == "match-source" {
		if src.VideoKbps > 0 {
			p.RateMode, p.BitrateK = "bitrate", src.VideoKbps
		} else {
			p.RateMode, p.CRF = "crf", clampCRF(p.VideoCodec, 18)
		}
		return
	}
	h := p.Height
	if h <= 0 {
		h = src.Height
	}
	fps := p.FPS
	if fps <= 0 {
		fps = src.FPS
	}
	bk := RecommendVideoBitrateK(profile, p.VideoCodec, h, fps, src.VideoKbps)
	if bk > 0 {
		p.RateMode, p.BitrateK = "bitrate", bk
	}
}

// ParseProbe extracts a SourceInfo from ffprobe -show_streams -show_format JSON (probe.streams
// worker output). Returns false when the JSON has no usable streams.
func ParseProbe(raw []byte) (SourceInfo, bool) {
	var doc struct {
		Streams []struct {
			CodecType  string `json:"codec_type"`
			CodecName  string `json:"codec_name"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			BitRate    string `json:"bit_rate"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
			RFrameRate string `json:"r_frame_rate"`
		} `json:"streams"`
		Format struct {
			BitRate  string `json:"bit_rate"`
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return SourceInfo{}, false
	}
	var si SourceInfo
	si.DurationSec = atof(doc.Format.Duration)
	for _, s := range doc.Streams {
		switch s.CodecType {
		case "video":
			if si.HasVideo {
				continue // first video stream wins
			}
			si.HasVideo = true
			si.VideoCodec = s.CodecName
			si.Width, si.Height = s.Width, s.Height
			si.FPS = parseFrac(s.RFrameRate)
			si.VideoKbps = bpsToKbps(s.BitRate)
		case "audio":
			if si.HasAudio {
				continue
			}
			si.HasAudio = true
			si.AudioCodec = s.CodecName
			si.AudioKbps = bpsToKbps(s.BitRate)
			si.SampleRate = atoiSafe(s.SampleRate)
			si.Channels = s.Channels
		}
	}
	// Fall back to container bitrate for an unreported video bitrate (minus measured audio).
	if si.HasVideo && si.VideoKbps == 0 {
		if total := bpsToKbps(doc.Format.BitRate); total > 0 {
			si.VideoKbps = max(0, total-si.AudioKbps)
		}
	}
	return si, si.HasVideo || si.HasAudio
}

// Summary is a one-line human description of the source for the UI.
func (s SourceInfo) Summary() string {
	var parts []string
	if s.HasVideo {
		v := s.VideoCodec
		if s.Width > 0 && s.Height > 0 {
			v += fmt.Sprintf(" %d×%d", s.Width, s.Height)
		}
		if s.FPS > 0 {
			v += fmt.Sprintf(" @%.3gfps", s.FPS)
		}
		if s.VideoKbps > 0 {
			v += fmt.Sprintf(" · %s", humanKbps(s.VideoKbps))
		}
		parts = append(parts, strings.TrimSpace(v))
	}
	if s.HasAudio {
		a := s.AudioCodec
		if s.AudioKbps > 0 {
			a += fmt.Sprintf(" %dk", s.AudioKbps)
		}
		if s.SampleRate > 0 {
			a += fmt.Sprintf(" %.1fkHz", float64(s.SampleRate)/1000)
		}
		parts = append(parts, strings.TrimSpace(a))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func humanKbps(k int) string {
	if k >= 1000 {
		return fmt.Sprintf("%.1f Mbps", float64(k)/1000)
	}
	return fmt.Sprintf("%d kbps", k)
}

func bpsToKbps(s string) int {
	n := atoiSafe(s)
	if n <= 0 {
		return 0
	}
	return n / 1000
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// parseFrac parses an ffmpeg "num/den" rational (e.g. r_frame_rate "30000/1001").
func parseFrac(s string) float64 {
	if i := strings.IndexByte(s, '/'); i > 0 {
		num := atof(s[:i])
		den := atof(s[i+1:])
		if den != 0 {
			return num / den
		}
		return 0
	}
	return atof(s)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
