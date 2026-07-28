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
	// Dropped is the TOTAL of everything this element and its wrapped stages threw away:
	// undersized/foreign input, respawn-backoff gaps, waiting-for-keyframe, per-route fps-cap
	// discards, sink dim mismatches. Each stage kept its own counter and none of them reached a
	// log or the panel, so a route that silently drops most of its frames looked identical to a
	// healthy one.
	//
	// RateCapped is the part of that total which is DELIBERATE rate limiting (the per-route
	// MediaLink.MaxFPS cap discarding frames the shared capture delivered at a faster rate,
	// mediaroute.spoutSource). Summing it into one "dropped" number made a healthy 60 fps source
	// feeding a 40 fps route read `dropped 41902 and climbing` - catastrophic loss and
	// correct-by-design throttling are the same number. Render/log them SEPARATELY; the real loss
	// is RealDrops().
	Dropped    uint64
	RateCapped uint64
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
	// CapSync names which synchronisation the live capture actually got: "keyed", "named" or
	// "unsync". Rendered because the branch decides correctness, not just contention - a real
	// Spout sender is a LEGACY shared texture with no keyed mutex, so the field takes a different
	// path than any synthetic keyed-mutex test, and not being able to see which one cost a whole
	// round of "the source must be static" misdiagnosis.
	CapSync    string
	CapStaleMs float64 // age of the last successful capture (frozen-sender oracle, R1)
	EncBusyMs  float64 // mean child capture+encode ms/frame (the zero-copy saturation signal:
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
	// DegradeReason is the one-sentence "why is this route not on its best path" - a poisoned
	// hardware encoder, the software encode tier, a mid-route engine substitution. EMPTY is the
	// only healthy value. A route that degraded silently while every counter read fine is the
	// worst failure shape there is, so this is rendered wherever the engine name is.
	DegradeReason string
	// Drive is the encoder MFT's resolved drive mode ("async"/"sync") on the native engine, taken
	// from the MFT's own MF_TRANSFORM_ASYNC attribute rather than assumed per vendor.
	Drive string
	// SoftwareEncode: this route is on a SOFTWARE encoder tier (native MF software MFT, or a
	// substituted libx264) rather than silicon.
	SoftwareEncode bool
	// BusyDrops and EncFails are SEPARATE on purpose: "encoder saturated but the route is
	// healthy" and "the encoder is failing" are different incidents with different responses, and
	// they are indistinguishable once summed into Dropped (which still counts both as a total).
	BusyDrops uint64 // frames the encoder had no credit for in time - saturation
	EncFails  uint64 // attributed hard encode failures reported by the encoder child
	// AUBytes/AUCount are the CONTENT oracle: bytes of real bitstream and how many access units
	// carried them. Bytes-per-frame is what separates a live picture from a black or frozen one,
	// and counters like OutFPS cannot - a black route reported healthy on those for 12 minutes.
	// They were collected from inc 1 and rendered NOWHERE until inc 5, which is how a black route
	// kept a healthy-looking panel: the one number that could have told the truth never left the
	// struct.
	AUBytes uint64
	AUCount uint64
	// AUBytesPerFrame is the same oracle over a SLIDING WINDOW (ratewin), which is what the
	// question actually needs: a lifetime average over a route that went black an hour ago still
	// reads healthy. This is the number to render.
	AUBytesPerFrame float64
	// PubFrames/PubBytes are the receive side's mirror: raw frames actually PUBLISHED into the
	// local video-share sender. The sink's Write is a volume-shaped operation behind an
	// error-shaped contract - it returns nil for a frame it threw away - so "route up, frames
	// arriving, nothing published" can only be seen as a volume. Dropped alone cannot say it: a
	// sink that drops nothing and publishes nothing reports the same zero.
	PubFrames uint64
	PubBytes  uint64
	// PubStalledMs/PubChanges/PubHash are the receive side's CONTENT oracle: how long the
	// published PICTURE has been identical, and how many times it actually changed. Every counter
	// above is rate- or volume-shaped, and that is not enough - a 4K route republished ONE
	// bit-identical frame for 48 minutes while fps 58.5, capStaleMs 16, dropped 0 and encFails 0
	// all read healthy (#58). Volume cannot see it either: an encoded 4K frame stays 3-5 kB
	// whether the picture moves or not, since keyframes alone account for that. A hash can.
	// PubStalledMs is the age of the last CHANGE, not of the last frame - that distinction is the
	// whole point, because a frozen source still delivers frames perfectly on time.
	// -1 = nothing published yet.
	PubStalledMs int64
	PubChanges   uint64
	PubHash      uint64
	// PubPeakFrac is the LARGEST fraction of the picture ever seen to change between frames on this
	// route. Needed because "it changed" is not "it is moving": a 4K desktop capture whose only live
	// element is a tray clock changes several times a second and still looks like a still image to a
	// human and to Resolume. Measured 0.005 there, against 0.065 for a live webcam on the same path.
	PubPeakFrac float64
	// Adapter/Drive/DevPolicy identify the code path serving this route, so a passing run on a
	// machine with no toolchain can still say WHICH path passed.
	AdapterLUID int64
	DevPolicy   string
	// Poisoned + LedgerFails expose the crash-loop ledger for this route's (adapter, encoder).
	Poisoned    bool
	LedgerFails int
}

