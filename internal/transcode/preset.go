// Package transcode builds ffmpeg jobs from named presets (mirrors + extends the web Local
// Studio preset model). Pure arg construction - no process spawning here; the transcode
// worker (internal/worker) runs the args. Output is always a NEW file; inputs are never
// modified. Logical codec is decoupled from the concrete encoder (see encoder.go) so a
// preset is portable across machines with different hardware.
package transcode

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Preset is a named transcode target. JSON-tagged so it round-trips through the worker
// wire protocol and persists in config (custom user presets).
type Preset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Desc  string `json:"desc,omitempty"`

	Container string `json:"container"` // mp4|webm|mkv|m4a|mp3|aac|ogg|wav|aiff|flac|opus

	// ── video ──
	VideoCodec      string  `json:"videoCodec"`                // h264|h265|vp9|av1|copy|none
	Accel           string  `json:"accel,omitempty"`           // auto|nvenc|qsv|amf|videotoolbox|vaapi|software
	EncoderOverride string  `json:"encoderOverride,omitempty"` // explicit ffmpeg encoder; wins over codec+accel
	RateMode        string  `json:"rateMode,omitempty"`        // crf|bitrate (default crf)
	CRF             int     `json:"crf,omitempty"`
	BitrateK        int     `json:"bitrateK,omitempty"` // video kbps when RateMode=bitrate
	Width           int     `json:"width,omitempty"`    // 0 = source
	Height          int     `json:"height,omitempty"`   // 0 = source; with Width=0, caps height keeping aspect
	FPS             float64 `json:"fps,omitempty"`      // 0 = source
	SpeedPreset     string  `json:"speedPreset,omitempty"`
	GOPSeconds      float64 `json:"gopSeconds,omitempty"`
	Tune            string  `json:"tune,omitempty"`
	Deinterlace     bool    `json:"deinterlace,omitempty"`

	// ── audio ──
	AudioCodec      string `json:"audioCodec"` // aac|opus|mp3|vorbis|flac|pcm-s16le|pcm-s16be|copy|none
	AudioBitrateK   int    `json:"audioBitrateK,omitempty"`
	AudioVBR        bool   `json:"audioVBR,omitempty"`        // libmp3lame VBR (-q:a) instead of CBR
	AudioVBRQuality int    `json:"audioVBRQuality,omitempty"` // -q:a level 0(best)..9
	Channels        int    `json:"channels,omitempty"`        // 0 = source
	SampleRate      int    `json:"sampleRate,omitempty"`      // 0 = source
	Loudness        string `json:"loudness,omitempty"`        // DEPRECATED legacy profile; MigrateLoudness maps it to the fields below

	// ── loudness normalization (two-pass, whole-track linear gain - see loudness.go) ──
	LoudnessOn        bool    `json:"loudnessOn,omitempty"`
	LoudnessI         float64 `json:"loudnessI,omitempty"`         // target integrated loudness (LUFS)
	LoudnessTP        float64 `json:"loudnessTP,omitempty"`        // true-peak ceiling (dBTP); 0 = DefaultLoudnessTP
	LoudnessRaiseOnly bool    `json:"loudnessRaiseOnly,omitempty"` // never turn an already-loud track down
}

// Ext is the output file extension for the preset's container.
func (p Preset) Ext() string {
	switch p.Container {
	case "m4a", "aac":
		return ".m4a"
	case "opus":
		return ".opus"
	case "ogg":
		return ".ogg"
	case "mkv":
		return ".mkv"
	case "webm":
		return ".webm"
	case "mp3":
		return ".mp3"
	case "wav":
		return ".wav"
	case "aiff":
		return ".aiff"
	case "flac":
		return ".flac"
	default:
		return ".mp4"
	}
}

// IsAudioOnly reports whether the preset produces an audio-only file (no video stream) -
// used by the Library "Music" browser mode to filter the preset chooser.
func (p Preset) IsAudioOnly() bool {
	return p.VideoCodec == "" || p.VideoCodec == "none"
}

