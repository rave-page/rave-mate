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

// Factories returns the medialink Encoder/Decoder factories: H.264 encodes prefer the
// native Media Foundation hardware pipeline (mfenc: no ffmpeg child, no raw stdin pipe,
// live forced IDRs); everything else - and any mfenc failure - runs the ffmpeg child.
func Factories(log *logbus.Bus) (medialink.EncoderFactory, medialink.DecoderFactory) {
	var mfWarned bool
	enc := func(ctx context.Context, spec medialink.EncodeSpec, src medialink.Source) (medialink.Source, error) {
		if spec.Codec == medialink.CodecH264 && mfenc.Available() {
			s, err := newMFBridge(ctx, log, spec, src)
			if err == nil {
				return s, nil
			}
			if !mfWarned {
				mfWarned = true
				log.Warn(source, "native MF encoder unavailable - using the ffmpeg child", map[string]any{"err": err.Error()})
			}
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
