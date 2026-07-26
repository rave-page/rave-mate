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
)

// decoder implements medialink.Sink (+PipelineReporter) over the ffmpeg child.
type decoder struct {
	log    *logbus.Bus
	ffmpeg string
	spec   medialink.DecodeSpec
	sink   medialink.Sink
	size   int // bytes per raw output frame

	accels []string // remaining hwaccel candidates; accels[0] is in use ("" = software)

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
	return &decoder{
		log: log, ffmpeg: ffmpeg, spec: spec, sink: sink,
		size:   spec.Width * spec.Height * 4,
		accels: accels,
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
	return medialink.PipelineStats{Encoder: "ffmpeg-decode", HWAccel: accel,
		OutFPS: d.out.value(), Restarts: restarts}
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
	buf := make([]byte, 0, d.size)
	tmp := make([]byte, 64<<10)
	for {
		n, err := stdout.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			for len(buf) >= d.size {
				payload := make([]byte, d.size)
				copy(payload, buf[:d.size])
				buf = append(buf[:0], buf[d.size:]...)
				d.framed.Store(true)
				out := &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: payload}
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
		}
		if err != nil {
			break
		}
	}
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
