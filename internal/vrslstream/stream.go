// Package vrslstream is the VRSL DMX-over-video stream publisher: it renders the shared DMX
// universe store into a VRSL-compatible video grid (internal/vrslgrid), ffmpeg-encodes it (LINEAR,
// full-range, no gamma), and pushes RTMP (or WHIP) to a transcode service (VRCDN/Twitch/custom).
// The VRChat world plays the stream back and decodes the grid with RaveVRSLGridReader. See
// .devnotes/VRSL_VIDEO_STREAM.md (mirror of the frozen world-repo contract).
//
// ISOLATION: in-proc, rtspserve-style low-throughput ffmpeg supervisor (NOT a featurehost child).
// Justified per CLAUDE.md's rtspserve carve-out:
//   - Bounded, low throughput: we rasterize a tiny synthetic grid frame from the in-memory DMX
//     store and write it to ffmpeg's stdin. No media INGEST, no decode, no cross-PC frame route.
//     ffmpeg owns the network egress (RTMP/WHIP push) - rave-mate never buffers/relays media bytes.
//   - The feature MUST read the SAME artnet.Store the in-proc dmx.Router owns, so one Art-Net
//     listener serves both. A featurehost child would need a second Art-Net listener (can't co-bind
//     UDP :6454) or a high-rate cross-process copy of the whole universe store every frame - the
//     opposite of isolation. Sharing the store by pointer is simpler AND lower-throughput.
//   - Supervised like rtspserve: capped 1->10s backoff (reset after a stable run), KillTree +
//     AssignToJob reap a wedged ffmpeg, tailWriter captures stderr, and the frame channel is
//     bounded (cap 4, drop-newest) so the producer can never outrun the encoder.
package vrslstream

import (
	"context"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/artnet"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
	"rave.page/mate/internal/vrslgrid"
)

const source = "vrslstream"

// framesBuf bounds the render->encode handoff. Grid frames are tiny + low-rate; 4 in-flight covers
// a brief encoder stall. DROP-NEWEST: when the channel is full the producer skips rendering this
// tick (never blocks the render loop, never accumulates). A dropped frame is stale DMX - the next
// frame supersedes it, and the frameCounter/keepalive still advance for liveness.
const framesBuf = 4

// Streamer is the stream module. cfg is re-read on each (re)start (the standard module pattern).
type Streamer struct {
	log      *logbus.Bus
	store    *artnet.Store // shared with dmx.Router (one Art-Net listener feeds both)
	cfgFn    func() config.StreamFeature
	dmxOnFn  func() bool              // DMX plane enabled? (it then owns the Art-Net listener)
	dmxCfgFn func() config.DMXFeature // Art-Net listen addr when WE own the listener
	// overlayFn provides the per-frame composite overlay painter (vrslgrid.CompositeSpec.Overlay,
	// e.g. the mocap region via mocap.Service.Overlay). Resolved fresh EACH rendered frame so
	// toggling the provider's module applies live without a stream restart; nil provider or
	// provider-returned nil = no overlay. Extended mode only (the overlay's calibration rides
	// the extended meta band's triad) - standard mode forces it off.
	overlayFn func() func(*image.RGBA)

	mu          sync.Mutex
	running     bool
	srcUp       bool
	ownListener bool
	restarts    int
	frames      uint64
	target      string
	lastErr     string
}

// New builds the streamer. store is the shared DMX universe cache (dmx.Router.Store());
// overlayFn may be nil (no overlay extension).
func New(log *logbus.Bus, store *artnet.Store, cfgFn func() config.StreamFeature,
	dmxOnFn func() bool, dmxCfgFn func() config.DMXFeature, overlayFn func() func(*image.RGBA)) *Streamer {
	return &Streamer{log: log, store: store, cfgFn: cfgFn, dmxOnFn: dmxOnFn, dmxCfgFn: dmxCfgFn, overlayFn: overlayFn}
}

