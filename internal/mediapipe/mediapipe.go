// Package mediapipe is the medialink P4 encode/decode pipeline (MEDIALINK_DESIGN.md §3.2):
// supervised ffmpeg children turning raw RGBA frames into H.264/HEVC/MJPEG bitstreams and back,
// plugged into the route manager through the medialink Encoder/Decoder factory seams. Children
// mirror internal/webcam/capture.go supervision (mediatools.Resolve + exec.CommandContext +
// sysexec.Hide, cmd.Cancel = KillTree + AssignToJob - PATH shims orphan otherwise, restart with
// capped 1→10 s backoff).
//
// Encoder capability is probed once per launch (`ffmpeg -encoders` + a tiny test encode per
// candidate - a listed HW encoder can still fail at runtime) and cached; decode capability =
// build-listed decoders; hw decode = probed `-hwaccels`, tried per tier with sw fallback.
//
// P4a scope note: AV1 is EXCLUDED from the probe (tiers 1/4b unreachable) - raw AV1 has no
// Annex-B AUD framing; per-frame splitting needs OBU parsing (or IVF mux) - deferred to the P8
// vendor-SDK pass. H.264/HEVC frame the elementary stream via inserted AUDs
// (h264_metadata/hevc_metadata bsf) + dump_extra so every keyframe carries its parameter sets
// (mid-stream join / decoder restart). Honest cost: AU boundaries are only known when the NEXT
// AUD arrives → ~1 frame added latency on the encode path (§3.2 pipe-path budget).
package mediapipe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/mfenc"
)

const source = "mediapipe"

// Factories returns the medialink Encoder/Decoder factories. The ENGINE IS KEYED ON THE NEGOTIATED
// ENCODER NAME, never on the codec: medialink.EncoderMFNative (advertised only when mfenc really
// works) runs the native Media Foundation pipeline - no ffmpeg child, no raw stdin pipe, live forced
// IDRs - and every other name runs the ffmpeg child. Keying on Codec==H264 (the old rule) silently
// ran mfenc for a negotiated libx264/h264_nvenc, so the Answer's encoder + the route's software/tier
// stats described an engine that wasn't running, and SWOnly's "force software" ran on GPU silicon.
func Factories(log *logbus.Bus) (medialink.EncoderFactory, medialink.DecoderFactory) {
	var mfWarned bool
	enc := func(ctx context.Context, spec medialink.EncodeSpec, src medialink.Source) (medialink.Source, error) {
		if encodeEngine(spec) == engineMFNative {
			if mfenc.Available() {
				s, err := newMFBridge(ctx, log, spec, src)
				if err == nil {
					return s, nil
				}
				if !mfWarned {
					mfWarned = true
					log.Warn(source, "native MF encoder failed to open - falling back to the ffmpeg child (raw stdin pipe: expect higher sender load)", map[string]any{"err": err.Error()})
				}
			}
			// The peer was answered EncoderMFNative, i.e. H.264. Substitute a real ffmpeg H.264
			// encoder so the wire codec still matches the answer; the peer decodes it unchanged.
			sub, ok := ffmpegH264Fallback()
			if !ok {
				return nil, fmt.Errorf("mediapipe: native MF encode unavailable and no ffmpeg H.264 encoder probed")
			}
			log.Warn(source, "substituting an ffmpeg H.264 encoder for the native MF engine", map[string]any{"encoder": sub})
			spec.Encoder = sub
			spec.Software = sub == "libx264"
		}
		ffmpeg, ok := mediatools.Resolve("ffmpeg")
		if !ok {
			return nil, fmt.Errorf("mediapipe: ffmpeg not found")
		}
		return newEncoder(ctx, log, ffmpeg, spec, src)
	}
	dec := func(ctx context.Context, spec medialink.DecodeSpec, sink medialink.Sink) (medialink.Sink, error) {
		ffmpeg, ok := mediatools.Resolve("ffmpeg")
		if !ok {
			return nil, fmt.Errorf("mediapipe: ffmpeg not found")
		}
		return newDecoder(ctx, log, ffmpeg, spec, sink), nil
	}
	return enc, dec
}

// encodeEngineKind names an encode engine.
type encodeEngineKind int

const (
	engineFfmpegChild encodeEngineKind = iota // supervised ffmpeg child, raw RGBA over stdin
	engineMFNative                            // internal/mfenc: pipe-free, in-process, GPU-resident
)

// encodeEngine reports which engine serves a spec. Keyed on the NEGOTIATED ENCODER NAME so the
// engine always matches the name the peer was answered with. The old rule (Codec == CodecH264) sent
// every H.264 spec to mfenc, including a negotiated libx264 - the route then reported a tier-4
// software encode while hardware silicon did the work, and SWOnly ("force software") didn't.
func encodeEngine(spec medialink.EncodeSpec) encodeEngineKind {
	if spec.Encoder == medialink.EncoderMFNative {
		return engineMFNative
	}
	return engineFfmpegChild
}

// h264FallbackOrder is the ffmpeg H.264 encoder preference when the native MF engine can't open:
// hardware first (still one GPU, just with the pipe), CPU last.
var h264FallbackOrder = []string{"h264_nvenc", "h264_qsv", "h264_amf", "h264_mf", "h264_vaapi",
	"h264_videotoolbox", "h264_v4l2m2m", "libx264"}

// ffmpegH264Fallback picks the best PROBED ffmpeg H.264 encoder (never triggers a probe).
func ffmpegH264Fallback() (string, bool) {
	caps, ok := Cached()
	if !ok {
		return "", false
	}
	return pickH264(caps.Encoders)
}

// pickH264 returns the preferred H.264 encoder present in encoders (pure; h264FallbackOrder).
func pickH264(encoders []string) (string, bool) {
	have := make(map[string]bool, len(encoders))
	for _, e := range encoders {
		have[e] = true
	}
	for _, e := range h264FallbackOrder {
		if have[e] {
			return e, true
		}
	}
	return "", false
}

// sleepCtx sleeps d or until ctx cancel; false when cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ringWriter keeps the last cap bytes written (ffmpeg stderr tail for error reporting).
type ringWriter struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newRingWriter(capacity int) *ringWriter { return &ringWriter{cap: capacity} }

func (w *ringWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.cap {
		w.buf = append(w.buf[:0], w.buf[len(w.buf)-w.cap:]...)
	}
	w.mu.Unlock()
	return len(p), nil
}

func (w *ringWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

// rate is a coarse frames/s estimator (anchor refreshed on read, ≥500 ms apart).
type rate struct {
	mu     sync.Mutex
	n      uint64
	anchor uint64
	at     time.Time
	fps    float64
}

func (r *rate) tick() { r.mu.Lock(); r.n++; r.mu.Unlock() }

func (r *rate) value() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.at.IsZero() {
		r.at, r.anchor = now, r.n
		return 0
	}
	if d := now.Sub(r.at); d >= 500*time.Millisecond {
		r.fps = float64(r.n-r.anchor) / d.Seconds()
		r.at, r.anchor = now, r.n
	}
	return r.fps
}
