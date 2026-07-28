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

// decode.go - the receive-side decode child: compressed AUs ride ffmpeg's stdin, raw RGBA frames
// come back on stdout (fixed W·H·4 stride framing) and flow into the inner sink. Supervised like
// the encoder; hw decode tiers (probed hwaccels: cuda → qsv → d3d11va → dxva2) with software
// fallback - a child that dies before producing a frame demotes to the next tier. After any
// restart the decoder drops non-keyframe input (undecodable) until the next keyframe.

const (
	decEarlyFailNs  = int64(3 * time.Second) // child dies unframed within this → demote tier
	decRespawnDelay = time.Second            // min gap between respawns (frames drop meanwhile)
	// decFreeRingMax is the DEPTH ceiling of the recycled raw-frame ring. Frames are written
	// synchronously and recycled immediately, so 1 is the steady-state need; 4 covers a reader
	// goroutine still draining an old child while a restarted one runs.
	decFreeRingMax = 4
	// decFreeRingBytes is the real bound: PARKED bytes, not frames. The old code fixed the depth at
	// 4 and its comment claimed "32 MB at 1080p" - but a 1080p frame is 8.3 MB, so 4 of them are
	// 33 MB and at 4K the same ring parks 132 MB per receive route, where the native decode path
	// parks 4 MiB of AU ring. A frame-shaped cap on a geometry-dependent buffer is how a bound that
	// reads correct scales 16x with the user's canvas.
	decFreeRingBytes = 32 << 20
)

// decFreeRingDepth sizes the recycle ring so PARKED bytes stay under decFreeRingBytes whatever the
// geometry: 4 at 720p, 3 at 1080p, 1 at 4K. Never 0 - getBuf allocates on an empty ring (fail open,
// same policy as the capture pool's PoolMiss), so a shallow ring degrades the optimisation instead
// of wedging a live route, and the LIVE frame each pump goroutine holds is unaffected either way.
func decFreeRingDepth(frameBytes int) int {
	if frameBytes <= 0 {
		return decFreeRingMax
	}
	n := decFreeRingBytes / frameBytes
	if n > decFreeRingMax {
		n = decFreeRingMax
	}
	if n < 1 {
		n = 1
	}
	return n
}

// decoder implements medialink.Sink (+PipelineReporter) over the ffmpeg child.
type decoder struct {
	log    *logbus.Bus
	ffmpeg string
	spec   medialink.DecodeSpec
	sink   medialink.Sink
	size   int // bytes per raw output frame

	accels []string    // remaining hwaccel candidates; accels[0] is in use ("" = software)
	free   chan []byte // bounded ring of recycled raw-frame buffers (decFreeRing)

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stop     context.CancelFunc
	readerWG sync.WaitGroup
	ptsq     []ptsEntry
	needKey  bool
	started  time.Time
	lastExit time.Time
	framed   atomic.Bool // child produced at least one output frame
	restarts int
	closed   bool

	out     rate
	dropped atomic.Uint64 // frames dropped awaiting a keyframe / foreign codec bypasses
}

// newDecoder builds the sink; the child spawns lazily on the first decodable frame.
func newDecoder(_ context.Context, log *logbus.Bus, ffmpeg string, spec medialink.DecodeSpec, sink medialink.Sink) *decoder {
	caps, _ := Probe(context.Background(), log)
	accels := append(append([]string{}, caps.HWAccels...), "") // "" = software floor
	size := spec.Width * spec.Height * 4
	return &decoder{
		log: log, ffmpeg: ffmpeg, spec: spec, sink: sink,
		size:   size,
		accels: accels,
		free:   make(chan []byte, decFreeRingDepth(size)),
	}
}

