package webcam

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/sysexec"
	"rave.page/mate/internal/videoshare"
)

// capture is one device's supervised ffmpeg rawvideo pipe, exposed as a medialink.Source
// (MEDIALINK_DESIGN.md §5). The child is restarted with capped backoff on exit (device hiccup /
// unplug-replug must not kill the route mid-set) - the rtspserve/audiorec supervision pattern.
//
// Pixel format: frames are requested as RGBA (CodecNRGBA), not BGRA - identical ffmpeg cost
// (swscale converts from whatever the camera emits either way) and it feeds the existing Spout
// shim (GL_RGBA) with zero per-frame swizzle.
type capDesc struct {
	Device string
	W, H   int
	FPS    int // 0 = device default
}

type capture struct {
	log    *logbus.Bus
	desc   capDesc
	size   int // bytes per frame
	frames chan capFrame

	cancel context.CancelFunc
	done   chan struct{}

	dropped  atomic.Uint64 // frames replaced while the consumer was behind (newest-wins)
	sent     atomic.Uint64
	poolMiss atomic.Uint64 // frames that had to allocate: the pool was at its live ceiling
	refBugs  atomic.Uint64 // buffer released past zero (a real bug - two writers, one buffer)

	mu       sync.Mutex
	srcUp    bool
	restarts int
	lastErr  string
}

// capStats is a capture's live snapshot.
type capStats struct {
	SrcUp    bool
	Restarts int
	Frames   uint64
	Dropped  uint64
	PoolMiss uint64 // frames allocated because the pixel pool was at its ceiling
	RefBugs  uint64 // release-past-zero events (should always be 0)
	LastErr  string
}

// newCapture validates the descriptor and starts the supervision loop (bound to ctx).
func newCapture(ctx context.Context, log *logbus.Bus, ffmpeg string, desc capDesc) (*capture, error) {
	size, err := frameSize(desc.W, desc.H)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithCancel(ctx)
	c := &capture{
		log: log, desc: desc, size: size,
		frames: make(chan capFrame, 1),
		cancel: cancel, done: make(chan struct{}),
	}
	go c.run(cctx, ffmpeg)
	return c, nil
}

// next yields the next captured frame WITH its buffer reference, io.EOF once the capture is closed.
// The caller holds exactly one reference and must Release it (after taking one per extra consumer).
func (c *capture) next(ctx context.Context) (capFrame, error) {
	select {
	case <-ctx.Done():
		return capFrame{}, ctx.Err()
	case cf, ok := <-c.frames:
		if !ok {
			return capFrame{}, io.EOF
		}
		return cf, nil
	}
}

// Close stops the child + supervision (idempotent via cancel) and returns any frame still pending
// in the hand-off channel to the pool - a buffer abandoned on shutdown would pin the pool's live
// ceiling for the life of the process.
func (c *capture) Close() error {
	c.cancel()
	<-c.done
	for {
		select {
		case cf, ok := <-c.frames:
			if !ok {
				return nil
			}
			cf.release()
		default:
			return nil
		}
	}
}

// drainForTest releases any frame still pending in the hand-off channel (Close's drain, without
// the child-process teardown).
func (c *capture) drainForTest() error {
	for {
		select {
		case cf := <-c.frames:
			cf.release()
		default:
			return nil
		}
	}
}

// stats snapshots the capture counters.
func (c *capture) stats() capStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capStats{SrcUp: c.srcUp, Restarts: c.restarts, PoolMiss: c.poolMiss.Load(), RefBugs: c.refBugs.Load(),
		Frames: c.sent.Load(), Dropped: c.dropped.Load(), LastErr: c.lastErr}
}

func (c *capture) setErr(msg string) {
	c.mu.Lock()
	if msg != "" && c.lastErr != msg {
		c.log.Warn(source, "capture error", map[string]any{"device": c.desc.Device, "error": msg})
	}
	c.lastErr = msg
	c.mu.Unlock()
}

