package audioengine

// ffmpeg-backed decode: anything beep can't decode natively (aac/m4a/opus/aiff/alac/wma/…) but
// ffmpeg can, played through the SAME native transport (seek bar, position, pause) as FLAC/MP3 -
// not the external modal. Decodes straight to 48kHz stereo f32 (the speaker rate, so no resample).
// Seek restarts ffmpeg with -ss (fast input seek); the child is KillTree'd on Seek/Close (no orphan).
// Runs in the `player` featurehost child, so a decode fault kills only that child.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

const (
	ffRate       = 48000 // decode to the speaker rate directly → no beep.Resample
	ffChannels   = 2
	ffSampleSize = 4                         // f32le
	ffFrameBytes = ffChannels * ffSampleSize // one interleaved L/R frame
)

// ffmpegPlayable: audio extensions beep can't decode but ffmpeg can. Only "playable" when ffmpeg
// actually resolves (else IsPlayable stays false → external Open).
var ffmpegPlayable = map[string]bool{
	".m4a": true, ".aac": true, ".opus": true, ".aiff": true, ".aif": true,
	".wma": true, ".alac": true, ".mka": true, ".caf": true, ".m4b": true,
}

func ffmpegAvailable() bool { _, ok := mediatools.Resolve("ffmpeg"); return ok }

// ffmpegSource decodes an audio file to 48kHz stereo f32 PCM via ffmpeg, presented as a
// beep.StreamSeekCloser. Stream/Seek are serialized by beep's speaker lock; the mutex only guards
// the child lifecycle against Close.
type ffmpegSource struct {
	ffmpeg string
	path   string
	total  int // total frames/channel from ffprobe (0 = unknown)

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	out    *readAhead
	pos    int // frames emitted since the last (re)start's base
	err    error
	closed bool
}

func newFFmpegSource(path string) (beep.StreamSeekCloser, beep.Format, error) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, beep.Format{}, fmt.Errorf("ffmpeg not found (install it from Settings → Transcode)")
	}
	s := &ffmpegSource{ffmpeg: ffmpeg, path: path, total: ffprobeFrames(path)}
	if err := s.start(0); err != nil {
		return nil, beep.Format{}, err
	}
	return s, beep.Format{SampleRate: ffRate, NumChannels: ffChannels, Precision: ffSampleSize}, nil
}

// start (re)spawns ffmpeg decoding from frame `from`. Caller holds s.mu (or is the constructor).
func (s *ffmpegSource) start(from int) error {
	s.stopChild()
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"-nostdin", "-v", "error"}
	if from > 0 {
		args = append(args, "-ss", strconv.FormatFloat(float64(from)/float64(ffRate), 'f', 6, 64))
	}
	args = append(args, "-i", s.path, "-vn",
		"-f", "f32le", "-acodec", "pcm_f32le", "-ac", strconv.Itoa(ffChannels), "-ar", strconv.Itoa(ffRate), "pipe:1")
	cmd := exec.CommandContext(ctx, s.ffmpeg, args...)
	cmd.Cancel = func() error { sysexec.KillTree(cmd.Process); return nil }
	sysexec.Hide(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	// false = kill-on-close job WITHOUT the 10% CPU cap. This is REALTIME playback decode - the
	// background/CPU-capped job (used for waveform/probe decodes) throttles ffmpeg in bursts, drains
	// the pipe, and makes audio choppy, especially under concurrent OBS/VRChat load.
	sysexec.AssignToJob(cmd.Process, false)
	// Background reader fills a bounded ring so the speaker pull NEVER blocks on ffmpeg's bursty pipe
	// writes (Windows anon-pipe buffers are small). Decouples decode jitter from realtime playback.
	s.cmd, s.cancel, s.out, s.pos = cmd, cancel, newReadAhead(stdout), from
	return nil
}

