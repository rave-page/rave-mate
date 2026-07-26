package medialink

import "strings"

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

// EncoderMFNative names the sender's PIPE-FREE H.264 engine (internal/mfenc: D3D11 upload →
// VideoProcessorBlt → encoder MFT, no ffmpeg child and no rawvideo stdin pipe). It is advertised
// ONLY when that engine is actually available, which is what makes it safe to preempt the tier
// order with (see pipeFreeWins). Distinct from ffmpeg's own "h264_mf" encoder, which is a normal
// pipe-fed tier-3 candidate.
const EncoderMFNative = "h264_mf_native"

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
	// EncoderMFNative first: same silicon as the *_nvenc/_amf/_qsv siblings, minus the ffmpeg child
	// and the multi-GB/s rawvideo pipe. Cheapest way to serve this tier when it is advertised.
	{3, CodecH264, DecodeH264, false, []string{EncoderMFNative, "h264_nvenc", "h264_qsv", "h264_amf", "h264_vaapi", "h264_mf", "h264_videotoolbox", "h264_v4l2m2m", "h264_omx"}},
	{4, CodecH264, DecodeH264, true, []string{"libx264"}},
	{4, CodecAV1, DecodeAV1, true, []string{"libsvtav1"}},
	{5, CodecJPEG, DecodeJPEG, false, []string{"mjpeg"}},
}

// swTier4MaxPixelRate bounds the tier-4 software encoders (libx264/libsvtav1) to what
// they survive in zerolatency mode: ~1080p60. Above it (4K VJ sources) the NDI-class
// intra tier (mjpeg: SIMD DCT, near-linear multithread) encodes native res at a
// fraction of the CPU - x264 at 4K60 pins every core, which is a set-killer.
const swTier4MaxPixelRate = 1920 * 1080 * 60

// swAutoMaxHeight is the auto downscale ceiling applied ONLY to tier-4 software encodes
// (EncodeMaxHeight 0 = auto). Hardware tiers and the intra tier run native res.
const swAutoMaxHeight = 1080

// NegotiateCodec picks the highest common tier: encoders = the source node's probed working
// encoders, decoders = the requesting node's decodable codecs. ok=false when nothing overlaps -
// the caller falls back to the P1 echo behaviour.
func NegotiateCodec(encoders, decoders []string) (CodecChoice, bool) {
	return NegotiateCodecFor(encoders, decoders, 0)
}

// NegotiateCodecFor is NegotiateCodec with the source's pixel rate (w*h*fps; 0 = unknown):
// hardware tiers are unaffected, but tier-4 SOFTWARE encoders are skipped above
// swTier4MaxPixelRate so a 4K source lands on the intra tier instead of melting the CPU.
func NegotiateCodecFor(encoders, decoders []string, pixelRate float64) (CodecChoice, bool) {
	return Negotiate(encoders, decoders, NegotiateOpts{PixelRate: pixelRate})
}

// NegotiateOpts carries the SENDER-side inputs that outrank the raw §3.2 tier order.
type NegotiateOpts struct {
	// PixelRate is the source's w*h*fps (0 = unknown) - gates the tier-4 software encoders.
	PixelRate float64
	// Prefer is the sender's configured codec preference ("hevc"|"h264"|"mjpeg"; "" = none). It is
	// the SENDER mirror of the receiver's MediaLink.PreferCodec: the matching tier is promoted to
	// the front when the requester can decode it and we hold an encoder for it. It never removes
	// tiers - an unsatisfiable preference falls straight through to the normal walk, so a peer that
	// can't decode the preferred codec still gets a working route instead of a raw-guard refusal.
	Prefer string
	// PinEncoder is the sender's hard encoder pin (MediaLink.Encoder; "" = negotiate). When we hold
	// it and the requester decodes its codec, it wins outright - the user asked for that engine.
	PinEncoder string
}

