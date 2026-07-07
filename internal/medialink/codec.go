package medialink

// codec.go is the §3.2 codec-matrix groundwork: nodes advertise their probed WORKING video
// encoders + decodable codecs (Caps.Encoders/Decoders - the app wires worker/encoders.go probe
// results in), an Offer carries the requester's decode caps, and the answerer picks the highest
// common tier. The ffmpeg encode/decode children land P4 - until then the choice is advisory,
// recorded in the Answer (Caps.Encoders = the one chosen encoder).

// Decoder capability names carried in Caps.Decoders (codecs the node can decode, hw or sw -
// the prober only advertises what actually works).
const (
	DecodeAV1  = "av1"
	DecodeHEVC = "hevc"
	DecodeH264 = "h264"
	DecodeJPEG = "mjpeg"
)

// CodecChoice is a negotiated video codec selection (§3.2).
type CodecChoice struct {
	Codec    Codec
	Encoder  string // ffmpeg encoder name on the source side
	Tier     int    // §3.2 tier: 1 (best) … 5 (floor)
	Software bool   // tier 4 software encode - surface the CPU-budget warning
}

// Warning returns the §3.2 CPU-budget note for software tiers ("" for hardware).
func (c CodecChoice) Warning() string {
	if !c.Software {
		return ""
	}
	return "software video encode (" + c.Encoder + "): expect high CPU load at 1080p60 and above"
}

// codecTiers is the §3.2 matrix, best tier first; encoders in preference order within a tier.
var codecTiers = []struct {
	tier     int
	codec    Codec
	decode   string // decoder capability the REQUESTER must hold
	software bool
	encoders []string // encoder the TARGET must hold (any one)
}{
	// HW encoder lists are vendor-NEUTRAL: any backend ffmpeg exposes - NVIDIA/AMD/Intel plus
	// Media Foundation (h264_mf/hevc_mf, which wraps ANY registered HW MFT incl. custom encoder
	// cards), VA-API, V4L2 M2M (SoC/custom), VideoToolbox. The prober only advertises what actually
	// test-encoded, so listing a backend here is harmless on machines that lack it.
	{1, CodecAV1, DecodeAV1, false, []string{"av1_nvenc", "av1_qsv", "av1_amf", "av1_vaapi", "av1_mf"}},
	{2, CodecHEVC, DecodeHEVC, false, []string{"hevc_nvenc", "hevc_qsv", "hevc_amf", "hevc_vaapi", "hevc_mf", "hevc_videotoolbox", "hevc_v4l2m2m"}},
	{3, CodecH264, DecodeH264, false, []string{"h264_nvenc", "h264_qsv", "h264_amf", "h264_vaapi", "h264_mf", "h264_videotoolbox", "h264_v4l2m2m", "h264_omx"}},
	{4, CodecH264, DecodeH264, true, []string{"libx264"}},
	{4, CodecAV1, DecodeAV1, true, []string{"libsvtav1"}},
	{5, CodecJPEG, DecodeJPEG, false, []string{"mjpeg"}},
}

// NegotiateCodec picks the highest common tier: encoders = the source node's probed working
// encoders, decoders = the requesting node's decodable codecs. ok=false when nothing overlaps -
// the caller falls back to the P1 echo behaviour.
func NegotiateCodec(encoders, decoders []string) (CodecChoice, bool) {
	enc := toSet(encoders)
	dec := toSet(decoders)
	for _, t := range codecTiers {
		if !dec[t.decode] {
			continue
		}
		for _, e := range t.encoders {
			if enc[e] {
				return CodecChoice{Codec: t.codec, Encoder: e, Tier: t.tier, Software: t.software}, true
			}
		}
	}
	return CodecChoice{}, false
}

// EncoderTier maps an encoder name (Answer.Caps.Encoders[0]) back to its §3.2 tier - the recv
// side derives its route-stat tier/software flag from the answered choice.
func EncoderTier(encoder string) (tier int, software, ok bool) {
	for _, t := range codecTiers {
		for _, e := range t.encoders {
			if e == encoder {
				return t.tier, t.software, true
			}
		}
	}
	return 0, false, false
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