// stopChild kills the current ffmpeg (if any) and reaps it. Caller holds s.mu.
func (s *ffmpegSource) stopChild() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.out != nil {
		s.out.stop() // unblock + exit the background reader before we drop it
	}
	if s.cmd != nil {
		_ = s.cmd.Wait()
	}
	s.cmd, s.cancel, s.out = nil, nil, nil
}

// readAhead drains ffmpeg stdout in a background goroutine into a bounded ring so the realtime
// Stream() pull never blocks on the pipe (Windows anon-pipe buffers are tiny; a CPU-throttled or
// bursty ffmpeg would otherwise underrun → choppy audio). Single consumer: Stream, under s.mu.
type readAhead struct {
	ch   chan []byte
	done chan struct{}
	cur  []byte
	off  int
}

const (
	raChunk  = 32 << 10 // reader fill granularity
	raBufCap = 48       // bounded decode-ahead: 48*32KB = 1.5MB ≈ 4s @ 48k stereo f32
)

func newReadAhead(r io.Reader) *readAhead {
	ra := &readAhead{ch: make(chan []byte, raBufCap), done: make(chan struct{})}
	go func() {
		defer close(ra.ch)
		for {
			buf := make([]byte, raChunk)
			n, err := io.ReadFull(r, buf)
			if n > 0 {
				select {
				case ra.ch <- buf[:n]:
				case <-ra.done:
					return
				}
			}
			if err != nil { // EOF / ErrUnexpectedEOF / pipe closed → stream end
				return
			}
		}
	}()
	return ra
}

// Read fills p from buffered chunks, blocking until data or EOF. Single-consumer (Stream/s.mu).
func (ra *readAhead) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if ra.off >= len(ra.cur) {
			chunk, ok := <-ra.ch
			if !ok {
				if n > 0 {
					return n, nil
				}
				return 0, io.EOF
			}
			ra.cur, ra.off = chunk, 0
		}
		c := copy(p[n:], ra.cur[ra.off:])
		n += c
		ra.off += c
	}
	return n, nil
}

// stop signals the reader goroutine to exit (idempotent-safe: called once per readAhead).
func (ra *readAhead) stop() { close(ra.done) }

func (s *ffmpegSource) Stream(samples [][2]float64) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.out == nil {
		return 0, false
	}
	var buf [ffFrameBytes]byte
	for i := range samples {
		if _, err := io.ReadFull(s.out, buf[:]); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF && s.err == nil {
				s.err = err
			}
			return i, i > 0
		}
		samples[i][0] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[0:4])))
		samples[i][1] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[4:8])))
		s.pos++
	}
	return len(samples), true
}

func (s *ffmpegSource) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

func (s *ffmpegSource) Len() int { return s.total } // immutable after construction

func (s *ffmpegSource) Position() int { s.mu.Lock(); defer s.mu.Unlock(); return s.pos }

// seekNoopFrames: a seek within this many frames of the live position is a no-op. The player's
// playhead-follow re-sets the seek slider ~2x/s, and Fyne's Slider.SetValue fires OnChangeEnded →
// a seek to ~the current position. Restarting ffmpeg for each (~230ms) would starve playback to
// ~0.6x realtime (choppy). Real jumps (track skips, scrubs) are always >> 0.5s away and still seek.
const seekNoopFrames = ffRate / 2 // 0.5s

func (s *ffmpegSource) Seek(p int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("seek on closed decoder")
	}
	if p < 0 {
		p = 0
	}
	if s.total > 0 && p > s.total {
		p = s.total
	}
	if s.out != nil && abs(p-s.pos) < seekNoopFrames {
		return nil // already essentially here - don't churn a restart (kills playhead-follow choppiness)
	}
	return s.start(p)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (s *ffmpegSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.stopChild()
	return nil
}

// ffprobeFrames returns the file's duration in frames (sec*rate), 0 if unknown.
func ffprobeFrames(path string) int {
	probe, ok := mediatools.Resolve("ffprobe")
	if !ok {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || sec <= 0 {
		return 0
	}
	return int(sec * float64(ffRate))
}
