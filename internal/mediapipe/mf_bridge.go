package mediapipe

// mf_bridge.go - native Media Foundation hardware encode as a medialink Source: the
// preferred H.264 engine on Windows. Frames go over a shared-memory ring to the
// supervised per-adapter Zig encoder child (native/zigenc: D3D11 upload →
// VideoProcessorBlt CSC/scale → encoder silicon) and come back as annex-B AUs with PTS
// preserved - no ffmpeg child, no multi-GB/s stdin pipe, and a vendor-driver fault kills
// only the encoder child (supervisor restarts it; the route survives). ffmpeg remains
// the fallback engine and the decode side. Scaling (spec.MaxHeight) rides the same Blt.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mfenc"
)

// mfBridge implements medialink.Source (+KeyframeSource, PipelineReporter) over an
// mfenc encoder-child session.
type mfBridge struct {
	log    *logbus.Bus
	enc    *mfenc.ProcSession
	src    medialink.Source
	size   int
	frames chan *medialink.Frame
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	tcq     []medialink.Timecode // FIFO pts→TC carry (no B-frames: output order = input order)
	lastKey int64

	out rate
	// dropped: undersized/foreign input frames. Atomic - feed() writes it, PipeStats reads it
	// from the telemetry goroutine (it was a plain int, i.e. a data race that also never
	// reached any surface).
	dropped    atomic.Uint64
	zeroCopy   bool
	downgrades int
}

// ZeroCopyCapture gates the zigmedia inc-1 path: a Spout source's pixels reach the encoder child
// as a GPU shared-texture handle instead of a host readback. Package seam (same shape as
// mfenc.Warnf) - the daemon and the media child both point it at their live config. Default OFF.
var ZeroCopyCapture = func() bool { return false }

// newMFBridge builds the native pipeline for spec; error = caller falls back to ffmpeg.
func newMFBridge(ctx context.Context, log *logbus.Bus, spec medialink.EncodeSpec, src medialink.Source) (*mfBridge, error) {
	if spec.Width <= 0 || spec.Height <= 0 || spec.Width > 16384 || spec.Height > 16384 {
		return nil, fmt.Errorf("mfenc: bad encode size %dx%d", spec.Width, spec.Height)
	}
	fps := spec.FPS
	if fps <= 0 {
		fps = 30
	}
	outW, outH := spec.Width, spec.Height
	if spec.MaxHeight > 0 && outH > spec.MaxHeight {
		outW = outW * spec.MaxHeight / outH
		outH = spec.MaxHeight
	}
	outW, outH = outW&^1, outH&^1 // encoders want even dims
	kbps := spec.BitrateKbps
	if kbps <= 0 {
		kbps = defaultBitrateKbps(outW, outH, fps)
	}
	// Device preference (WP-3): the native engine takes the adapter LUID directly, so it is the
	// device-steerable H.264 path on every vendor (ffmpeg has no device flag for AMF at all). An
	// unusable LUID degrades to the default adapter inside the shim.
	var luid int64
	if key, _, ok := spec.Device(); ok {
		if v, ok := encoderscan.LUIDInt64(key); ok {
			luid = v
		}
	}
	opts := mfenc.ProcOpts{LUID: luid, InW: spec.Width, InH: spec.Height, OutW: outW, OutH: outH,
		FPS: fps, Kbps: kbps, Gop: gopFrames(fps)}
	// Zero-copy request (zigmedia inc 1) needs ALL of: the flag, a source that really exposes a
	// shared texture, a handle that matches the negotiated geometry, and a sender not already
	// pinned to the readback path. Anything missing = today's frame path, byte for byte.
	zc, downgrades := zeroCopyOpts(&opts, spec, src)
	enc, err := mfenc.OpenProcSessionOpts(opts)
	if err != nil && zc && errors.Is(err, mfenc.ErrZeroCopyRefused) {
		// Same child, same geometry, reopened on the readback path. ONE warn per route so a rig
		// that always downgrades is visible instead of silently slow.
		log.Warn(source, "zero-copy capture refused - reopening this route on the readback path",
			map[string]any{"err": err.Error(), "sender": opts.Spout.Name})
		opts.Spout, zc = nil, false
		downgrades++
		enc, err = mfenc.OpenProcSessionOpts(opts)
	}
	if err != nil {
		return nil, err
	}
	bctx, cancel := context.WithCancel(ctx)
	b := &mfBridge{log: log, enc: enc, src: src, size: spec.Width * spec.Height * 4,
		frames: make(chan *medialink.Frame, encFramesBuf), cancel: cancel, done: make(chan struct{}),
		zeroCopy: zc, downgrades: downgrades}
	log.Info(source, "native MF hardware encode (isolated encoder child)", map[string]any{
		"encoder": enc.Name(), "in": fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"out": fmt.Sprintf("%dx%d", outW, outH), "kbps": kbps, "swizzle": enc.InputIsBGRA(),
		"device": spec.DeviceLUID, "capture": captureLabel(zc)})
	// A zero-copy session's pixels never pass through here: no feed goroutine, so the source's
	// capture is never attached and no readback is ever performed (mediaroute attaches lazily).
	if !zc {
		go b.feed(bctx)
	}
	go b.emit(bctx)
	return b, nil
}

