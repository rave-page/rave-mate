package medialink

import "context"

// pipeline.go - §3.2 encode/decode seams (P4). medialink stays pure Go / hardware-free: the ffmpeg
// children live in internal/mediapipe and plug in through these factories (Options.Encoder/
// Decoder). nil factories = P1–P3 passthrough (raw frames on the wire). Contract: a wrapper's
// Close closes the wrapped Source/Sink too; a decoder passes frames of any OTHER codec through
// untouched (defensive degrade).

// EncodeSpec parametrizes the send-side encode child for one route (§3.2 negotiated choice).
type EncodeSpec struct {
	Encoder     string // ffmpeg encoder name (negotiated, e.g. "hevc_nvenc")
	Codec       Codec  // wire codec the encoder produces
	Tier        int    // §3.2 tier (1 best … 5 floor)
	Software    bool   // tier-4 software encode (CPU warning surfaced in route stats)
	Width       int
	Height      int
	FPS         float64
	BitrateKbps int // requester budget (Offer.Bitrate); 0 = encoder default
}

// DecodeSpec parametrizes the receive-side decode child (dims/fps from the source's advert).
type DecodeSpec struct {
	Codec  Codec
	Width  int
	Height int
	FPS    float64
}

// EncoderFactory wraps a raw-video Source with an encode child. The returned Source SHOULD
// implement KeyframeSource (PLI recovery, §2.5) and may implement PipelineReporter.
type EncoderFactory func(ctx context.Context, spec EncodeSpec, src Source) (Source, error)

// DecoderFactory wraps a Sink with a decode child (compressed in → raw frames out).
type DecoderFactory func(ctx context.Context, spec DecodeSpec, sink Sink) (Sink, error)

// PipelineStats is an encode/decode child's live telemetry (§7 route stats).
type PipelineStats struct {
	Encoder  string  // ffmpeg encoder/decoder in use
	HWAccel  string  // decode side: active hwaccel ("" = software)
	OutFPS   float64 // frames leaving the child per second
	Restarts int     // supervised child restarts
}

// PipelineReporter is the optional stats surface of a factory-built Source/Sink.
type PipelineReporter interface {
	PipeStats() PipelineStats
}

// CompressedVideo reports whether c is an encoded video codec (vs raw pixels / audio).
func (c Codec) CompressedVideo() bool {
	switch c {
	case CodecJPEG, CodecH264, CodecHEVC, CodecAV1:
		return true
	}
	return false
}

// IntraOnly reports whether every frame of c is independently decodable (raw pixels, MJPEG).
func (c Codec) IntraOnly() bool { return c != CodecH264 && c != CodecHEVC && c != CodecAV1 }
