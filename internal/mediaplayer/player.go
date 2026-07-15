package mediaplayer

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/oto/v3"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// maxFPS caps the decode/render rate; the player never asks ffmpeg for more than the source.
const maxFPS = 30.0

// audioBufferLatency is the oto device buffer for this (Fyne A/V trim) player. 100ms favours
// underrun-free playback under concurrent video decode over minimal latency - this isn't the
// cue-audition path (that's the low-latency native engine in the player child).
const audioBufferLatency = 100 * time.Millisecond

// audioPlayerBufFrames caps the oto PLAYER read-ahead buffer (distinct from the device buffer
// above). Unset, oto defaults to ~0.5s/player - which (a) makes the samples master-clock lead the
// DAC by 0.5s (video paced to clock() would run 0.5s ahead of audio) and (b) prime-blocks Play()
// for ~0.5s on Windows (playImpl runs on the caller until the buffer fills), stalling seeks. Match
// the device latency (~100ms) for parity with the retired beep speaker.
const audioPlayerBufFrames = sampleRate / 10 // ~100ms, whole frames

var (
	audioCtxOnce sync.Once
	audioCtx     *oto.Context
	audioCtxErr  error
)

// initAudioContext opens the process oto device once (this player is the only oto user in the main
// process; the native engine's context lives in the player child process).
func initAudioContext() error {
	audioCtxOnce.Do(func() {
		var ready chan struct{}
		audioCtx, ready, audioCtxErr = oto.NewContext(&oto.NewContextOptions{
			SampleRate:   sampleRate,
			ChannelCount: 2,
			Format:       oto.FormatSignedInt16LE,
			BufferSize:   audioBufferLatency,
		})
		if audioCtxErr == nil {
			<-ready
		}
	})
	return audioCtxErr
}

// State is a snapshot of the player for the transport UI.
type State struct {
	Path     string
	Playing  bool
	Cur      float64 // current position (s)
	Total    float64 // duration (s)
	HasVideo bool
	W, H     int // video canvas size (px)
}

// Player streams audio + video from one media file by shelling out to ffmpeg (two procs). Audio
// is the master clock; the video reader paces frames to it (drop/hold). Audio-only → no frames;
// video-only → a wall-clock master. All shared state sits behind mu; each play/seek bumps a
// generation so a stale reader can't clobber the live state.
type Player struct {
	log *logbus.Bus

	mu      sync.Mutex
	path    string
	info    Info
	fps     float64
	vidW    int
	vidH    int
	playing bool
	paused  bool
	closed  bool

	vol         int64              // atomic: playback volume 0–100 (applied via oto Player.SetVolume)
	gen         uint64             // current play/seek generation
	cancel      context.CancelFunc // cancels the current generation's ffmpeg procs
	seekOff     float64            // start offset (s) of the current generation
	samples     *int64             // atomic stereo-frame counter (audio master clock)
	audioActive bool               // audio clock in use (else wall clock)
	wall        *wallClock         // video-only master clock
	aud         *oto.Player        // audio sink for the current generation (nil = none)

	frameMu sync.Mutex
	frame   *image.NRGBA // latest published video frame (nil if audio-only)

	onTick   func(cur, total float64)
	onFrame  func() // fired (from the pacer) right after a new video frame is published
	tickStop chan struct{}
	wg       sync.WaitGroup // current generation goroutines (ffmpeg waiters + video loop)
}

// New builds a player. log may be nil.
func New(log *logbus.Bus) *Player {
	p := &Player{log: log, tickStop: make(chan struct{}), vol: 100}
	go p.tickLoop()
	return p
}

// SetVolume sets playback volume (0–100), applied live to the oto player + remembered for the next
// generation.
func (p *Player) SetVolume(pct int) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	atomic.StoreInt64(&p.vol, int64(pct))
	p.mu.Lock()
	if p.aud != nil {
		p.aud.SetVolume(float64(pct) / 100)
	}
	p.mu.Unlock()
}

