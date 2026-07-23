package transcode

import (
	"path/filepath"
	"strings"
)

// Containers is the UI/editor order for supported output containers.
func Containers() []string {
	return []string{"mp4", "webm", "mkv", "m4a", "mp3", "ogg", "wav", "aiff", "flac", "opus"}
}

// ResolveSourceContainer resolves a Container=="" ("source format") preset against the input
// file: the container matching the input's extension, else mkv (the muxer that carries
// anything) so a stream-copy of an exotic capture still succeeds.
func ResolveSourceContainer(p Preset, inputPath string) Preset {
	if p.Container != "" {
		return p
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(inputPath)), ".")
	switch ext {
	case "m4a", "aac", "mp4", "mov":
		if p.IsAudioOnly() {
			p.Container = "m4a"
		} else {
			p.Container = "mp4"
		}
	case "oga":
		p.Container = "ogg"
	case "aif":
		p.Container = "aiff"
	default:
		if containsString(Containers(), ext) {
			p.Container = ext
		} else {
			p.Container = "mkv"
		}
	}
	return p
}

// IsAudioOnlyContainer reports whether a container cannot carry a video stream in this app.
func IsAudioOnlyContainer(container string) bool {
	switch container {
	case "m4a", "aac", "mp3", "ogg", "wav", "aiff", "flac", "opus":
		return true
	default:
		return false
	}
}

// VideoCodecsForContainer returns the selectable logical video codecs for a container.
func VideoCodecsForContainer(container string, allowVideo bool) []string {
	if !allowVideo || IsAudioOnlyContainer(container) {
		return []string{"none"}
	}
	switch container {
	case "webm":
		return []string{"vp9", "av1", "copy", "none"}
	case "mkv":
		return []string{"h264", "h265", "vp9", "av1", "copy", "none"}
	default: // mp4 and unknown/custom containers prefer broad compatibility.
		return []string{"h264", "h265", "av1", "copy", "none"}
	}
}

// AudioCodecsForContainer returns the selectable logical audio codecs for a container.
func AudioCodecsForContainer(container string) []string {
	switch container {
	case "mp4":
		return []string{"aac", "mp3", "copy", "none"}
	case "webm":
		return []string{"opus", "vorbis", "copy", "none"}
	case "mkv":
		return []string{"aac", "opus", "mp3", "vorbis", "flac", "pcm-s16le", "pcm-s16be", "copy", "none"}
	case "m4a", "aac":
		return []string{"aac", "copy", "none"}
	case "mp3":
		return []string{"mp3", "copy", "none"}
	case "ogg":
		return []string{"vorbis", "opus", "copy", "none"}
	case "opus":
		return []string{"opus", "copy", "none"}
	case "wav":
		return []string{"pcm-s16le", "copy", "none"}
	case "aiff":
		return []string{"pcm-s16be", "copy", "none"}
	case "flac":
		return []string{"flac", "copy", "none"}
	default:
		return []string{"aac", "opus", "mp3", "vorbis", "flac", "pcm-s16le", "pcm-s16be", "copy", "none"}
	}
}

// NormalizePreset coerces a preset into combinations the encoder can actually write.
func NormalizePreset(p Preset) Preset {
	p = MigrateLoudness(p)
	if p.Container == "" {
		p.Container = "mp4"
	}
	videoOpts := VideoCodecsForContainer(p.Container, true)
	if !containsString(videoOpts, p.VideoCodec) {
		p.VideoCodec = videoOpts[0]
	}
	if p.VideoCodec == "copy" || p.VideoCodec == "none" || p.VideoCodec == "" {
		p.EncoderOverride = ""
		p.SpeedPreset = ""
		p.Tune = ""
	} else if p.Accel == "" {
		p.Accel = "auto"
	}

	audioOpts := AudioCodecsForContainer(p.Container)
	if !containsString(audioOpts, p.AudioCodec) {
		p.AudioCodec = audioOpts[0]
	}
	ladder := AudioBitrateLadder(p.AudioCodec)
	if len(ladder) == 0 {
		p.AudioBitrateK = 0
		p.AudioVBR = false
		p.AudioVBRQuality = 0
	} else {
		p.AudioBitrateK = nearestAudioBitrate(p.AudioCodec, p.AudioBitrateK)
		if p.AudioCodec != "mp3" {
			p.AudioVBR = false
			p.AudioVBRQuality = 0
		} else {
			if p.AudioVBRQuality < 0 {
				p.AudioVBRQuality = 0
			}
			if p.AudioVBRQuality > 9 {
				p.AudioVBRQuality = 9
			}
		}
	}

	// Loudness normalization needs an audio re-encode; clamp targets to sane ranges.
	if !LoudnessAppliesTo(p.AudioCodec) {
		p.LoudnessOn = false
	}
	if p.LoudnessOn {
		if p.LoudnessI == 0 {
			p.LoudnessI = DefaultLoudnessI
		}
		p.LoudnessI = clampF(p.LoudnessI, -36, -5)
		if p.LoudnessTP != 0 {
			p.LoudnessTP = clampF(p.LoudnessTP, -9, 0)
		}
	} else {
		p.LoudnessI, p.LoudnessTP, p.LoudnessRaiseOnly = 0, 0, false
	}
	return p
}

func nearestAudioBitrate(codec string, kbps int) int {
	ladder := AudioBitrateLadder(codec)
	if len(ladder) == 0 {
		return 0
	}
	if kbps <= 0 {
		return RecommendAudioBitrateK(codec, 0)
	}
	best := ladder[0]
	for _, b := range ladder[1:] {
		if abs(b-kbps) < abs(best-kbps) {
			best = b
		}
	}
	return best
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
