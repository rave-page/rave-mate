// Package mpvplayer drives an mpv render window over its JSON IPC. mpv does the (hardware-
// accelerated) decode + display; this package launches it and exposes a small transport
// (play/pause/seek/position) plus jump-to-track, so the app's Fyne UI is the controller while mpv
// handles efficient video - instead of pushing decoded frames through Fyne's canvas.
//
// Why not decode frames into Fyne ourselves: pumping RGBA frames through Fyne's canvas stutters
// (fixed-rate CPU upload, no GPU present path - a proven dead end, kept only as the mediaplayer
// fallback). mpv keeps the full GPU decode→present path. The one downside - mpv rendering in its
// OWN top-level window - is removed by embedding: on Windows, mpv accepts `--wid=<HWND>` and renders
// INTO a foreign window. Options.WID gives mpv the app's child host window (see internal/mpvembed),
// so the video sits inline in the app instead of a separate popout. WID==0 keeps the popout window.
package mpvplayer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/mpvipc"
	"rave.page/mate/internal/sysexec"
)

var endpointSeq int64 // unique IPC endpoint suffix per launch

// Available reports whether an mpv binary can be resolved (PATH or the app-managed bin dir).
func Available() bool {
	_, ok := mediatools.Resolve("mpv")
	return ok
}

// State is a transport snapshot for the controller UI.
type State struct {
	Path    string
	Playing bool
	Paused  bool    // held (still loaded); false once mpv exited
	Cur     float64 // position (s)
	Total   float64 // duration (s)
}

// Player is one mpv instance under IPC control.
type Player struct {
	log *logbus.Bus

	mu      sync.Mutex
	cmd     *exec.Cmd
	cli     *mpvipc.Client
	path    string
	cur     float64
	total   float64
	paused  bool
	closed  bool
	onTick  func(cur, total float64)
	onClose func()
}

// New builds an (unopened) player. log may be nil.
func New(log *logbus.Bus) *Player { return &Player{log: log} }

// OnTick registers a position callback (cur, total seconds), fired as mpv reports progress.
func (p *Player) OnTick(fn func(cur, total float64)) {
	p.mu.Lock()
	p.onTick = fn
	p.mu.Unlock()
}

// OnClose registers a callback fired once when mpv exits (e.g. the user closed its window), so the
// controller UI can dismiss itself.
func (p *Player) OnClose(fn func()) {
	p.mu.Lock()
	p.onClose = fn
	p.mu.Unlock()
}

// Options tune the mpv launch. Zero value = mpv's own popout window with default decode.
type Options struct {
	WID     uintptr  // foreign window handle (Windows HWND) to render INTO via --wid; 0 = own window
	VO      string   // mpv --vo; "" = mpv default (gpu)
	HWDec   string   // mpv --hwdec; "" = "auto" (own window) / "auto-safe" is passed by the caller when embedding
	Profile string   // optional mpv --profile; "" = none
	Extra   []string // extra raw mpv flags (power users)
}

// Open launches mpv on file with default options (its own window) and connects the IPC channel
// (starts playing). title labels the window. Kept for callers that don't embed.
func (p *Player) Open(file, title string) error { return p.OpenWith(file, title, Options{}) }

// OpenWith launches mpv on file with opts and connects the IPC channel (starts playing). When
// opts.WID != 0 mpv renders INTO that window (embedded, no popout) and its own input handling is
// disabled - the Fyne transport is the sole controller.
func (p *Player) OpenWith(file, title string, opts Options) error {
	bin, ok := mediatools.Resolve("mpv")
	if !ok {
		return fmt.Errorf("mpv not found (install it in Settings)")
	}
	endpoint := newEndpoint()
	hwdec := opts.HWDec
	if hwdec == "" {
		hwdec = "auto"
	}
	args := []string{
		"--input-ipc-server=" + endpoint,
		"--force-window=yes", // show a window even for audio-only
		"--keep-open=yes",    // pause at EOF instead of quitting - keep control
		"--idle=yes",         // survive load errors
		"--no-terminal",
		"--hwdec=" + hwdec,
		"--title=" + title,
	}
	if opts.VO != "" {
		args = append(args, "--vo="+opts.VO)
	}
	if opts.Profile != "" {
		args = append(args, "--profile="+opts.Profile)
	}
	if opts.WID != 0 {
		// Embedded: render into the host window; suppress mpv's own input + chrome so the Fyne
		// controls are the only driver. --no-config keeps a user's mpv.conf from injecting
		// embed-incompatible flags (e.g. its own --wid/--vo).
		args = append(args,
			fmt.Sprintf("--wid=%d", opts.WID),
			"--no-config",
			"--no-osc",
			"--osd-level=0",
			"--input-default-bindings=no",
			"--input-vo-keyboard=no",
			"--input-cursor=no",
			"--no-window-dragging",
			"--cursor-autohide=no",
		)
	}
	args = append(args, opts.Extra...)
	args = append(args, file)
	cmd := exec.Command(bin, args...)
	sysexec.Hide(cmd) // no console window
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mpv: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cli, err := mpvipc.Dial(ctx, endpoint)
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("connect mpv: %w", err)
	}

	p.mu.Lock()
	p.cmd, p.cli, p.path, p.paused = cmd, cli, file, false
	p.mu.Unlock()

	cli.OnProperty(p.onProperty)
	cli.OnEvent(p.onEvent)
	_ = cli.ObserveProperty(1, "time-pos")
	_ = cli.ObserveProperty(2, "duration")
	_ = cli.ObserveProperty(3, "pause")
	_ = cli.ObserveProperty(4, "eof-reached")

	go p.waitExit(cmd)
	return nil
}