// Write implements medialink.Sink. Frames of another codec pass through untouched (defensive
// degrade - e.g. a raw route that never negotiated encode).
func (d *decoder) Write(f *medialink.Frame) error {
	if f.Codec != d.spec.Codec {
		return d.sink.Write(f)
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	if d.cmd == nil {
		if time.Since(d.lastExit) < decRespawnDelay { // respawn backoff: drop, counted
			d.dropped.Add(1)
			d.mu.Unlock()
			return nil
		}
		if err := d.spawnLocked(); err != nil {
			d.mu.Unlock()
			return err
		}
	}
	if d.needKey && !f.Keyframe() {
		d.dropped.Add(1)
		d.mu.Unlock()
		return nil
	}
	d.needKey = false
	if f.Flags&medialink.FlagConfig == 0 && len(d.ptsq) < encPTSQueueCap {
		d.ptsq = append(d.ptsq, ptsEntry{pts: f.PTS, tc: f.TC})
	}
	stdin := d.stdin
	d.mu.Unlock()

	if _, err := stdin.Write(f.Payload); err != nil {
		d.restart("stdin write failed")
	}
	return nil // child failures never kill the route; supervision recovers
}

// Close implements medialink.Sink: stop the child and close the inner sink.
func (d *decoder) Close() error {
	d.mu.Lock()
	d.closed = true
	stop := d.stop
	d.cmd, d.stdin, d.stop = nil, nil, nil
	d.mu.Unlock()
	if stop != nil {
		stop()
	}
	d.readerWG.Wait()
	return d.sink.Close()
}

// PipeStats implements medialink.PipelineReporter.
func (d *decoder) PipeStats() medialink.PipelineStats {
	d.mu.Lock()
	accel := ""
	if len(d.accels) > 0 {
		accel = d.accels[0]
	}
	restarts := d.restarts
	d.mu.Unlock()
	pubF, pubB := medialink.InnerPublished(d.sink)
	pubStale, pubChg, pubHash, pubPeak := medialink.InnerContent(d.sink)
	return medialink.PipelineStats{Encoder: "ffmpeg-decode", HWAccel: accel,
		OutFPS: d.out.value(), Restarts: restarts,
		Dropped:    d.dropped.Load() + medialink.InnerDrops(d.sink),
		RateCapped: medialink.InnerRateCapped(d.sink),
		PubFrames:  pubF, PubBytes: pubB,
		PubStalledMs: pubStale, PubChanges: pubChg, PubHash: pubHash, PubPeakFrac: pubPeak}
}

// spawnLocked starts a child on the current tier. Caller holds mu.
func (d *decoder) spawnLocked() error {
	accel := ""
	if len(d.accels) > 0 {
		accel = d.accels[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, d.ffmpeg, decodeArgs(d.spec, accel)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	tail := newRingWriter(1 << 10)
	cmd.Stderr = tail
	sysexec.Hide(cmd)
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil } // shim-orphan guard
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	// Realtime class (see encode.go): a decode child that misses the incoming frame rate stalls the
	// route and forces keyframe requests - a 10% CPU cap guarantees exactly that under load.
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobRealtime)
	d.cmd, d.stdin, d.stop = cmd, stdin, cancel
	d.started = time.Now()
	d.framed.Store(false)
	d.needKey = true // fresh decoder needs an IDR (+params via dump_extra) first
	d.ptsq = d.ptsq[:0]
	d.readerWG.Add(1)
	go d.readRaw(ctx, cmd, stdout, tail)
	return nil
}

// readRaw pumps decoded raw frames to the inner sink until the child exits, then handles
// restart/demotion bookkeeping.
func (d *decoder) readRaw(ctx context.Context, cmd *exec.Cmd, stdout io.Reader, tail *ringWriter) {
	defer d.readerWG.Done()
	d.pumpFrames(stdout)
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return // deliberate stop (Close/restart)
	}
	// Unplanned exit: demote the tier when the child never framed (hwaccel broken here).
	msg := "decode child exited"
	d.mu.Lock()
	early := !d.framed.Load() && time.Since(d.started) < time.Duration(decEarlyFailNs)
	if early && len(d.accels) > 1 {
		d.log.Warn(source, "hw decode tier failed - demoting", map[string]any{
			"hwaccel": d.accels[0], "error": errStr(werr), "tail": tail.String()})
		d.accels = d.accels[1:]
	}
	if d.cmd == cmd {
		d.cmd, d.stdin, d.stop = nil, nil, nil
		d.restarts++
	}
	d.lastExit = time.Now()
	d.mu.Unlock()
	if werr != nil {
		d.log.Warn(source, msg, map[string]any{"error": werr.Error(), "tail": tail.String()})
	}
}

// pumpFrames reassembles the child's fixed-size rawvideo output and writes each frame to the sink.
//
// Reads land DIRECTLY in the frame buffer, capped at the bytes still missing from the current
// frame, so a read can never spill into the next one. That means: one copy total (kernel → frame
// buffer), no staging slice, and no residual memmove per frame - the old loop appended into a
// staging buffer, copied out a fresh 8/33 MB payload, then memmoved the leftovers down, i.e. ~3
// full-frame passes plus an allocation per frame on the RECEIVING PC's hot path.
//
// Buffers come from a bounded free ring and are recycled right after Write returns - the Sink
// contract forbids retaining Payload for exactly this reason.
func (d *decoder) pumpFrames(stdout io.Reader) {
	frame := d.getBuf()
	filled := 0
	for {
		n, err := stdout.Read(frame[filled:])
		if n > 0 {
			filled += n
			if filled >= d.size { // == by construction; >= is belt and braces
				d.emit(frame)
				d.putBuf(frame)
				frame = d.getBuf()
				filled = 0
			}
		}
		if err != nil {
			return
		}
	}
}

