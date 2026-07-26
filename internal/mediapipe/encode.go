package mediapipe

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/sysexec"
)

// encode.go - the send-side encode child: raw RGBA frames from the inner Source ride ffmpeg's
// stdin; AUD-framed bitstream AUs come back on stdout and are emitted as medialink Frames with
// keyframe/config flags. Supervised (capped 1→10 s backoff); implements KeyframeSource by
// restarting the child (a fresh child opens with an IDR + parameter sets - dump_extra keeps
// every later keyframe self-contained too), rate-limited so PLI storms can't thrash the encoder.

const (
	encFramesBuf     = 4               // encoded-frame handoff (backpressure blocks the child)
	encPTSQueueCap   = 64              // in-flight PTS map guard (child pipeline depth ≪ this)
	encKeyRestartMin = 2 * time.Second // min gap between keyframe-forced restarts
	encKeyFreshNs    = int64(500 * time.Millisecond)
	encEarlyFailNs   = int64(3 * time.Second) // child dies without an AU within this → demote the hw scaler
)

type ptsEntry struct {
	pts int64
	tc  medialink.Timecode
}

// encoder implements medialink.Source (+KeyframeSource, PipelineReporter) over the ffmpeg child.
type encoder struct {
	log    *logbus.Bus
	ffmpeg string
	spec   medialink.EncodeSpec
	src    medialink.Source
	size   int // bytes per raw input frame

	frames chan *medialink.Frame
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	ptsq     []ptsEntry
	runStop  context.CancelFunc // cancels the CURRENT child only (keyframe restart)
	lastKey  int64              // unix ns of the last emitted keyframe
	lastKick int64              // unix ns of the last forced restart
	restarts int
	srcEOF   bool

	out     rate
	dropped atomic.Uint64 // undersized/foreign input frames skipped
	framed  atomic.Bool   // child emitted at least one AU (hw-scaler demotion probe)
	swScale atomic.Bool   // MaxHeight downscale pinned to CPU swscale (GPU scaler failed here)
}

// newEncoder validates the spec and starts the supervision loop.
func newEncoder(ctx context.Context, log *logbus.Bus, ffmpeg string, spec medialink.EncodeSpec, src medialink.Source) (*encoder, error) {
	if spec.Width <= 0 || spec.Height <= 0 || spec.Width > 16384 || spec.Height > 16384 {
		return nil, fmt.Errorf("mediapipe: bad encode size %dx%d", spec.Width, spec.Height)
	}
	cctx, cancel := context.WithCancel(ctx)
	e := &encoder{
		log: log, ffmpeg: ffmpeg, spec: spec, src: src,
		size:   spec.Width * spec.Height * 4,
		frames: make(chan *medialink.Frame, encFramesBuf),
		cancel: cancel, done: make(chan struct{}),
	}
	go e.run(cctx)
	return e, nil
}

// Next implements medialink.Source.
func (e *encoder) Next(ctx context.Context) (*medialink.Frame, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f, ok := <-e.frames:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	}
}

// Close implements medialink.Source: stops the child + supervision and closes the inner source.
func (e *encoder) Close() error {
	e.cancel()
	<-e.done
	return e.src.Close()
}

// RequestKeyframe implements medialink.KeyframeSource (§2.5 PLI): restart the child unless a
// keyframe is fresh or a restart just happened (ffmpeg's CLI has no live force-IDR channel -
// honest cost: one child respawn, ~100 ms hole covered by the receiver's waitKey policy).
func (e *encoder) RequestKeyframe() {
	now := time.Now().UnixNano()
	e.mu.Lock()
	defer e.mu.Unlock()
	if now-e.lastKey < encKeyFreshNs || now-e.lastKick < int64(encKeyRestartMin) {
		return
	}
	if e.runStop != nil {
		e.lastKick = now
		e.runStop()
	}
}

// PipeStats implements medialink.PipelineReporter.
func (e *encoder) PipeStats() medialink.PipelineStats {
	e.mu.Lock()
	restarts := e.restarts
	e.mu.Unlock()
	return medialink.PipelineStats{Encoder: e.spec.Encoder, OutFPS: e.out.value(), Restarts: restarts}
}

// hwScaling reports whether the current argv puts a GPU scaler in the filter chain.
func (e *encoder) hwScaling() bool {
	return !e.swScale.Load() && e.spec.MaxHeight > 0 && e.spec.Height > e.spec.MaxHeight &&
		hwScaleFamily(e.spec.Encoder)
}

