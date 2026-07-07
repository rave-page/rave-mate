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
	frames chan *medialink.Frame

	cancel context.CancelFunc
	done   chan struct{}

	dropped atomic.Uint64 // frames replaced while the consumer was behind (newest-wins)
	sent    atomic.Uint64

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
		frames: make(chan *medialink.Frame, 1),
		cancel: cancel, done: make(chan struct{}),
	}
	go c.run(cctx, ffmpeg)
	return c, nil
}

// Next implements medialink.Source: the next captured frame, io.EOF once the capture is closed.
func (c *capture) Next(ctx context.Context) (*medialink.Frame, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f, ok := <-c.frames:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	}
}

// Close implements medialink.Source: stops the child + supervision (idempotent via cancel).
func (c *capture) Close() error {
	c.cancel()
	<-c.done
	return nil
}

// stats snapshots the capture counters.
func (c *capture) stats() capStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capStats{SrcUp: c.srcUp, Restarts: c.restarts,
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
	sysexec.AssignToJob(cmd.Process, true) // app death reaps the tree too
	c.mu.Lock()
	c.srcUp = true
	c.mu.Unlock()
	c.setErr("")

	rerr := readFrames(stdout, c.size, func(buf []byte) bool {
		c.deliver(&medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: buf})
		return ctx.Err() == nil
	})
	werr := cmd.Wait()
	if rerr != nil && ctx.Err() == nil {
		return fmt.Errorf("frame pipe: %w", rerr)
	}
	if werr != nil && ctx.Err() == nil {
		return fmt.Errorf("ffmpeg: %v - %s", werr, tail.String())
	}
	return nil
}

// deliver pushes a frame newest-wins: a pending stale frame is replaced, never blocking the pipe.
func (c *capture) deliver(f *medialink.Frame) {
	for {
		select {
		case c.frames <- f:
			c.sent.Add(1)
			return
		default:
			select {
			case <-c.frames: // drop the stale pending frame, retry
				c.dropped.Add(1)
			default:
			}
		}
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