// emit writes one complete raw frame (PTS/TC popped in output order) to the inner sink.
func (d *decoder) emit(frame []byte) {
	d.framed.Store(true)
	out := &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: frame}
	d.mu.Lock()
	if len(d.ptsq) > 0 {
		out.PTS, out.TC = d.ptsq[0].pts, d.ptsq[0].tc
		d.ptsq = d.ptsq[1:]
	}
	d.mu.Unlock()
	d.out.tick()
	if d.sink.Write(out) != nil {
		d.restart("sink write failed")
	}
}

// getBuf takes a frame buffer from the ring (allocating when empty).
func (d *decoder) getBuf() []byte {
	select {
	case b := <-d.free:
		return b
	default:
		return make([]byte, d.size)
	}
}

// putBuf returns a frame buffer to the ring; a full ring drops it (GC handles the overflow).
func (d *decoder) putBuf(b []byte) {
	if len(b) != d.size {
		return
	}
	select {
	case d.free <- b:
	default:
	}
}

// restart kills the current child; the next Write respawns (next tier if demoted).
func (d *decoder) restart(reason string) {
	d.mu.Lock()
	stop := d.stop
	d.cmd, d.stdin, d.stop = nil, nil, nil
	if stop != nil {
		d.restarts++
	}
	d.mu.Unlock()
	if stop != nil {
		d.log.Warn(source, "decode child restart", map[string]any{"reason": reason})
		stop()
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// decodeTelemetry logs one line per interval naming WHICH receive path is serving this route and how
// much it actually PUBLISHED. Symmetric with the send side's "route encode telemetry", and
// load-bearing now that native decode is the default: a silent fallback to the ffmpeg frame path
// would make "default on" unfalsifiable on a box with no toolchain and no remote-exec.
//
// publishedFps beside outFps is the whole point, and it is the before/after instrument for this
// increment. On the field rig a 4K route's local republish delivered ~13.5 DISTINCT frames/s while
// the source encoded at 37: the CPU SendImage upload of 33 MB/frame is the capacity ceiling. Both
// paths report PubFrames through the same wrapper chain, so the two numbers are directly comparable
// and the ceiling is visible without a probe tool.
func decodeTelemetry(ctx context.Context, log *logbus.Bus, path string, spec medialink.DecodeSpec, r medialink.PipelineReporter) {
	t := time.NewTicker(routeTelemetryEvery)
	defer t.Stop()
	var prevPub uint64
	prev := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		st := r.PipeStats()
		dPub := st.PubFrames - prevPub
		secs := now.Sub(prev).Seconds()
		prevPub, prev = st.PubFrames, now
		pubFPS := 0.0
		if secs > 0 {
			pubFPS = float64(dPub) / secs
		}
		log.Info(source, "route decode telemetry", map[string]any{
			"decode": path, "engine": st.Encoder, "hwaccel": st.HWAccel,
			"in": fmt.Sprintf("%dx%d", spec.Width, spec.Height),
			// The receive side's content oracle: frames that actually reached the local Spout sender,
			// and the rate they reached it at. "Frames arrived" and "frames were published" are
			// different questions, and the sink's Write cannot tell them apart - it returns nil either
			// way, which is exactly why the volume has to be counted.
			"published": st.PubFrames, "publishedFps": fmt.Sprintf("%.1f", pubFPS),
			// ...and whether the published PICTURE is moving. publishedFps counts frames, so it
			// reads 59 while the same bit-identical frame goes out forever (#58). pubStalledMs is
			// the age of the last CHANGE; pubChanges is how many there have ever been.
			"pubStalledMs": st.PubStalledMs, "pubChanges": st.PubChanges,
			"pubPeakMoved": fmt.Sprintf("%.3f%%", st.PubPeakFrac*100),
			"outFps":       fmt.Sprintf("%.1f", st.OutFPS), "gpuPublish": st.ZeroDecode,
			"dropped": st.Dropped, "lost": st.RealDrops(), "ringDrops": st.InDropped,
			"decErrors": st.DecErrors, "restarts": st.Restarts, "degraded": st.DegradeReason})
	}
}
