package mediapipe

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
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

// encoder implements medialink.Source (+KeyframeSource, RateControlSource, PipelineReporter) over
// the ffmpeg child.
var _ medialink.RateControlSource = (*encoder)(nil)

type encoder struct {
	log           *logbus.Bus
	ffmpeg        string
	spec          medialink.EncodeSpec // live (SetRateHint lowers spec.MaxHeight under mu); read snapshotted
	baseMaxHeight int                  // negotiated downscale ceiling; the hint never exceeds it (restore target)
	src           medialink.Source
	size          int // bytes per raw input frame

	frames chan *medialink.Frame
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	ptsq     []ptsEntry
	runStop  context.CancelFunc // cancels the CURRENT child only (keyframe restart)
	lastKey  int64              // unix ns of the last emitted keyframe
	lastKick int64              // unix ns of the last forced restart
	kicked   bool               // runStop was a deliberate respawn: restart at once, no backoff
	restarts int
	srcEOF   bool
	rt       *retarget // live bitrate/fps -> respawn decision (guarded by mu)

	gateFPS  atomic.Int32  // receiver fps cap applied pre-stdin (0 = none); feeder-owned fpsGate reads it
	fpsDrops atomic.Uint64 // frames dropped by the fps cap (deliberate: RateCapped, not loss)

	out     rate
	dropped atomic.Uint64 // undersized/foreign input frames skipped
	framed  atomic.Bool   // child emitted at least one AU (hw-scaler demotion probe)
	swScale atomic.Bool   // MaxHeight downscale pinned to CPU swscale (GPU scaler failed here)
	resCap  *resCapGate   // anti-flap gate shared in spirit with the native bridge (per-engine instance)
}

// newEncoder validates the spec and starts the supervision loop.
func newEncoder(ctx context.Context, log *logbus.Bus, ffmpeg string, spec medialink.EncodeSpec, src medialink.Source) (*encoder, error) {
	if spec.Width <= 0 || spec.Height <= 0 || spec.Width > 16384 || spec.Height > 16384 {
		return nil, fmt.Errorf("mediapipe: bad encode size %dx%d", spec.Width, spec.Height)
	}
	// Resolve the VA-API render node once, here, so the encode child's argv names the node whose
	// driver actually has the encode entrypoint (not a blind default). No-op off Linux / non-vaapi.
	if strings.HasSuffix(spec.Encoder, "_vaapi") && spec.VAAPIRenderNode == "" {
		spec.VAAPIRenderNode = ensureVAAPINode(ctx, ffmpeg)
	}
	cctx, cancel := context.WithCancel(ctx)
	if spec.FPS <= 0 {
		spec.FPS = 30
	}
	ceil := spec.BitrateKbps
	if ceil <= 0 { // encodeArgs' own default (output pixel rate), now owned here so hints can retarget it
		ow, oh := spec.Width, spec.Height
		if spec.MaxHeight > 0 && oh > spec.MaxHeight {
			ow, oh = ow*spec.MaxHeight/oh, spec.MaxHeight
		}
		ceil = defaultBitrateKbps(ow, oh, spec.FPS)
	}
	e := &encoder{
		log: log, ffmpeg: ffmpeg, spec: spec, baseMaxHeight: spec.MaxHeight, src: src,
		size:   spec.Width * spec.Height * 4,
		frames: make(chan *medialink.Frame, encFramesBuf),
		cancel: cancel, done: make(chan struct{}),
		resCap: newResCapGate(effCapHeight(spec.MaxHeight, spec.Height)),
		rt:     newRetarget(spec.Encoder, ceil, spec.FPS, time.Now()),
	}
	go e.run(cctx)
	go e.retargetLoop(cctx)
	return e, nil
}

// retargetLoop is the 1 Hz control tick: feeds the measured output to the closed-loop correction and
// respawns the child when the debounced spawn rate changed materially (hint or correction).
func (e *encoder) retargetLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			bps, _ := e.out.w.Rate(now)
			e.mu.Lock()
			e.rt.observe(now, bps*8/1000)
			e.retargetLocked(now, bps*8/1000)
			e.mu.Unlock()
		}
	}
}

// retargetLocked respawns the child when rt decides so (caller holds mu).
func (e *encoder) retargetLocked(now time.Time, measuredKbps float64) {
	if e.runStop == nil || !e.rt.decide(now) {
		return
	}
	w, cur := e.rt.want(), e.rt.running
	e.kickLocked(now)
	e.log.Info(source, "rate retarget: respawning the encode child (forced IDR, route stays up)", map[string]any{
		"encoder": e.spec.Encoder, "ceilingKbps": e.rt.ceilKbps, "spawnKbps": w.kbps, "wasKbps": cur.kbps,
		"scalePct": e.rt.scalePct, "fps": w.fps, "wasFps": cur.fps, "measuredKbps": int(measuredKbps)})
}

// kickLocked stops the current child for an immediate respawn (caller holds mu, runStop != nil).
func (e *encoder) kickLocked(now time.Time) {
	e.lastKick = now.UnixNano()
	e.kicked = true
	e.runStop()
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
		e.kickLocked(time.Unix(0, now))
	}
}