// run supervises the ffmpeg child: spawn → pipe frames → restart with capped backoff on exit.
func (c *capture) run(ctx context.Context, ffmpeg string) {
	defer close(c.done)
	defer close(c.frames)
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := c.runOnce(ctx, ffmpeg)
		c.mu.Lock()
		c.srcUp = false
		c.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.setErr(err.Error())
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // stable run - reset
		}
		c.mu.Lock()
		c.restarts++
		c.mu.Unlock()
		c.log.Warn(source, "capture ffmpeg exited - restarting",
			map[string]any{"device": c.desc.Device, "backoff": backoff.String()})
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// runOnce runs one capture process to completion, emitting frames newest-wins.
func (c *capture) runOnce(ctx context.Context, ffmpeg string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, captureArgs(c.desc)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	tail := newRingWriter(1 << 10)
	cmd.Stderr = tail
	sysexec.Hide(cmd)
	// Kill the whole tree on stop/exit: PATH ffmpeg is often a launcher shim (chocolatey/scoop)
	// whose REAL ffmpeg child otherwise orphans and keeps the camera open (seen live).
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil }
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	// Media class: a LIVE capture, so not the 10% batch bucket (it used to share that cap with
	// gridfix/probe sweeps and got throttled into dropping frames), but still bounded (see JobMedia).
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobMedia) // app death reaps the tree too
	c.mu.Lock()
	c.srcUp = true
	c.mu.Unlock()
	c.setErr("")

	rerr := framePipe{
		size:    c.size,
		alloc:   c.allocFrame,
		recycle: c.recycleFrame,
		emit: func(buf []byte) bool {
			c.deliver(buf)
			return ctx.Err() == nil
		},
	}.run(stdout)
	werr := cmd.Wait()
	if rerr != nil && ctx.Err() == nil {
		return fmt.Errorf("frame pipe: %w", rerr)
	}
	if werr != nil && ctx.Err() == nil {
		return fmt.Errorf("ffmpeg: %v - %s", werr, tail.String())
	}
	return nil
}

// allocFrame takes the next frame buffer from the BOUNDED pixel pool, falling back to a plain
// allocation when the pool is at its live-bytes ceiling.
//
// Policy: fail OPEN. The pool's own contract for a producer is "drop the frame at the ceiling", but
// dropping every frame would kill a live camera preview, and a single leaked reference would pin the
// ceiling forever. Allocating instead degrades to the pre-pool behaviour (counted as PoolMiss)
// rather than wedging the capture - the ceiling still bounds how much is RECYCLED, which is what
// bounds the churn.
func (c *capture) allocFrame() []byte {
	if b, ok := videoshare.TryGetPix(c.size); ok {
		return b
	}
	c.poolMiss.Add(1)
	return make([]byte, c.size)
}

// recycleFrame hands back a buffer that never became a frame (torn tail / read error).
func (c *capture) recycleFrame(buf []byte) {
	if len(buf) == c.size {
		videoshare.PutPix(buf)
	}
}

// capFrame is one captured frame plus the refcount that owns its pixel buffer. The frame's
// Release IS the ref's, so a downstream consumer (medialink's send loop) releases it without
// knowing anything about the pool.
type capFrame struct {
	f   *medialink.Frame
	ref *videoshare.PixRef
}

func (cf capFrame) release() {
	if cf.ref != nil {
		cf.ref.Release()
	}
}

// newFrame wraps a captured buffer in a refcounted frame. One reference belongs to the consumer of
// c.frames (the distributor); it takes one more per network tap before fanning out.
func (c *capture) newFrame(buf []byte) capFrame {
	ref := videoshare.NewPixRef(buf, len(buf) == c.size, func(int32) { c.refBugs.Add(1) })
	return capFrame{ref: ref, f: &medialink.Frame{Kind: medialink.KindVideo,
		Codec: medialink.CodecNRGBA, Payload: ref.Pix(), Release: ref.Release}}
}

// deliver pushes a frame newest-wins: a pending stale frame is replaced, never blocking the pipe.
// The displaced frame is RELEASED - dropping one used to be "forget it", which is only safe while
// every buffer is garbage.
func (c *capture) deliver(buf []byte) {
	cf := c.newFrame(buf)
	for {
		select {
		case c.frames <- cf:
			c.sent.Add(1)
			return
		default:
			select {
			case old := <-c.frames: // drop the stale pending frame, retry
				c.dropped.Add(1)
				old.release()
			default:
			}
		}
	}
}

// releaseFrame drops one reference to a frame's buffer (no-op when it carries none).
func releaseFrame(f *medialink.Frame) {
	if f != nil && f.Release != nil {
		f.Release()
	}
}

// captureArgs builds the dshow → raw RGBA pipe argv.
func captureArgs(d capDesc) []string {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-fflags", "nobuffer",
		"-f", "dshow",
		"-video_size", fmt.Sprintf("%dx%d", d.W, d.H),
	}
	if d.FPS > 0 {
		args = append(args, "-framerate", strconv.Itoa(d.FPS))
	}
	return append(args,
		"-i", "video="+d.Device,
		"-an", "-pix_fmt", "rgba", "-f", "rawvideo", "-")
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
