package transcode

// Quality profiles auto-fill rate-control + bitrate from (codec, height, fps), mirroring the
// web Local Studio "Quality profile" dropdown. ApplyProfile mutates a preset in place; the
// "custom" profile leaves it untouched.

// Profiles are the selectable profile names (besides "custom").
var Profiles = []string{"custom", "streaming", "youtube-hq", "master", "mobile", "match-source"}

// ApplyProfile fills CRF/bitrate on p for the named profile. For bitrate profiles it sets
// RateMode=bitrate and a target derived from the resolution; CRF profiles set RateMode=crf.
func ApplyProfile(p *Preset, profile string) {
	switch profile {
	case "streaming":
		p.RateMode = "bitrate"
		p.BitrateK = bitrateFor(p, 0.10)
	case "youtube-hq":
		p.RateMode = "bitrate"
		p.BitrateK = bitrateFor(p, 0.15)
	case "master":
		p.RateMode = "crf"
		p.CRF = clampCRF(p.VideoCodec, 16)
	case "mobile":
		p.RateMode = "bitrate"
		p.BitrateK = bitrateFor(p, 0.05)
	case "match-source":
		p.RateMode = "crf"
		p.CRF = clampCRF(p.VideoCodec, 18)
	default: // custom: leave as-is
	}
}

// bitrateFor estimates a video bitrate (kbps) as bitsPerPixel × pixels × fps, scaled by a
// per-profile factor. Uses sane defaults when height/fps are "source" (0).
func bitrateFor(p *Preset, factor float64) int {
	h := p.Height
	if h <= 0 {
		h = 1080
	}
	w := p.Width
	if w <= 0 {
		w = h * 16 / 9
	}
	fps := p.FPS
	if fps <= 0 {
		fps = 30
	}
	// H.265/AV1/VP9 are ~40% more efficient than H.264 at equal quality.
	eff := 1.0
	switch p.VideoCodec {
	case "h265", "av1", "vp9":
		eff = 0.6
	}
	kbps := factor * float64(w) * float64(h) * fps * eff / 1000
	if kbps < 500 {
		kbps = 500
	}
	return int(kbps)
}

// clampCRF keeps a CRF in the encoder's sensible range (AV1/SVT uses a wider scale).
func clampCRF(codec string, crf int) int {
	lo, hi := 0, 51
	if codec == "av1" {
		hi = 63
	}
	if crf < lo {
		return lo
	}
	if crf > hi {
		return hi
	}
	return crf
}