// Open probes file and prepares playback at the given target canvas size (does not start playing).
func (p *Player) Open(file string, vidW, vidH int) error {
	if _, ok := mediatools.Resolve("ffmpeg"); !ok {
		return fmt.Errorf("ffmpeg not found (install FFmpeg in Settings)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info, err := Probe(ctx, file)
	if err != nil {
		return err
	}
	fps := info.FPS
	if fps <= 0 || fps > maxFPS {
		fps = maxFPS
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopCurrentLocked()
	p.path, p.info, p.fps, p.vidW, p.vidH = file, info, fps, vidW, vidH
	p.seekOff, p.paused, p.playing = 0, false, false
	p.frameMu.Lock()
	p.frame = nil
	p.frameMu.Unlock()
	return nil
}

// Play starts playback (from the current position) or resumes if paused.
func (p *Player) Play() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == "" || p.closed {
		return
	}
	if p.cancel == nil {
		p.paused = false
		if err := p.startLocked(p.seekOff); err != nil && p.log != nil {
			p.log.Error("player", "start failed", map[string]any{"err": err.Error()})
		}
		return
	}
	p.setPausedLocked(false)
}

// Pause holds playback (audio + video) in place.
func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel == nil {
		return
	}
	p.setPausedLocked(true)
}

// TogglePause flips play/pause (starting playback if not yet running); returns resulting paused.
func (p *Player) TogglePause() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == "" || p.closed {
		return false
	}
	if p.cancel == nil {
		p.paused = false
		if err := p.startLocked(p.seekOff); err != nil && p.log != nil {
			p.log.Error("player", "start failed", map[string]any{"err": err.Error()})
		}
		return false
	}
	p.setPausedLocked(!p.paused)
	return p.paused
}

// Seek jumps to sec seconds, relaunching ffmpeg at that offset; preserves play/pause state.
func (p *Player) Seek(sec float64) {
	if sec < 0 {
		sec = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.path == "" || p.closed {
		return
	}
	if p.info.Duration > 0 && sec > p.info.Duration {
		sec = p.info.Duration
	}
	wasRunning := p.cancel != nil
	p.stopCurrentLocked()
	p.seekOff = sec
	if wasRunning {
		if err := p.startLocked(sec); err != nil && p.log != nil {
			p.log.Error("player", "seek restart failed", map[string]any{"err": err.Error()})
		}
	}
}

// State snapshots current playback.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return State{
		Path:     p.path,
		Playing:  p.playing,
		Cur:      p.positionLocked(),
		Total:    p.info.Duration,
		HasVideo: p.info.HasVideo && p.vidW > 0 && p.vidH > 0,
		W:        p.vidW,
		H:        p.vidH,
	}
}

// Position returns the current position + total duration (s). Matches the mpv player's signature so
// the shared transport/trim UI can drive either engine.
func (p *Player) Position() (cur, total float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.positionLocked(), p.info.Duration
}

// Frame returns the latest decoded video frame (nil if audio-only). Cheap - returns the cached
// buffer; the caller should copy/refresh promptly (the buffer is reused for a later frame).
func (p *Player) Frame() *image.NRGBA {
	p.frameMu.Lock()
	defer p.frameMu.Unlock()
	return p.frame
}

// OnTick registers a ~200ms position callback (cur, total seconds).
func (p *Player) OnTick(fn func(cur, total float64)) {
	p.mu.Lock()
	p.onTick = fn
	p.mu.Unlock()
}

// OnFrame registers a callback fired once per displayed video frame (from the pacer). Lets the UI
// repaint exactly when a new frame is ready - phase-locked, no judder from a fixed-rate sampler.
func (p *Player) OnFrame(fn func()) {
	p.mu.Lock()
	p.onFrame = fn
	p.mu.Unlock()
}

// Close stops everything, kills ffmpeg, and stops the ticker. Idempotent.
func (p *Player) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.stopCurrentLocked()
	p.path, p.playing = "", false
	ts := p.tickStop
	p.mu.Unlock()

	close(ts)
	p.wg.Wait() // ffmpeg procs reaped + video loop exited → no leaks
}

// --- internals (mu held unless noted) ---