// Negotiate picks the video codec + encoder for a route. Precedence, highest first:
//
//  1. PinEncoder - an explicit user pin, when the requester can decode its codec.
//  2. Prefer - the sender's codec preference, when satisfiable.
//  3. PIPE-FREE PREEMPTION: EncoderMFNative (advertised only when the native, pipe-free H.264
//     engine really exists) beats every higher tier, because those tiers are all served by an
//     ffmpeg child fed raw RGBA over a 64 KB stdin pipe - 497 MB/s at 1080p60, 1.99 GB/s at 4K60,
//     plus a CPU swscale RGBA→NV12 and a GPU re-upload. That memory-bandwidth traffic, not the
//     codec, is what starves OBS on the sending PC. AV1/HEVC's bitrate edge does not pay for it on
//     a LAN link that already has a per-route bitrate budget.
//  4. The plain §3.2 tier walk (AV1 → HEVC → H.264 hw → sw → mjpeg).
//
// ok=false when nothing overlaps - the caller falls back to the P1 echo behaviour (or the
// raw-video guard refuses the route).
func Negotiate(encoders, decoders []string, o NegotiateOpts) (CodecChoice, bool) {
	enc := toSet(encoders)
	dec := toSet(decoders)
	usable := func(software bool) bool { return !(software && o.PixelRate > swTier4MaxPixelRate) }

	if o.PinEncoder != "" {
		if ch, ok := choiceFor(o.PinEncoder); ok && enc[o.PinEncoder] && dec[decodeOf(ch.Codec)] && usable(ch.Software) {
			return ch, true
		}
	}
	if want := preferDecodeName(o.Prefer); want != "" && dec[want] {
		for _, t := range codecTiers {
			if t.decode != want || !usable(t.software) {
				continue
			}
			for _, e := range t.encoders {
				if enc[e] {
					return CodecChoice{Codec: t.codec, Encoder: e, Tier: t.tier, Software: t.software}, true
				}
			}
		}
	}
	if enc[EncoderMFNative] && dec[DecodeH264] {
		ch, _ := choiceFor(EncoderMFNative)
		return ch, true
	}
	for _, t := range codecTiers {
		if !usable(t.software) || !dec[t.decode] {
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

// choiceFor builds the CodecChoice for a known encoder name (ok=false when it isn't in the matrix).
func choiceFor(encoder string) (CodecChoice, bool) {
	for _, t := range codecTiers {
		for _, e := range t.encoders {
			if e == encoder {
				return CodecChoice{Codec: t.codec, Encoder: e, Tier: t.tier, Software: t.software}, true
			}
		}
	}
	return CodecChoice{}, false
}

// decodeOf maps a wire codec to the decode capability the requester must hold.
func decodeOf(c Codec) string {
	switch c {
	case CodecAV1:
		return DecodeAV1
	case CodecHEVC:
		return DecodeHEVC
	case CodecH264:
		return DecodeH264
	case CodecJPEG:
		return DecodeJPEG
	}
	return ""
}

// preferDecodeName maps a config codec preference to its decode capability ("" = no preference).
func preferDecodeName(prefer string) string {
	switch strings.ToLower(strings.TrimSpace(prefer)) {
	case "hevc", "h265":
		return DecodeHEVC
	case "h264", "avc":
		return DecodeH264
	case "mjpeg", "jpeg":
		return DecodeJPEG
	case "av1":
		return DecodeAV1
	}
	return ""
}

// rawVideoMaxPixels caps UNCOMPRESSED video on a route: raw NRGBA above this frame area
// is refused outright - every raw frame is AES-GCM-sealed + TCP-written whole, and at
// 1080p60 that is ~500 MB/s of crypto + wire on the sender (the spout-over-peerlink
// source-PC melt). Small frames stay allowed for the P1 echo path + tests.
const rawVideoMaxPixels = 320 * 240

// rawVideoOK reports whether a video source is small enough to stream uncompressed.
func rawVideoOK(d SourceDesc) bool { return d.Width*d.Height <= rawVideoMaxPixels }

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