// Play resumes playback.
func (p *Player) Play() { p.setPause(false) }

// Pause holds playback.
func (p *Player) Pause() { p.setPause(true) }

// TogglePause flips play/pause and returns the resulting (optimistic) paused state. The mpv IPC
// call is fire-and-forget off the caller thread - mpvipc.Command blocks up to 5s waiting for mpv,
// and these run on the Fyne UI thread (transport buttons / jump rail), so a slow mpv (e.g. seeking
// a multi-GB file) would freeze the whole UI. State reconciles via the observed "pause" property.
func (p *Player) TogglePause() bool {
	p.mu.Lock()
	cli, want := p.cli, !p.paused
	p.mu.Unlock()
	if cli != nil {
		go func() { _ = cli.Set("pause", want) }()
	}
	return want
}

// SetVolume sets playback volume (0–100, where 100 = source level). Off-thread - see TogglePause.
func (p *Player) SetVolume(pct int) {
	p.mu.Lock()
	cli := p.cli
	p.mu.Unlock()
	if cli != nil {
		go func() { _ = cli.Set("volume", pct) }()
	}
}

// Seek jumps to sec seconds (absolute). Off-thread - a synchronous mpv seek on a large file blocks
// the IPC (≤5s) and would freeze the UI thread that calls it (jump rail / seek slider).
func (p *Player) Seek(sec float64) {
	if sec < 0 {
		sec = 0
	}
	p.mu.Lock()
	cli := p.cli
	p.mu.Unlock()
	if cli != nil {
		go func() { _, _ = cli.Command("seek", strconv.FormatFloat(sec, 'f', 3, 64), "absolute") }()
	}
}

// Position returns the current position + total duration (s) - matches the ffmpeg player's
// signature so the shared transport/trim UI can drive either engine.
func (p *Player) Position() (cur, total float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur, p.total
}

// State snapshots the transport.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return State{Path: p.path, Playing: !p.paused && !p.closed, Paused: p.paused && !p.closed, Cur: p.cur, Total: p.total}
}

// Close quits mpv and tears down the IPC channel. Idempotent.
func (p *Player) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	cli, cmd := p.cli, p.cmd
	p.cli = nil
	p.mu.Unlock()

	if cli != nil {
		_, _ = cli.Command("quit")
		_ = cli.Close()
	}
	if cmd != nil && cmd.Process != nil {
		// Give quit a beat to land, then ensure the process is gone.
		go func() {
			time.Sleep(300 * time.Millisecond)
			_ = cmd.Process.Kill()
		}()
	}
}

func (p *Player) setPause(v bool) {
	p.mu.Lock()
	cli := p.cli
	p.mu.Unlock()
	if cli != nil {
		go func() { _ = cli.Set("pause", v) }() // off-thread - see TogglePause
	}
}

// onProperty handles observed mpv property changes (time-pos / duration / pause / eof).
func (p *Player) onProperty(name string, data json.RawMessage) {
	switch name {
	case "time-pos":
		if f, ok := asFloat(data); ok {
			p.mu.Lock()
			p.cur = f
			fn, total := p.onTick, p.total
			p.mu.Unlock()
			if fn != nil {
				fn(f, total)
			}
		}
	case "duration":
		if f, ok := asFloat(data); ok {
			p.mu.Lock()
			p.total = f
			p.mu.Unlock()
		}
	case "pause":
		var b bool
		if json.Unmarshal(data, &b) == nil {
			p.mu.Lock()
			p.paused = b
			p.mu.Unlock()
		}
	}
}

func (p *Player) onEvent(string) {}

// waitExit reaps mpv and fires onClose when its window/process goes away.
func (p *Player) waitExit(cmd *exec.Cmd) {
	_ = cmd.Wait()
	p.mu.Lock()
	already := p.closed
	p.closed = true
	fn := p.onClose
	if p.cli != nil {
		_ = p.cli.Close()
		p.cli = nil
	}
	p.mu.Unlock()
	if !already && fn != nil {
		fn()
	}
}

// asFloat decodes a JSON number property value (null/other → false).
func asFloat(data json.RawMessage) (float64, bool) {
	var f float64
	if json.Unmarshal(data, &f) != nil {
		return 0, false
	}
	return f, true
}

// newEndpoint returns a unique IPC endpoint: a named pipe on Windows, a temp unix socket elsewhere.
func newEndpoint() string {
	n := atomic.AddInt64(&endpointSeq, 1)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\ravemate-mpv-%d-%d`, os.Getpid(), n)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("ravemate-mpv-%d-%d.sock", os.Getpid(), n))
}