// startLocked launches the ffmpeg pair for the current file at offset and the reader goroutines.
func (p *Player) startLocked(offset float64) error {
	bin, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return fmt.Errorf("ffmpeg not found")
	}
	p.gen++
	gen := p.gen
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.seekOff = offset
	p.samples = nil
	p.audioActive = false
	p.wall = nil

	hasVideo := p.info.HasVideo && p.vidW > 0 && p.vidH > 0
	if hasVideo {
		vcmd := exec.CommandContext(ctx, bin,
			"-hide_banner", "-loglevel", "error",
			"-ss", ftoa(offset), "-i", p.path,
			"-an", "-f", "rawvideo", "-pix_fmt", "rgba",
			"-s", fmt.Sprintf("%dx%d", p.vidW, p.vidH),
			"-r", ftoa(p.fps), "pipe:1")
		sysexec.Hide(vcmd)
		// NOT LowPriority: this is a real-time playback decoder. Deprioritizing it starves decode
		// under load and stutters playback (background transcode is the only place LowPriority fits).
		vout, err := vcmd.StdoutPipe()
		if err != nil {
			cancel()
			p.cancel = nil
			return fmt.Errorf("video pipe: %w", err)
		}
		if err := vcmd.Start(); err != nil {
			cancel()
			p.cancel = nil
			return fmt.Errorf("video start: %w", err)
		}
		p.wg.Add(2)
		go p.videoLoop(ctx, gen, vout, p.vidW, p.vidH, p.fps, offset)
		go func() { defer p.wg.Done(); _ = vcmd.Wait() }()
	}

	if p.info.HasAudio {
		if err := initAudioContext(); err != nil {
			if p.log != nil {
				p.log.Warn("player", "audio init failed - video-only", map[string]any{"err": err.Error()})
			}
		} else {
			// audio-only: add the gapless encoder priming back so a SEEK lands on the from=0 origin
			// (the waveform/grid) - else the cue plays ~priming early. With video present, leave it
			// uncompensated so audio + video stay in lockstep (both target file-PTS offset).
			aSeek := offset
			if !hasVideo {
				aSeek += mediatools.CodecLeadSkipMs(p.info.AudioCodec) / 1000
			}
			acmd := exec.CommandContext(ctx, bin,
				"-hide_banner", "-loglevel", "error",
				"-ss", ftoa(aSeek), "-i", p.path,
				"-vn", "-f", "s16le", "-ar", fmt.Sprintf("%d", sampleRate), "-ac", "2", "pipe:1")
			sysexec.Hide(acmd)
			// NOT LowPriority: audio is the master clock - a starved audio decoder underruns, which
			// freezes the clock and stutters BOTH audio and video in lockstep.
			aout, err := acmd.StdoutPipe()
			if err != nil {
				cancel()
				p.cancel = nil
				return fmt.Errorf("audio pipe: %w", err)
			}
			if err := acmd.Start(); err != nil {
				cancel()
				p.cancel = nil
				return fmt.Errorf("audio start: %w", err)
			}
			p.samples = new(int64)
			p.audioActive = true
			// bufio read-ahead smooths oto's pipe reads (avoids audio underrun, which would stall the
			// master clock and stutter the video paced to it).
			aBuf := bufio.NewReaderSize(aout, 256<<10)
			player := audioCtx.NewPlayer(&pcmReader{r: aBuf, samples: p.samples})
			player.SetBufferSize(audioPlayerBufFrames * bytesPerFrame) // ~100ms, not oto's 0.5s default
			player.SetVolume(float64(atomic.LoadInt64(&p.vol)) / 100)
			p.aud = player
			if !p.paused {
				player.Play()
			}
			p.wg.Add(1)
			go func() { defer p.wg.Done(); _ = acmd.Wait() }()
		}
	}

	if !p.audioActive {
		// No audio (or audio init failed): drive video by wall clock.
		p.wall = newWallClock(offset)
		if !p.paused {
			p.wall.resume()
		}
	}
	p.playing = !p.paused
	return nil
}

// stopCurrentLocked cancels the current generation (kills ffmpeg) and detaches audio. Goroutines
// exit on their own once the procs die; Close waits on wg. Safe when nothing is running.
func (p *Player) stopCurrentLocked() {
	if p.aud != nil {
		p.aud.Pause()     // stop pulling before killing ffmpeg (no read on a dying pipe)
		_ = p.aud.Close() // releases the oto player (buffer freed); the context stays open
		p.aud = nil
	}
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.samples = nil
	p.audioActive = false
	p.wall = nil
	p.playing = false
}

// setPausedLocked applies pause/resume to both audio (Ctrl) and video pacing (wall clock).
func (p *Player) setPausedLocked(pause bool) {
	p.paused = pause
	if p.aud != nil {
		if pause {
			p.aud.Pause()
		} else {
			p.aud.Play()
		}
	}
	if p.wall != nil {
		if pause {
			p.wall.pause()
		} else {
			p.wall.resume()
		}
	}
	p.playing = !pause && p.cancel != nil
}

// positionLocked computes the current AUDIBLE position from the active master clock. samples counts
// frames as oto's read-ahead pulls them from pcmReader, which leads the DAC by the player's unplayed
// buffer (capped at ~100ms via SetBufferSize) - subtract it so the clock (and the video paced to it)
// tracks what's actually heard, not what's buffered. Matches internal/audio.Position (audible = pos
// - buffered).
func (p *Player) positionLocked() float64 {
	if p.audioActive && p.samples != nil {
		played := atomic.LoadInt64(p.samples)
		if p.aud != nil {
			played -= int64(p.aud.BufferedSize() / bytesPerFrame) // frames read-ahead but not yet played
		}
		if played < 0 {
			played = 0
		}
		return p.seekOff + float64(played)/float64(sampleRate)
	}
	if p.wall != nil {
		return p.wall.pos()
	}
	return p.seekOff
}