// Builtins are the default presets. Video presets default Accel="auto" so the UI resolves
// the best working hardware encoder per machine.
var Builtins = []Preset{
	{ID: "web1080", Label: "Web 1080p (H.264 + AAC)", Desc: "Best compatibility - +faststart, yuv420p, ~2s GOP.",
		Container: "mp4", VideoCodec: "h264", Accel: "auto", CRF: 21, Height: 1080, GOPSeconds: 2, AudioCodec: "aac", AudioBitrateK: 160},
	{ID: "web720", Label: "Web 720p Small (H.264)", Desc: "Smaller files for quick sharing.",
		Container: "mp4", VideoCodec: "h264", Accel: "auto", CRF: 23, Height: 720, GOPSeconds: 2, AudioCodec: "aac", AudioBitrateK: 128},
	{ID: "web4k265", Label: "Web 4K (H.265 + AAC)", Desc: "High quality, smaller than H.264 at 4K.",
		Container: "mp4", VideoCodec: "h265", Accel: "auto", CRF: 22, Height: 2160, GOPSeconds: 2, AudioCodec: "aac", AudioBitrateK: 192},
	{ID: "webmVp9", Label: "WebM 1080p (VP9 + Opus)", Desc: "Open codec; great for the web.",
		Container: "webm", VideoCodec: "vp9", Accel: "software", CRF: 31, Height: 1080, AudioCodec: "opus", AudioBitrateK: 128},
	{ID: "av1", Label: "AV1 1080p (+ Opus)", Desc: "Best compression; slower encode.",
		Container: "mp4", VideoCodec: "av1", Accel: "auto", CRF: 30, Height: 1080, AudioCodec: "aac", AudioBitrateK: 160},
	{ID: "remux", Label: "Lossless Remux (MP4)", Desc: "Change container only - no re-encode, instant.",
		Container: "mp4", VideoCodec: "copy", AudioCodec: "copy"},

	// Audio-only.
	{ID: "audioOpus", Label: "Audio Only (Opus 160k)", Desc: "Strip video; small high-quality audio.",
		Container: "opus", VideoCodec: "none", AudioCodec: "opus", AudioBitrateK: 160},
	{ID: "audioAac", Label: "Audio Only (AAC 256k)", Desc: "Strip video; broadly compatible audio.",
		Container: "m4a", VideoCodec: "none", AudioCodec: "aac", AudioBitrateK: 256},
	{ID: "ogg-vorbis", Label: "Ogg Vorbis 320k", Desc: "Open audio format with a higher lossy ceiling than AAC.",
		Container: "ogg", VideoCodec: "none", AudioCodec: "vorbis", AudioBitrateK: 320},
	{ID: "mp3-320", Label: "MP3 320 (CBR)", Desc: "Universal DJ/player compatibility.",
		Container: "mp3", VideoCodec: "none", AudioCodec: "mp3", AudioBitrateK: 320},
	{ID: "mp3-v0", Label: "MP3 V0 (VBR)", Desc: "Transparent VBR, smaller than 320 CBR.",
		Container: "mp3", VideoCodec: "none", AudioCodec: "mp3", AudioVBR: true, AudioVBRQuality: 0},
	{ID: "flac", Label: "FLAC (lossless)", Desc: "Lossless compression - archival / mastering.",
		Container: "flac", VideoCodec: "none", AudioCodec: "flac"},
	{ID: "aiff", Label: "AIFF (lossless PCM)", Desc: "Uncompressed PCM - DJ software friendly.",
		Container: "aiff", VideoCodec: "none", AudioCodec: "pcm-s16be"},
	{ID: "wav", Label: "WAV (lossless PCM)", Desc: "Uncompressed PCM.",
		Container: "wav", VideoCodec: "none", AudioCodec: "pcm-s16le"},
}