// Start launches the render producer + the supervised ffmpeg push, bound to ctx (module Start
// contract: non-blocking). Fails only on missing push config; ffmpeg/source hiccups degrade with a
// logged restart. When the DMX plane is disabled, we own the Art-Net listener (best-effort) so the
// stream still has data.
func (s *Streamer) Start(ctx context.Context) error {
	cfg := s.cfgFn()
	if strings.TrimSpace(cfg.URL) == "" {
		return fmt.Errorf("vrslstream: no push URL configured (Settings -> VRSL Stream)")
	}

	// Frame geometry is fixed for this run (cfg is re-read on restart).
	mono := cfg.ResolvedColorMode() == "mono"
	universes := cfg.ResolvedUniverses()
	probe := vrslgrid.RenderComposite(s.store, vrslgrid.CompositeSpec{
		Universes: universes, Mono: mono, Extended: cfg.ResolvedMode() == "extended",
	})
	w, h := probe.Bounds().Dx(), probe.Bounds().Dy()

	own := false
	if !s.dmxOnFn() {
		own = s.startOwnListener(ctx)
	}
	frames := make(chan *image.RGBA, framesBuf)

	s.mu.Lock()
	s.running, s.ownListener = true, own
	s.srcUp, s.restarts, s.frames, s.lastErr = false, 0, 0, ""
	s.target = redactTarget(cfg)
	s.mu.Unlock()
	s.log.Info(source, "vrsl stream up", map[string]any{
		"target": s.target, "mode": cfg.ResolvedMode(), "color": cfg.ResolvedColorMode(),
		"size": fmt.Sprintf("%dx%d", w, h), "fps": cfg.ResolvedFPS(), "transport": cfg.ResolvedTransport(),
	})

	debuglog.Go(s.log, source, func() { s.runProducer(ctx, cfg, frames) })
	debuglog.Go(s.log, source, func() { s.runFFmpeg(ctx, cfg, w, h, frames) })
	debuglog.Go(s.log, source, func() {
		<-ctx.Done()
		s.mu.Lock()
		s.running, s.srcUp = false, false
		s.mu.Unlock()
	})
	return nil
}

// startOwnListener best-effort binds an Art-Net listener feeding the shared store (used when the DMX
// plane is off). A bind failure (something else already owns the port) is non-fatal - the shared
// store is read-only from our side; whoever bound it feeds the data.
func (s *Streamer) startOwnListener(ctx context.Context) bool {
	addr := s.dmxCfgFn().ResolvedListenAddr()
	lis := artnet.NewListener(s.log, s.store, "rave-mate", "rave-mate VRSL stream bridge")
	debuglog.Go(s.log, source, func() {
		if err := lis.Run(ctx, addr); err != nil && ctx.Err() == nil {
			s.log.Warn(source, "own art-net listener unavailable (DMX plane off)",
				map[string]any{"addr": addr, "error": err.Error()})
			s.mu.Lock()
			s.ownListener = false
			s.mu.Unlock()
		}
	})
	return true
}

// runProducer renders the store into VRSL frames at <=fps, only when a universe changed (dirty flag
// via store generation) or the >=1fps keep-alive is due, mirroring dmx.runGrid. frameCounter
// advances on every EMITTED frame (incl. keepalives) - the world's liveness signal.
func (s *Streamer) runProducer(ctx context.Context, cfg config.StreamFeature, frames chan *image.RGBA) {
	mono := cfg.ResolvedColorMode() == "mono"
	extended := cfg.ResolvedMode() == "extended"
	universes := cfg.ResolvedUniverses()
	tick := time.NewTicker(time.Second / time.Duration(cfg.ResolvedFPS()))
	defer tick.Stop()

	var lastGen uint64
	var lastSend time.Time
	var counter byte
	var warnedOverlay bool
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			// Overlay resolved fresh each frame (live module toggling); standard mode forces
			// it off (its calibration rides the extended meta band's triad).
			ov, suppressed := overlayForFrame(s.overlayFn, extended)
			if suppressed && !warnedOverlay {
				warnedOverlay = true
				s.log.Warn(source, "mocap overlay requires extended mode - ignored (Settings -> VRSL Stream -> Mode)", nil)
			}
			gen := s.store.Generation()
			// An active overlay carries live pose data outside the DMX store - render every
			// tick while it's on (the dirty-flag only tracks universe changes).
			if ov == nil && !shouldRender(gen, lastGen, now, lastSend) {
				continue
			}
			// drop-newest: consumer behind -> skip this tick (bounded chan, no accumulation).
			if len(frames) >= cap(frames) {
				continue
			}
			counter++ // advances per emitted frame; wraps 0..255
			img := vrslgrid.RenderComposite(s.store, vrslgrid.CompositeSpec{
				Universes: universes, Mono: mono, Extended: extended, FrameCounter: counter, Overlay: ov,
			})
			lastGen, lastSend = gen, now
			// Room was checked above and only this goroutine sends -> the send won't block.
			select {
			case frames <- img:
			case <-ctx.Done():
				return
			}
		}
	}
}

// shouldRender is the dirty-flag: render when universe data changed (generation moved) or the
// >=1fps keep-alive is due (mirrors dmx.shouldRender).
func shouldRender(gen, lastGen uint64, now, lastSend time.Time) bool {
	return gen != lastGen || now.Sub(lastSend) >= time.Second
}

// overlayForFrame resolves the per-frame overlay: nil provider or a provider-returned nil =
// no overlay; standard (non-extended) mode forces nil and reports the suppression (the
// overlay contract needs the extended meta band's calibration triad).
func overlayForFrame(provider func() func(*image.RGBA), extended bool) (ov func(*image.RGBA), suppressed bool) {
	if provider == nil {
		return nil, false
	}
	ov = provider()
	if ov == nil {
		return nil, false
	}
	if !extended {
		return nil, true
	}
	return ov, false
}