// clock returns the current master position (mu-safe), used by the video pacer.
func (p *Player) clock() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.positionLocked()
}

// vframe is one decoded video frame queued for display (its bitmap + presentation time).
type vframe struct {
	buf *image.NRGBA
	pts float64
}

// videoBufferFrames sizes the read-ahead queue from a memory budget - frames are full RGBA
// bitmaps, so a high-res canvas gets fewer frames, a small one more. ffmpeg decodes ahead to fill
// this queue, so decode jitter on high-bitrate sources is absorbed instead of stuttering.
func videoBufferFrames(w, h int) int {
	const budget = 96 << 20 // ~96 MB read-ahead
	fb := w * h * 4
	if fb <= 0 {
		return 8
	}
	n := budget / fb
	if n < 8 {
		n = 8
	}
	if n > 48 {
		n = 48
	}
	return n
}

// videoLoop decodes RGBA frames ahead into a bounded buffer pool (reader goroutine) and paces them
// to the master clock for display (this goroutine: drop late / hold early). Decoupling decode from
// display lets ffmpeg run ahead during easy stretches and bank frames for high-bitrate spikes, so
// playback stays smooth. Pool buffers are recycled (no per-frame alloc → no GC stutter).
func (p *Player) videoLoop(ctx context.Context, gen uint64, r io.Reader, w, h int, fps, offset float64) {
	defer p.wg.Done()
	frameDur := 1.0 / fps
	nbuf := videoBufferFrames(w, h)

	free := make(chan *image.NRGBA, nbuf)
	for i := 0; i < nbuf; i++ {
		free <- image.NewNRGBA(image.Rect(0, 0, w, h))
	}
	ready := make(chan vframe, nbuf)

	// Reader: pull frames as fast as buffers free up. Blocking on <-free is the back-pressure that
	// bounds read-ahead (and makes ffmpeg block on its pipe while paused/holding).
	var rwg sync.WaitGroup
	rwg.Add(1)
	go func() {
		defer rwg.Done()
		defer close(ready)
		idx := 0
		for {
			var buf *image.NRGBA
			select {
			case <-ctx.Done():
				return
			case buf = <-free:
			}
			if _, err := io.ReadFull(r, buf.Pix); err != nil {
				return // EOF or killed proc
			}
			pts := offset + float64(idx)/fps
			idx++
			select {
			case <-ctx.Done():
				return
			case ready <- vframe{buf: buf, pts: pts}:
			}
		}
	}()

	recycle := func(b *image.NRGBA) {
		if b == nil {
			return
		}
		select {
		case free <- b:
		default: // pool full (shouldn't happen) - let GC reclaim
		}
	}
	done := func() { rwg.Wait() }

	for {
		var vf vframe
		var ok bool
		select {
		case <-ctx.Done():
			done()
			return
		case vf, ok = <-ready:
			if !ok {
				done()
				return
			}
		}

		now := p.clock()
		if vf.pts < now-frameDur {
			recycle(vf.buf) // stale: drop without publishing
			continue
		}
		// Future frame: wait until the clock catches up (re-checking cancel/pause in small slices).
		for vf.pts > now {
			if ctx.Err() != nil {
				recycle(vf.buf)
				done()
				return
			}
			sleep := vf.pts - now
			if sleep > 0.1 {
				sleep = 0.1
			}
			time.Sleep(time.Duration(sleep * float64(time.Second)))
			now = p.clock()
		}

		p.mu.Lock()
		live := gen == p.gen
		cb := p.onFrame
		p.mu.Unlock()
		if !live {
			recycle(vf.buf)
			done()
			return
		}
		p.frameMu.Lock()
		prev := p.frame
		p.frame = vf.buf
		p.frameMu.Unlock()
		recycle(prev) // the previously displayed bitmap is now free (Fyne already uploaded it)
		if cb != nil {
			cb() // phase-locked repaint: exactly one UI refresh per displayed frame
		}
	}
}

// tickLoop fires onTick ~every 200ms with the current position until Close.
func (p *Player) tickLoop() {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-p.tickStop:
			return
		case <-t.C:
			p.mu.Lock()
			fn := p.onTick
			cur := p.positionLocked()
			total := p.info.Duration
			active := p.path != ""
			p.mu.Unlock()
			if fn != nil && active {
				fn(cur, total)
			}
		}
	}
}