// SetRateHint implements medialink.RateControlSource for the ffmpeg substitute. ffmpeg's CLI has no
// live bitrate/fps channel, so:
//   - fps: LIVE - frames are dropped before stdin on an even PTS grid (fpsGate); the declared
//     -framerate (RC bits/frame) follows on the next debounced respawn.
//   - bitrate: the child respawns with the new ceiling when it differs materially (retarget: >= 15%,
//     <= 1 respawn / 5 s; forced IDR, route stays up).
//   - RESOLUTION: the governor's last-resort VRAM step updates the downscale ceiling and respawns at once
//     (resCap anti-flap gate). Target = SMALLER of the negotiated ceiling and the hint; a cleared hint
//     (MaxHeight 0) restores it, so the state converges when the governor dedups to a single hint.
func (e *encoder) SetRateHint(h medialink.RateHint) {
	now := time.Now()
	e.mu.Lock()
	prevCap := e.rt.capFPS
	e.rt.hint(h.MaxBitrateKbps, h.MaxFPS)
	gate := 0
	if f := e.rt.effFPS(); f < e.rt.srcFPS {
		gate = int(f + 0.5)
	}
	e.gateFPS.Store(int32(gate))
	e.retargetLocked(now, 0)
	e.mu.Unlock()
	if h.MaxFPS != prevCap {
		e.log.Info(source, "rate hint: fps cap on the ffmpeg substitute (pre-stdin drop, live)", map[string]any{
			"encoder": e.spec.Encoder, "maxFPS": h.MaxFPS, "gateFps": gate})
	}
	want := e.baseMaxHeight
	if h.MaxHeight > 0 && (want == 0 || h.MaxHeight < want) {
		want = h.MaxHeight
	}
	// Route the target through the shared anti-flap gate (effective coded height; 0 -> source), so the
	// substitute respawns at most once per window and a restore needs the pressure sustained-gone.
	target, changed := e.resCap.decide(effCapHeight(want, e.spec.Height))
	if changed {
		newMax := target
		if target >= e.spec.Height {
			newMax = 0 // at/above source = no downscale
		}
		e.mu.Lock()
		e.spec.MaxHeight = newMax
		if e.runStop != nil {
			e.kickLocked(now) // respawn with the new downscale ceiling
		}
		e.mu.Unlock()
		e.log.Info(source, "rate hint: resolution cap applied via child respawn (VRAM last resort)",
			map[string]any{"maxHeight": newMax, "codedHeight": target, "hintHeight": h.MaxHeight, "base": e.baseMaxHeight})
	}
}

// PipeStats implements medialink.PipelineReporter.
func (e *encoder) PipeStats() medialink.PipelineStats {
	e.mu.Lock()
	restarts, ceil := e.restarts, e.rt.ceilKbps
	e.mu.Unlock()
	return medialink.PipelineStats{Encoder: e.spec.Encoder, OutFPS: e.out.value(), Restarts: restarts,
		Dropped:       e.dropped.Load() + medialink.InnerDrops(e.src),
		RateCapped:    medialink.InnerRateCapped(e.src) + e.fpsDrops.Load(),
		QualityTarget: e.spec.QualityTarget, BudgetKbps: ceil}
}

// hwScaling reports whether the current argv puts a GPU scaler in the filter chain.
func (e *encoder) hwScaling() bool {
	e.mu.Lock()
	maxH := e.spec.MaxHeight // mutable via SetRateHint
	e.mu.Unlock()
	return !e.swScale.Load() && maxH > 0 && e.spec.Height > maxH && hwScaleFamily(e.spec.Encoder)
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
		eof, kicked := e.srcEOF, e.kicked
		e.kicked = false
		e.mu.Unlock()
		if ctx.Err() != nil || eof {
			return
		}
		e.mu.Lock()
		e.restarts++
		e.mu.Unlock()
		if kicked && err == nil {
			continue // deliberate respawn (keyframe / retarget / resolution): no backoff - the gap is the spawn
		}
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
	spec := e.spec // snapshot: SetRateHint can lower spec.MaxHeight concurrently
	sr := e.rt.want()
	e.rt.spawned(sr, time.Now())
	spec.BitrateKbps, spec.FPS = sr.kbps, sr.fps
	ceil, scale := e.rt.ceilKbps, e.rt.scalePct
	e.mu.Unlock()
	// spawn-rate evidence on the rig (rare: route start, PLI, retarget <= 1/5 s)
	e.log.Info(source, "encode child spawn", map[string]any{"encoder": spec.Encoder,
		"ceilingKbps": ceil, "spawnKbps": sr.kbps, "scalePct": scale, "fps": sr.fps, "maxHeight": spec.MaxHeight})

	cmd := exec.CommandContext(rctx, e.ffmpeg, encodeArgs(spec, e.swScale.Load())...)
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
	// Realtime class: kill-on-close but NO CPU cap. This child has to drain a live frame rate;
	// parking it in the 10% batch bucket does not save the machine work - the sender still pays
	// full Spout readback + pipe writes for frames a starved encoder never consumes (that is the
	// spout-share melt). Bounding cost belongs upstream (fps/pixel-rate gates), not here.
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobRealtime) // app death reaps the tree too

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
	var gate fpsGate // receiver fps cap: drop here, on the source's newest-wins hand-off (no new queue)
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
		if !gate.pass(pts, int(e.gateFPS.Load())) {
			e.fpsDrops.Add(1)
			if f.Release != nil {
				f.Release()
			}
			continue
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
		e.out.tickBytes(len(au)) // bytes too: the retarget loop's measured output
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
