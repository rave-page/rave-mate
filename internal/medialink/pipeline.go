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
	MaxHeight   int // downscale ceiling (px): input taller than this is scaled down; 0 = native
	// Encode device (WP-3): which GPU to encode on. DeviceLUID is the DXGI adapter LUID key
	// ("0xHIGH_0xLOW"), DeviceIndex its DXGI ordinal (the number ffmpeg's d3d11va/-gpu/-qsv_device
	// flags take). Zero value ("" / 0) is NOT a device: engines treat DeviceIndex < 0 OR an empty
	// LUID as "engine default" and emit no device flags, so an unset spec behaves exactly as before.
	DeviceLUID  string
	DeviceIndex int
}

// Device reports the resolved encode device, ok=false when the engine should use its own default.
func (s EncodeSpec) Device() (luid string, index int, ok bool) {
	if s.DeviceLUID == "" || s.DeviceIndex < 0 {
		return "", -1, false
	}
	return s.DeviceLUID, s.DeviceIndex, true
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

// ZeroCopySource is a raw-video Source whose pixels live in a GPU shared texture an encoder may
// consume DIRECTLY - no host readback, no frame bytes on the Go heap. An encode engine that can
// open such a texture (the native MF child, same adapter) asks once at open time and then never
// calls Next; every other engine ignores this and takes the frame path. ok=false = no shared
// texture (no backend, unknown sender, or a DX9/CPU-memoryshare sender): use Next.
//
// Implementations must NOT start a capture as a side effect of this call - the whole point is
// that a zero-copy route never opens the readback.
type ZeroCopySource interface {
	SharedTexture() (handle uint64, dxgiFormat uint32, w, h int, name string, ok bool)
}

// ZeroCopySink is the receive-side mirror of ZeroCopySource: a raw-video Sink whose DESTINATION
// pixels live in a GPU shared texture a decoder may render into DIRECTLY - no raw frame down a
// pipe, none on the Go heap. A decode engine that can drive such a texture (the native MF child,
// zigmedia inc 2) asks once at open and then never calls Write for that codec; every other engine
// ignores this and takes the frame path. ok=false = no shared texture (no backend, CPU/memoryshare
// sender): use Write.
//
// The texture must ALREADY EXIST when this returns ok - a decoder cannot create it - and it must
// stay valid until Close.
type ZeroCopySink interface {
	SharedTexture() (handle uint64, dxgiFormat uint32, w, h int, name string, ok bool)
}

// PipelineStats is an encode/decode child's live telemetry (§7 route stats).
type PipelineStats struct {
	Encoder  string  // ffmpeg encoder/decoder in use
	HWAccel  string  // decode side: active hwaccel ("" = software)
	OutFPS   float64 // frames leaving the child per second
	Restarts int     // supervised child restarts
	// Dropped counts frames THIS element and everything it wraps threw away: undersized/foreign
	// input, respawn-backoff gaps, waiting-for-keyframe, per-route fps-cap drops, sink dim
	// mismatches. Each stage kept its own counter and none of them reached a log or the panel, so
	// a route that silently drops most of its frames looked identical to a healthy one.
	Dropped uint64
	// Native-engine session telemetry (zero for ffmpeg children). Rising LatP99Ms is the
	// Phase-2 load governor's early saturation signal.
	LatP50Ms    float64 // submit→AU latency percentiles
	LatP99Ms    float64
	QueueDepth  int     // frames in flight inside the encoder
	ChildCPUPct float64 // encoder child process CPU
	// Zero-copy capture (zigmedia inc 1): the encoder child reads the sender's GPU shared
	// texture itself, so no frame ever crosses the host. Zero on every other path.
	ZeroCopy    bool    // the live session really is zero-copy (not downgraded)
	CapFPS      float64 // shared textures captured per second inside the child
	CapSkips    uint64  // pacing ticks skipped (previous encode still running)
	MtxTimeouts uint64  // shared-texture mutex acquire timeouts (sender contention)
	SrcErrors   uint64  // capture hard failures (CopyResource/acquire)
	CapStaleMs  float64 // age of the last successful capture (frozen-sender oracle, R1)
	EncBusyMs   float64 // mean child capture+encode ms/frame (the zero-copy saturation signal:
	// the parent submits nothing, so submit→AU percentiles stay empty on this path)
	Downgrades int // zero-copy → readback fallbacks on this route (a rig that always
	// downgrades must be visible here, not silently slow)
	// AdapterMoved: the sender's texture lives on a different GPU than the encode device asked
	// for, and the session was re-placed there instead of downgrading (zigmedia inc 3, R7).
	AdapterMoved bool
	// Zero-copy DECODE (zigmedia inc 2): the decoder child renders straight into the local
	// video-share sender's texture, so no decoded frame crosses a pipe or the Go heap. Zero on
	// the ffmpeg decode path.
	ZeroDecode  bool    // the live session really is publishing on the GPU
	DecFPS      float64 // frames published into the destination texture per second
	DecBusyMs   float64 // mean child decode+publish ms/frame
	InDropped   uint64  // AUs the inbound ring could not take (ring full)
	DecDropped  uint64  // AUs the child could not decode (oversized / awaiting a keyframe)
	DecErrors   uint64  // publish hard failures (Blt / acquire)
	DecStaleMs  float64 // age of the last publish (frozen-destination oracle)
	DecMtxTimeo uint64  // destination-texture mutex acquire timeouts
}

// PipelineReporter is the optional stats surface of a factory-built Source/Sink.
type PipelineReporter interface {
	PipeStats() PipelineStats
}

// InnerDrops sums the Dropped counter of a wrapped Source/Sink, so the ONE reporter the router
// asks (the outermost wrapper) accounts for the whole chain instead of the stage counters dying
// where they were incremented. 0 when the inner stage reports nothing.
func InnerDrops(inner any) uint64 {
	if pr, ok := inner.(PipelineReporter); ok {
		return pr.PipeStats().Dropped
	}
	return 0
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