// runFFmpeg supervises the encode+push subprocess: spawn -> feed frames -> restart with capped
// backoff on exit (a source/network hiccup must not kill the push mid-set).
func (s *Streamer) runFFmpeg(ctx context.Context, cfg config.StreamFeature, w, h int, frames chan *image.RGBA) {
	backoff := time.Second
	for ctx.Err() == nil {
		ffmpeg, ok := mediatools.Resolve("ffmpeg")
		if !ok {
			s.setErr("ffmpeg not found (install it in Settings -> Library & media, or add to PATH)")
			if !sleepCtx(ctx, 10*time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		err := s.runFFmpegOnce(ctx, ffmpeg, cfg, w, h, frames)
		s.mu.Lock()
		s.srcUp = false
		s.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.setErr(err.Error())
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // stable run - reset
		}
		s.mu.Lock()
		s.restarts++
		s.mu.Unlock()
		s.log.Warn(source, "ffmpeg exited - restarting", map[string]any{"backoff": backoff.String()})
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
}

// runFFmpegOnce runs one push process to completion, feeding RGBA frames into its stdin. ffmpeg
// writes the encoded stream straight to the network (RTMP/WHIP) - we never read its stdout.
func (s *Streamer) runFFmpegOnce(ctx context.Context, ffmpeg string, cfg config.StreamFeature, w, h int, frames chan *image.RGBA) error {
	want := w * h * 4
	cmd := exec.CommandContext(ctx, ffmpeg, ffmpegArgs(cfg, w, h)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	tail := &tailWriter{}
	cmd.Stderr = tail
	sysexec.Hide(cmd)
	// Kill the whole tree on stop: a PATH ffmpeg is often a launcher shim whose real child would
	// otherwise orphan and keep pushing (mediapipe precedent).
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil }
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobMedia) // live stream: bounded, not batch-capped
	s.mu.Lock()
	s.srcUp = true
	s.mu.Unlock()
	s.setErr("")

	writeErr := s.feed(ctx, stdin, want, frames)
	_ = stdin.Close()
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil // deliberate stop
	}
	if writeErr != nil {
		return writeErr
	}
	if werr != nil {
		return fmt.Errorf("ffmpeg: %v - %s", werr, tail.String())
	}
	return nil
}

// feed pumps rendered frames into stdin until ctx cancel or a write error (child died).
func (s *Streamer) feed(ctx context.Context, stdin io.Writer, want int, frames chan *image.RGBA) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case img := <-frames:
			if len(img.Pix) != want {
				continue // dims drifted (never, given fixed cfg) - drop defensively
			}
			if _, err := stdin.Write(img.Pix); err != nil {
				return nil // child died - Wait reports why via stderr tail
			}
			s.mu.Lock()
			s.frames++
			s.mu.Unlock()
		}
	}
}

func (s *Streamer) setErr(msg string) {
	s.mu.Lock()
	if msg != "" && s.lastErr != msg {
		s.log.Warn(source, "push error", map[string]any{"error": msg})
	}
	s.lastErr = msg
	s.mu.Unlock()
}

// Status is the live snapshot for the settings card + ctl stream-status.
type Status struct {
	Running     bool
	SourceUp    bool   // ffmpeg currently pushing
	OwnListener bool   // we own the Art-Net listener (DMX plane off)
	Restarts    int    // push restarts this run
	Frames      uint64 // frames fed to ffmpeg
	Target      string // redacted push target
	LastErr     string
}

// Status returns the live snapshot.
func (s *Streamer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Running: s.running, SourceUp: s.srcUp, OwnListener: s.ownListener,
		Restarts: s.restarts, Frames: s.frames, Target: s.target, LastErr: s.lastErr,
	}
}

// StatusText renders the snapshot as the multi-line ctl reply.
func (s *Streamer) StatusText() string {
	st := s.Status()
	if !st.Running {
		return "vrsl stream off (enable it in Settings -> VRSL Stream)"
	}
	out := "vrsl stream running -> " + st.Target + "\n"
	switch {
	case st.LastErr != "":
		out += "status: ERROR " + st.LastErr + "\n"
	case st.SourceUp:
		out += fmt.Sprintf("status: pushing - %d frame(s), %d restart(s)\n", st.Frames, st.Restarts)
	default:
		out += "status: starting encoder…\n"
	}
	if st.OwnListener {
		out += "art-net: own listener (DMX plane off)\n"
	} else {
		out += "art-net: shared (DMX plane feeds the store)\n"
	}
	return out
}

// redactTarget renders the push target without the stream key (never log the key).
func redactTarget(cfg config.StreamFeature) string {
	url := strings.TrimSpace(cfg.URL)
	if cfg.ResolvedTransport() == "whip" {
		return "whip " + url
	}
	if strings.TrimSpace(cfg.StreamKey) != "" {
		return "rtmp " + strings.TrimRight(url, "/") + "/***"
	}
	return "rtmp " + url
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

// tailWriter keeps the last chunk of stderr for error reporting.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 1024 {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-1024:]...)
	}
	t.mu.Unlock()
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}