// run supervises the child: spawn → pipe → restart with capped backoff until ctx or source EOF.
func (e *encoder) run(ctx context.Context) {
	defer close(e.done)
	defer close(e.frames)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := e.runOnce(ctx)
		e.mu.Lock()
		eof := e.srcEOF
		e.mu.Unlock()
		if ctx.Err() != nil || eof {
			return
		}
		e.mu.Lock()
		e.restarts++
		e.mu.Unlock()
		// GPU-scaler demotion (mirrors the decoder's hwaccel tier demotion): a child that dies
		// without ever emitting an AU while a hardware MaxHeight scaler was in the filter chain
		// means this ffmpeg build / driver has no usable scale_cuda|scale_qsv. Pin swscale and
		// retry at once - never loop forever on an argv the local ffmpeg rejects.
		if err != nil && !e.framed.Load() && e.hwScaling() &&
			time.Since(started) < time.Duration(encEarlyFailNs) {
			e.swScale.Store(true)
			e.log.Warn(source, "hardware downscaler unavailable - falling back to CPU swscale",
				map[string]any{"encoder": e.spec.Encoder, "maxHeight": e.spec.MaxHeight, "error": err.Error()})
			continue
		}
		if err != nil {
			e.log.Warn(source, "encode child exited - restarting",
				map[string]any{"encoder": e.spec.Encoder, "error": err.Error(), "backoff": backoff.String()})
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// runOnce runs one child to completion: feeder (src→stdin) + reader (stdout→AUs→frames).
func (e *encoder) runOnce(ctx context.Context) error {
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	e.mu.Lock()
	e.runStop = rcancel
	e.ptsq = e.ptsq[:0]
	e.mu.Unlock()

	cmd := exec.CommandContext(rctx, e.ffmpeg, encodeArgs(e.spec, e.swScale.Load())...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	tail := newRingWriter(1 << 10)
	cmd.Stderr = tail
	sysexec.Hide(cmd)
	// Kill the whole tree on stop: PATH ffmpeg is often a launcher shim whose real child
	// otherwise orphans and keeps encoding (webcam/capture.go precedent).
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil }
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	sysexec.AssignToJob(cmd.Process, true) // app death reaps the tree too

	feedErr := make(chan error, 1)
	go func() { feedErr <- e.feed(rctx, stdin) }()
	rerr := e.readAUs(rctx, stdout)
	ferr := <-feedErr
	werr := cmd.Wait()
	if rctx.Err() != nil {
		return nil // deliberate stop (route teardown or keyframe restart)
	}
	if ferr != nil {
		return ferr
	}
	if rerr != nil {
		return fmt.Errorf("au pipe: %w", rerr)
	}
	if werr != nil {
		return fmt.Errorf("ffmpeg: %v - %s", werr, tail.String())
	}
	return nil
}

// feed pumps raw frames from the inner source into the child's stdin. Source EOF closes stdin
// (child flushes + exits) and flags the whole encoder done.
func (e *encoder) feed(ctx context.Context, stdin io.WriteCloser) error {
	defer func() { _ = stdin.Close() }()
	for {
		f, err := e.src.Next(ctx)
		if err != nil {
			if err == io.EOF {
				e.mu.Lock()
				e.srcEOF = true
				e.mu.Unlock()
				return nil
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if f.Kind != medialink.KindVideo || len(f.Payload) != e.size {
			e.dropped.Add(1)
			if f.Release != nil {
				f.Release()
			}
			continue
		}
		pts := f.PTS
		if pts == 0 {
			pts = time.Now().UnixNano()
		}
		e.mu.Lock()
		if len(e.ptsq) < encPTSQueueCap {
			e.ptsq = append(e.ptsq, ptsEntry{pts: pts, tc: f.TC})
		}
		e.mu.Unlock()
		_, werr := stdin.Write(f.Payload)
		if f.Release != nil {
			f.Release() // stdin.Write copied into the pipe - pooled buffer is free
		}
		if werr != nil {
			return nil // child died - Wait reports why
		}
	}
}

// readAUs splits the child's stdout into access units and emits them as frames.
func (e *encoder) readAUs(ctx context.Context, stdout io.Reader) error {
	emit := func(au []byte, info auInfo) {
		e.framed.Store(true)
		f := &medialink.Frame{Kind: medialink.KindVideo, Codec: e.spec.Codec, Payload: au}
		if info.Keyframe {
			f.Flags |= medialink.FlagKeyframe
			e.mu.Lock()
			e.lastKey = time.Now().UnixNano()
			e.mu.Unlock()
		}
		if info.Config {
			f.Flags |= medialink.FlagConfig
		}
		// No B-frames → output order == input order: pop the matching input PTS/TC.
		// Config-only AUs carry no picture - they reuse (not consume) the pending PTS.
		e.mu.Lock()
		if len(e.ptsq) > 0 {
			f.PTS, f.TC = e.ptsq[0].pts, e.ptsq[0].tc
			if !info.Config {
				e.ptsq = e.ptsq[1:]
			}
		}
		e.mu.Unlock()
		e.out.tick()
		select {
		case e.frames <- f:
		case <-ctx.Done(): // consumer gone - child teardown drains the rest
		}
	}
	var w io.Writer
	var flush func()
	if e.spec.Codec == medialink.CodecJPEG {
		w = newJPEGSplitter(emit) // EOI-framed: nothing to flush
	} else {
		as := newAUSplitter(e.spec.Codec == medialink.CodecHEVC, emit)
		w, flush = as, as.flush
	}
	_, err := io.Copy(w, stdout)
	if flush != nil && ctx.Err() == nil {
		flush() // stream end: the last AU has no following AUD
	}
	if err == io.ErrClosedPipe {
		return nil
	}
	return err
}