// Find returns the builtin preset with id, or false.
func Find(id string) (Preset, bool) {
	for _, p := range Builtins {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// AllPresets returns builtins plus the user's custom presets; a custom preset overrides a
// builtin with the same ID.
func AllPresets(custom []Preset) []Preset {
	out := make([]Preset, 0, len(Builtins)+len(custom))
	overridden := make(map[string]int, len(custom))
	for _, c := range custom {
		overridden[c.ID] = len(out)
		out = append(out, c)
	}
	for _, b := range Builtins {
		if _, ok := overridden[b.ID]; !ok {
			out = append(out, b)
		}
	}
	return out
}

// Job is a concrete transcode request.
type Job struct {
	Input  string
	Output string
	Preset Preset

	TrimStart float64 // seconds (0 = from start)
	TrimEnd   float64 // seconds (0 or ≤start = to end)

	// GainDB is the loudness plan's whole-track gain, set by the worker after the
	// pass-1 measurement (PlanGain). nil = no gain filter. Args never invents
	// dynamics processing on its own.
	GainDB *float64
}

// videoEncoder returns the concrete ffmpeg video encoder for the preset: an explicit
// override wins; otherwise the software encoder for the codec (the UI resolves HW encoders
// into EncoderOverride before dispatch, so the worker never needs detection).
func (p Preset) videoEncoder() string {
	if p.EncoderOverride != "" {
		return p.EncoderOverride
	}
	return softwareEncoder(p.VideoCodec)
}

// Args builds the ffmpeg argument list (excluding the ffmpeg binary itself). Input seeking
// (-ss before -i) is used for fast trims; -t limits the duration.
func (j Job) Args() []string {
	a := []string{"-hide_banner", "-y"}
	if j.TrimStart > 0 {
		a = append(a, "-ss", ftoa(j.TrimStart))
	}
	a = append(a, "-i", j.Input)
	if j.TrimEnd > j.TrimStart {
		a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
	}

	a = append(a, j.videoArgs()...)
	a = append(a, j.audioArgs()...)

	if c := j.Preset.Container; c == "mp4" || c == "m4a" || c == "aac" {
		a = append(a, "-movflags", "+faststart")
	}
	return append(a, j.Output)
}

func (j Job) videoArgs() []string {
	p := j.Preset
	switch p.VideoCodec {
	case "copy":
		return []string{"-c:v", "copy"}
	case "", "none":
		return []string{"-vn"}
	}
	enc := p.videoEncoder()
	a := []string{"-c:v", enc}
	if p.RateMode == "bitrate" && p.BitrateK > 0 {
		a = append(a, "-b:v", fmt.Sprintf("%dk", p.BitrateK))
	} else {
		a = append(a, qualityArgs(enc, p.VideoCodec, p.CRF)...)
	}
	a = append(a, "-pix_fmt", "yuv420p")
	gop := p.GOPSeconds
	if gop <= 0 {
		gop = 2
	}
	fps := p.FPS
	if fps <= 0 {
		fps = 30 // GOP frame count needs a reference fps when source fps is unknown
	}
	a = append(a, "-g", strconv.Itoa(int(gop*fps)))
	if strings.HasPrefix(enc, "libx26") {
		a = append(a, "-sc_threshold", "0") // x264/x265-only; HW encoders reject it
		if p.Tune != "" {
			a = append(a, "-tune", p.Tune)
		}
	}
	if p.SpeedPreset != "" {
		a = append(a, "-preset", p.SpeedPreset)
	}
	if p.FPS > 0 {
		a = append(a, "-r", ftoa(p.FPS))
	}
	if vf := videoFilters(p); vf != "" {
		a = append(a, "-vf", vf)
	}
	return a
}

// videoFilters builds the -vf chain (deinterlace → scale).
func videoFilters(p Preset) string {
	var parts []string
	if p.Deinterlace {
		parts = append(parts, "bwdif")
	}
	switch {
	case p.Width > 0 && p.Height > 0:
		parts = append(parts, fmt.Sprintf("scale=%d:%d", p.Width, p.Height))
	case p.Height > 0:
		parts = append(parts, fmt.Sprintf("scale=-2:'min(%d,ih)'", p.Height))
	case p.Width > 0:
		parts = append(parts, fmt.Sprintf("scale='min(%d,iw)':-2", p.Width))
	}
	return strings.Join(parts, ",")
}

func (j Job) audioArgs() []string {
	p := j.Preset
	switch p.AudioCodec {
	case "copy":
		return []string{"-c:a", "copy"}
	case "", "none":
		return []string{"-an"}
	}
	enc := audioEncoder(p.AudioCodec)
	a := []string{"-c:a", enc}
	switch {
	case enc == "libmp3lame" && p.AudioVBR:
		a = append(a, "-q:a", strconv.Itoa(p.AudioVBRQuality))
	case enc == "flac":
		a = append(a, "-compression_level", "8")
	case strings.HasPrefix(enc, "pcm_"):
		// no bitrate for PCM
	case p.AudioBitrateK > 0:
		a = append(a, "-b:a", fmt.Sprintf("%dk", p.AudioBitrateK))
	}
	if p.SampleRate > 0 {
		a = append(a, "-ar", strconv.Itoa(p.SampleRate))
	}
	if p.Channels > 0 {
		a = append(a, "-ac", strconv.Itoa(p.Channels))
	}
	if af := j.gainFilter(); af != "" {
		a = append(a, "-af", af)
	}
	return a
}

// gainFilter returns the single linear volume filter for a planned loudness gain -
// the ONLY audio filter this package ever emits (no loudnorm/dynaudnorm/limiters).
func (j Job) gainFilter() string {
	if j.GainDB == nil || math.Abs(*j.GainDB) < 0.05 {
		return ""
	}
	return fmt.Sprintf("volume=%.2fdB", *j.GainDB)
}

// qualityArgs maps a target CRF to the right rate-control flag for the encoder family.
func qualityArgs(encoder, codec string, crf int) []string {
	q := strconv.Itoa(crf)
	switch {
	case strings.HasSuffix(encoder, "_nvenc"):
		return []string{"-rc", "vbr", "-cq", q, "-preset", "p5"}
	case strings.HasSuffix(encoder, "_qsv"):
		return []string{"-global_quality", q, "-preset", "medium"}
	case strings.HasSuffix(encoder, "_amf"):
		return []string{"-rc", "cqp", "-qp_i", q, "-qp_p", q, "-quality", "balanced"}
	case encoder == "libvpx-vp9":
		return []string{"-crf", q, "-b:v", "0"} // VP9 constant-quality needs -b:v 0
	default: // libx264 / libx265 / libsvtav1 / videotoolbox
		return []string{"-crf", q}
	}
}

// HWAccels returns the accel options the UI offers.
func HWAccels() []string {
	return []string{"auto", "nvenc", "qsv", "amf", "videotoolbox", "vaapi", "software"}
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }
