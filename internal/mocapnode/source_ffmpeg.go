package mocapnode

// source_ffmpeg.go - ffmpeg-backed capture sources: desktop duplication (ddagrab; gdigrab
// fallback) and DirectShow devices (the Spout path: VRChat Stream Camera -> Spout2 -> OBS Spout
// source -> OBS Virtual Camera -> dshow "OBS Virtual Camera"). Both pipe -f rawvideo
// -pix_fmt bgra to stdout - no codec, contract bytes arrive as captured. Discovery + supervision
// mirror internal/vrslstream: mediatools.Resolve, hidden window, KillTree on cancel (a PATH
// ffmpeg is often a launcher shim), AssignToJob so app death reaps the tree, stderr tail in the
// error, capped 1->10s restart backoff (reset after a stable run). Arg builders are pure and
// unit-tested; tests never spawn ffmpeg.

import (
	"context"
	"fmt"
	"image"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// FFmpegDesktopSource captures the desktop. Default grabber "ddagrab" (Desktop Duplication via
// the lavfi source filter, GPU path):
//
//	ffmpeg -f lavfi -i ddagrab=output_idx=M:framerate=F[:video_size=WxH:offset_x=X:offset_y=Y],hwdownload,format=bgra
//	       -f rawvideo -pix_fmt bgra -
//
// Grabber "gdigrab" is the documented fallback (GDI, CPU; works without D3D11 but slower and
// ignores Monitor - it captures the virtual desktop):
//
//	ffmpeg -f gdigrab -framerate F [-offset_x X -offset_y Y -video_size WxH] -i desktop
//	       -f rawvideo -pix_fmt bgra -
type FFmpegDesktopSource struct {
	Monitor int                              // ddagrab output_idx (0 = primary); gdigrab ignores it
	Crop    image.Rectangle                  // capture sub-rect in desktop coords; empty = full frame (W/H then required)
	W, H    int                              // frame geometry when Crop is empty (a raw pipe needs fixed geometry)
	FPS     int                              // capture rate; <=0 = 30
	Grabber string                           // "ddagrab" (default) | "gdigrab"
	FFmpeg  string                           // binary override; "" = mediatools.Resolve("ffmpeg")
	Logf    func(format string, args ...any) // optional supervision log
}

// Frames implements Source via the shared supervised runner.
func (s *FFmpegDesktopSource) Frames(ctx context.Context, emit func(Frame)) error {
	w, h, err := s.frameSize()
	if err != nil {
		return err
	}
	args, err := s.args()
	if err != nil {
		return err
	}
	return runFFmpeg(ctx, s.FFmpeg, args, w, h, s.Logf, emit)
}

// frameSize is the raw-pipe geometry: the crop rect when set, else the configured W/H.
func (s *FFmpegDesktopSource) frameSize() (int, int, error) {
	if !s.Crop.Empty() {
		return s.Crop.Dx(), s.Crop.Dy(), nil
	}
	if s.W <= 0 || s.H <= 0 {
		return 0, 0, fmt.Errorf("mocapnode: desktop source needs W/H or a crop rect (raw pipe geometry)")
	}
	return s.W, s.H, nil
}

// args builds the pure ffmpeg argv (unit-tested; no process here).
func (s *FFmpegDesktopSource) args() ([]string, error) {
	fps := s.FPS
	if fps <= 0 {
		fps = 30
	}
	switch s.Grabber {
	case "", "ddagrab":
		filter := fmt.Sprintf("ddagrab=output_idx=%d:framerate=%d", s.Monitor, fps)
		if !s.Crop.Empty() {
			filter += fmt.Sprintf(":video_size=%dx%d:offset_x=%d:offset_y=%d",
				s.Crop.Dx(), s.Crop.Dy(), s.Crop.Min.X, s.Crop.Min.Y)
		}
		filter += ",hwdownload,format=bgra"
		return append(commonInArgs(), "-f", "lavfi", "-i", filter,
			"-f", "rawvideo", "-pix_fmt", "bgra", "-"), nil
	case "gdigrab":
		args := append(commonInArgs(), "-f", "gdigrab", "-framerate", strconv.Itoa(fps))
		if !s.Crop.Empty() {
			args = append(args,
				"-offset_x", strconv.Itoa(s.Crop.Min.X), "-offset_y", strconv.Itoa(s.Crop.Min.Y),
				"-video_size", fmt.Sprintf("%dx%d", s.Crop.Dx(), s.Crop.Dy()))
		} else {
			args = append(args, "-video_size", fmt.Sprintf("%dx%d", s.W, s.H))
		}
		return append(args, "-i", "desktop", "-f", "rawvideo", "-pix_fmt", "bgra", "-"), nil
	default:
		return nil, fmt.Errorf("mocapnode: unknown grabber %q (want ddagrab|gdigrab)", s.Grabber)
	}
}

// FFmpegDShowSource captures a DirectShow device by name - the Spout path's last hop
// ("OBS Virtual Camera"):
//
//	ffmpeg -f dshow [-framerate F] -video_size WxH -i video=<device> -f rawvideo -pix_fmt bgra -
type FFmpegDShowSource struct {
	Device string // dshow device name, e.g. "OBS Virtual Camera"
	W, H   int    // negotiated frame size; <=0 = 1920x1080 (the OBS canvas default)
	FPS    int    // 0 = device default; passed as -framerate when set
	FFmpeg string // binary override; "" = mediatools.Resolve("ffmpeg")
	Logf   func(format string, args ...any)
}

// Frames implements Source via the shared supervised runner.
func (s *FFmpegDShowSource) Frames(ctx context.Context, emit func(Frame)) error {
	if strings.TrimSpace(s.Device) == "" {
		return fmt.Errorf("mocapnode: dshow source needs a device name")
	}
	w, h := s.W, s.H
	if w <= 0 || h <= 0 {
		w, h = 1920, 1080
	}
	return runFFmpeg(ctx, s.FFmpeg, s.args(w, h), w, h, s.Logf, emit)
}

// args builds the pure ffmpeg argv (unit-tested; no process here).
func (s *FFmpegDShowSource) args(w, h int) []string {
	args := append(commonInArgs(), "-f", "dshow")
	if s.FPS > 0 {
		args = append(args, "-framerate", strconv.Itoa(s.FPS))
	}
	return append(args,
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-i", "video="+s.Device,
		"-f", "rawvideo", "-pix_fmt", "bgra", "-")
}

func commonInArgs() []string {
	return []string{"-hide_banner", "-loglevel", "warning", "-fflags", "nobuffer"}
}

// runFFmpeg supervises the capture subprocess: resolve binary -> spawn -> chunk stdout into
// BGRA frames -> emit; restart with capped backoff on exit. Returns nil on ctx cancel only.
func runFFmpeg(ctx context.Context, bin string, args []string, w, h int,
	logf func(string, ...any), emit func(Frame)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	backoff := time.Second
	for ctx.Err() == nil {
		ffmpeg := bin
		if ffmpeg == "" {
			p, ok := mediatools.Resolve("ffmpeg")
			if !ok {
				logf("mocapnode: ffmpeg not found (install via Settings -> Library & media, or add to PATH)")
				if !sleepCtx(ctx, 10*time.Second) {
					return nil
				}
				continue
			}
			ffmpeg = p
		}
		started := time.Now()
		if err := runFFmpegOnce(ctx, ffmpeg, args, w, h, emit); err != nil && ctx.Err() == nil {
			logf("mocapnode: ffmpeg capture exited: %v - restarting in %s", err, backoff)
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > 30*time.Second {
			backoff = time.Second // stable run - reset
		}
		if !sleepCtx(ctx, backoff) {
			return nil
		}
		if backoff < 10*time.Second {
			backoff *= 2
		}
	}
	return nil
}

// runFFmpegOnce runs one capture process to completion, chunking its stdout into frames. The
// frame buffer is reused - emit must not retain it.
func runFFmpegOnce(ctx context.Context, ffmpeg string, args []string, w, h int, emit func(Frame)) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	tail := &tailWriter{}
	cmd.Stderr = tail
	sysexec.Hide(cmd)
	// Kill the whole tree on stop: a PATH ffmpeg is often a launcher shim whose real child
	// would otherwise orphan and keep capturing (vrslstream precedent).
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil }
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	sysexec.AssignToJobClass(cmd.Process, sysexec.JobMedia) // live capture: bounded, not batch-capped

	buf := make([]byte, w*h*4)
	readErr := error(nil)
	for {
		if _, err := io.ReadFull(stdout, buf); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				readErr = err
			}
			break
		}
		emit(Frame{Pix: buf, W: w, H: h, Stride: w * 4, Fmt: FmtBGRA})
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil // deliberate stop
	}
	if werr != nil {
		return fmt.Errorf("ffmpeg: %v - %s", werr, tail.String())
	}
	return readErr
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