// PipelineReporter is the optional stats surface of a factory-built Source/Sink.
type PipelineReporter interface {
	PipeStats() PipelineStats
}

// AUNoiseFloorBytes separates "this bitstream carries a picture" from "this bitstream carries
// nothing". MEASURED, not guessed: a static 720p sender encodes to 49 B/AU and moving content at
// the same geometry to 184 (zigmedia inc-3 M3); the field's black 4K30 route sat at 255 B/frame on
// a 20 Mbps budget where real content would be ~83,000, while a healthy webcam route carried 3,169.
// So the useful threshold is not "thousands" - it is "sustained under ~1 kB/frame is not a picture".
//
// It deliberately does NOT mean "black". A frozen source, a black source and a genuinely static
// one are indistinguishable in the bitstream (that is what an encoder is for) and all three are
// worth telling the operator about, so the wording everywhere names all three and NOTHING marks
// the route degraded on this alone.
const AUNoiseFloorBytes = 1000

// NoPictureContent reports whether the windowed bytes-per-frame is at the noise floor while AUs
// are still flowing - the rendered half of the content oracle.
func (p PipelineStats) NoPictureContent() bool {
	return p.AUCount > 0 && p.AUBytesPerFrame > 0 && p.AUBytesPerFrame < AUNoiseFloorBytes
}

// RealDrops is Dropped minus the deliberate rate-limiting share: frames actually LOST. Saturating,
// so a stage that reports RateCapped without folding it into Dropped can never underflow.
func (p PipelineStats) RealDrops() uint64 {
	if p.RateCapped >= p.Dropped {
		return 0
	}
	return p.Dropped - p.RateCapped
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

// InnerRateCapped is InnerDrops for the rate-limited share: it must ride the same wrapper chain,
// or the outermost reporter would total the fps-cap discards into Dropped and lose the ability to
// say they were intentional.
func InnerRateCapped(inner any) uint64 {
	if pr, ok := inner.(PipelineReporter); ok {
		return pr.PipeStats().RateCapped
	}
	return 0
}

// InnerPublished rides the same chain for the receive side's DELIVERED volume. The publishing sink
// is always the innermost stage (the decode wrapper owns it), so without this the one reporter the
// router asks can never say whether anything reached the Spout sender.
func InnerPublished(inner any) (frames, bytes uint64) {
	if pr, ok := inner.(PipelineReporter); ok {
		st := pr.PipeStats()
		return st.PubFrames, st.PubBytes
	}
	return 0, 0
}

// InnerContent lifts the wrapped sink's CONTENT oracle (PubStalledMs/PubChanges/PubHash) up the
// wrapper chain. Without this the stall dies at the innermost sink - the same way AUBytes was
// collected and rendered nowhere while a black route reported healthy. stalledMs is -1 when the
// inner sink has published nothing (or reports no stats), never 0: "fresh" and "never" must not
// look alike.
func InnerContent(inner any) (stalledMs int64, changes, hash uint64, peakFrac float64) {
	if pr, ok := inner.(PipelineReporter); ok {
		st := pr.PipeStats()
		return st.PubStalledMs, st.PubChanges, st.PubHash, st.PubPeakFrac
	}
	return -1, 0, 0, 0
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
