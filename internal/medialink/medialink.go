// Package medialink is rave-mate's inter-instance MEDIA plane - the low-latency, high-quality
// audio/video transport that replaces NDI on a LAN. It's the data plane counterpart to peerlink
// (which stays the JSON control plane: discovery, pairing/auth, capability routing, negotiation).
//
// Design:
//   - Any source → any sink across instances: Spout→Spout, webcam→Spout, audio→audio device.
//     Sources (Spout receiver / webcam / audio capture) and Sinks (Spout sender / audio out) are
//     small interfaces (route.go); the hardware-bound implementations live behind build tags and
//     are wired later - this package owns the protocol + timecode + transport, which are pure Go
//     and fully unit-testable without hardware.
//   - A dedicated binary media socket (transport.go), NOT peerlink's JSON text frames: peerlink
//     wraps every payload as a MAC'd canonical-JSON string, fatal for per-frame A/V. Here each
//     frame is a compact binary header + payload, AEAD-sealed (AES-256-GCM) with a per-session key
//     derived (HKDF) from the peerlink handshake - this is also the link encryption peerlink still
//     lacks.
//   - Every frame carries a PTS (nanoseconds on a shared media clock) and an optional SMPTE
//     timecode (timecode.go / ltc.go), so receivers present video+audio in lock-step and rave-mate
//     can be an LTC/MTC timecode master or slave for Resolume / lighting / DAWs.
//   - LAN-optimised: uncompressed frames stay uncompressed on the same PC (Spout GPU texture);
//     over the wire, video rides the already-probed HW encoders (worker/encoders.go) and audio
//     rides PCM (cheap on GbE, zero codec latency) - the codec is negotiated, not fixed here.
//
// Standards compliance:
//   - Crypto: AES-256-GCM (FIPS 197 + NIST SP 800-38D); 96-bit deterministic nonce = 32-bit fixed
//     field + 64-bit invocation counter per SP 800-38D §8.2.1 (unique per HKDF-derived per-session,
//     per-direction key, so never reused). Key derivation: HKDF-SHA-256 (RFC 5869). A future UDP
//     profile MUST carry an explicit sequence and follow SRTP (RFC 3711) nonce/replay rules.
//   - Timecode: SMPTE ST 12M drop-frame counting; LTC is real SMPTE ST 12M-1 biphase-mark (24/25/30
//     incl. 29.97 drop) - >30 fps is refused (needs ST 12M-2 field-doubling) rather than mis-encoded.
//   - Wire format is proprietary by design (like NDI) for instance↔instance LAN links; the frame
//     header is RTP-analogous (RFC 3550: sequence, timestamp, payload type, marker≈keyframe). The
//     STANDARD-compliant surfaces are the endpoints: Spout (video), the audio device, and LTC/MTC
//     out. Standards-based egress - AES67 (RFC 3550 + L16/L24 RFC 3190) audio, SMPTE ST 2110 video,
//     NDI compat - is planned as an interop sink so external gear (Resolume/DAWs/lighting) can
//     receive directly. The internal transport stays proprietary for minimum latency.
//
// Code-unit → MEDIALINK_DESIGN.md section map (see the "P1/P2 decisions" appendices):
//
//	frame.go        §2.1 wire (fixed 26B header, KindRepair reservation), §3.1 payload cap
//	transport.go    §1 Conn (AES-256-GCM, HKDF per-direction keys), §2.1 SRTP note, §2.1 rekey budget
//	metaframe.go    §2.1 RTCP-semantic reports + §2.5 NACK meta types (frozen v1) + P2 sync/syncr
//	negotiate.go    §1 Advert/Offer/Answer + §2.1 transport/nack/fec reservations + P2 Caps extension
//	mediaclock.go   §2.3 three-tier ClockSource seam (monotonic tier; PTP/follow-master P8)
//	syncclock.go    §2.3 tier 2: NTP-style clock filter (OffsetEstimator) + SoftwareClock (P2)
//	telemetry.go    §7 per-route stats: RFC 3550 §A.8 jitter, §A.3 loss, latency window, reports (P2)
//	nack.go         §2.5 NACK/retransmit: bounded retransmit buffer + KeyframeSource PLI seam
//	codec.go        §3.2 codec capability matrix (tiered NVENC/QSV/AMF → sw fallback → MJPEG)
//	pipeline.go     §3.2 encode/decode child seams (P4; ffmpeg children live in mediapipe)
//	jitterbuf.go    §3.3 adaptive receive jitter buffer + keyframe-aware policy drops (P4)
//	route.go        §1 Source/Sink any-to-any Pump glue
//	router.go       §8 session layer: listener (TCP 47641-47645, NODELAY), offer→answer→dial→Pump,
//	                caps grant, report/sync/NACK loops
//	tcplane.go      §4 timecode plane: TC master election, media.tc announces, slave follow (P3)
//	timecode.go     §4 SMPTE ST 12M timecode (READ-ONLY here; LTC audio egress owned elsewhere)
//	ltc.go          §4 SMPTE ST 12M-1 LTC (READ-ONLY here)
package medialink

// Kind is a media stream's data type.
type Kind uint8

const (
	KindMeta   Kind = 0 // config / control (stream metadata, RTCP-style reports, NACK - see metaframe.go)
	KindVideo  Kind = 1
	KindAudio  Kind = 2
	KindRepair Kind = 3 // FEC repair frames (§2.5 reservation; carried, never generated in P1–P7)
)

// Codec identifies a frame payload's encoding. Raw-pixel/PCM codecs stay uncompressed (same-PC or
// GbE); H264/HEVC/AV1 ride the host's HW encoder for cross-PC HD.
type Codec uint8

const (
	CodecNone   Codec = 0
	CodecBGRA   Codec = 1 // uncompressed 32-bit BGRA (Spout/webcam frames, same-PC or GbE)
	CodecNRGBA  Codec = 2 // uncompressed 32-bit RGBA (Go image.NRGBA order)
	CodecJPEG   Codec = 3 // motion-JPEG (cheap intra, tolerant to loss)
	CodecH264   Codec = 4
	CodecHEVC   Codec = 5
	CodecAV1    Codec = 6
	CodecPCMS16 Codec = 16 // interleaved signed 16-bit PCM
	CodecPCMS24 Codec = 17 // interleaved signed 24-bit PCM (packed 3-byte)
	CodecPCMF32 Codec = 18 // interleaved 32-bit float PCM
	CodecOpus   Codec = 19
)

// Flag bits on a frame.
type Flag uint8

const (
	FlagKeyframe Flag = 1 << 0 // decodable without prior frames (video I-frame)
	FlagConfig   Flag = 1 << 1 // codec config / extradata (SPS/PPS, stream params), not displayable
	FlagEnd      Flag = 1 << 2 // end of stream
)

// Frame is one unit on a media stream. PTS is nanoseconds on the shared clock; TC is an optional
// SMPTE timecode (zero = none). Payload holds the (possibly encoded) media bytes.
type Frame struct {
	Stream  uint16 // stream id within the session (a session may carry several)
	Kind    Kind
	Codec   Codec
	Flags   Flag
	Seq     uint32 // per-stream monotonic; gap = loss
	PTS     int64  // presentation time, nanoseconds on the shared media clock
	TC      Timecode
	Payload []byte
	// Release recycles Payload's buffer once the frame is fully consumed (nil = GC).
	// A consumer that retains Payload (retransmit buffer) must NOT call it.
	Release func()
}

// Keyframe reports whether the frame is independently decodable.
func (f *Frame) Keyframe() bool { return f.Flags&FlagKeyframe != 0 }