func captureLabel(zeroCopy bool) string {
	if zeroCopy {
		return "zerocopy"
	}
	return "readback"
}

// zeroCopyOpts fills opts.Spout when the whole gate holds; returns whether zero-copy was
// requested and how many downgrade decisions were already taken (for route stats).
func zeroCopyOpts(opts *mfenc.ProcOpts, spec medialink.EncodeSpec, src medialink.Source) (bool, int) {
	if !ZeroCopyCapture() {
		return false, 0
	}
	zcs, ok := src.(medialink.ZeroCopySource)
	if !ok {
		return false, 0
	}
	h, _, w, hh, name, ok := zcs.SharedTexture()
	if !ok || h == 0 {
		return false, 0
	}
	if w != spec.Width || hh != spec.Height {
		return false, 1 // sender moved between advert and open: the child would refuse anyway
	}
	if mfenc.ZeroCopyPinnedToReadback(name) {
		return false, 1
	}
	// Re-read on every (re)open + on the 2 s health tick: a restarted sender must never be
	// re-issued its dead handle (risk R1, the silently frozen picture).
	opts.Spout = &mfenc.SpoutSource{Name: name, Resolve: func() (uint64, uint32, int, int, bool) {
		hd, f, ww, hgt, _, ok := zcs.SharedTexture()
		return hd, f, ww, hgt, ok
	}}
	return true, 0
}

// feed pumps raw frames into the encoder; source EOF drains + closes it.
func (b *mfBridge) feed(ctx context.Context) {
	defer b.enc.Close() // drains; Output closes after tail AUs
	for {
		f, err := b.src.Next(ctx)
		if err != nil {
			return // EOF / cancel - drain via defer
		}
		if f.Kind != medialink.KindVideo || len(f.Payload) != b.size {
			b.dropped.Add(1)
			if f.Release != nil {
				f.Release()
			}
			continue
		}
		pts := f.PTS
		if pts == 0 {
			pts = time.Now().UnixNano()
		}
		b.mu.Lock()
		if len(b.tcq) < encPTSQueueCap {
			b.tcq = append(b.tcq, f.TC)
		}
		b.mu.Unlock()
		err = b.enc.Encode(f.Payload, pts)
		if f.Release != nil {
			f.Release() // Encode returns after the GPU upload copied the rows - buffer is free
		}
		if err != nil {
			if ctx.Err() == nil {
				b.log.Warn(source, "MF encode failed - route ends", map[string]any{"err": err.Error()})
			}
			return
		}
	}
}

// emit converts encoder AUs to medialink frames.
func (b *mfBridge) emit(ctx context.Context) {
	defer close(b.done)
	for au := range b.enc.Output() {
		f := &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecH264,
			PTS: au.PTSNs, Payload: au.Data}
		if au.Keyframe {
			f.Flags |= medialink.FlagKeyframe
			b.mu.Lock()
			b.lastKey = time.Now().UnixNano()
			b.mu.Unlock()
		}
		b.mu.Lock()
		if len(b.tcq) > 0 {
			f.TC = b.tcq[0]
			b.tcq = b.tcq[1:]
		}
		b.mu.Unlock()
		b.out.tick()
		select {
		case b.frames <- f:
		case <-ctx.Done():
			return
		}
	}
}

// Next implements medialink.Source.
func (b *mfBridge) Next(ctx context.Context) (*medialink.Frame, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f, ok := <-b.frames:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	case <-b.done:
		select {
		case f, ok := <-b.frames:
			if ok {
				return f, nil
			}
		default:
		}
		return nil, io.EOF
	}
}

// Close implements medialink.Source.
func (b *mfBridge) Close() error {
	b.cancel()
	if b.zeroCopy {
		b.enc.Close() // no feed goroutine owns the session on this path (its defer normally does)
	}
	return b.src.Close()
}

// RequestKeyframe implements medialink.KeyframeSource: a LIVE forced IDR (no child
// restart, no stream hole) - rate-limited so PLI storms stay cheap anyway.
func (b *mfBridge) RequestKeyframe() {
	b.mu.Lock()
	fresh := time.Now().UnixNano()-b.lastKey < encKeyFreshNs
	b.mu.Unlock()
	if !fresh {
		b.enc.ForceKeyframe()
	}
}

// PipeStats implements medialink.PipelineReporter (per-session perf: p99 rise is the
// Phase-2 governor's early saturation signal).
func (b *mfBridge) PipeStats() medialink.PipelineStats {
	st := b.enc.Stats()
	return medialink.PipelineStats{Encoder: medialink.EncoderMFNative, OutFPS: b.out.value(),
		Restarts: st.Restarts, LatP50Ms: st.LatP50Ms, LatP99Ms: st.LatP99Ms,
		QueueDepth: st.QueueDepth, ChildCPUPct: st.ChildCPUPct,
		ZeroCopy: st.ZeroCopy, CapFPS: st.CapFPS, CapSkips: st.CapSkips,
		MtxTimeouts: st.MtxTimeouts, SrcErrors: st.SrcErrors, CapStaleMs: st.CapStaleMs,
		EncBusyMs:  st.EncBusyMs,
		Downgrades: b.downgrades + st.Downgrades,
		Dropped:    b.dropped.Load() + medialink.InnerDrops(b.src)}
}
