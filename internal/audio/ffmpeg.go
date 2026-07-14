package audio

// ffmpeg-backed decode: formats the native decoders can't handle (aac/m4a/opus/alac/wma/…) but
// ffmpeg can, presented as an audio.Decoder so they share the SAME low-latency transport (RAM
// preload, sample-accurate seek, hold-Space audition) as native FLAC/MP3/WAV/OGG - never the
// external modal. ffmpeg decodes straight to the device format (48kHz stereo f32le) so there's no
// resample and ReadFrames hands the pipe bytes through untouched. A normal-length track preloads to
// RAM (LoadDecoder) → every audition seek is an instant index move, no ffmpeg respawn. Only huge
// sets stream; a stream seek restarts ffmpeg at -ss. Runs in the `player` featurehost child, so a
// decode fault kills only that child. No beep.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/sysexec"
)

// ffmpeg decodes to the device format directly (deviceRate/deviceChannels/f32le) → no resample.
const (
	ffSampleSize = deviceBytes                   // f32le
	ffFrameBytes = deviceChannels * ffSampleSize // one interleaved L/R frame
)

// ffmpegPlayable: audio extensions the native decoders can't handle but ffmpeg can. Only truly
// "playable" when ffmpeg resolves (else the caller keeps the external Open path).
var ffmpegPlayable = map[string]bool{
	".m4a": true, ".aac": true, ".opus": true, ".aiff": true, ".aif": true,
	".wma": true, ".alac": true, ".mka": true, ".caf": true, ".m4b": true,
}

func ffmpegAvailable() bool { _, ok := mediatools.Resolve("ffmpeg"); return ok }

// FFmpegPlayable reports whether the ffmpeg fallback can serve path's extension (ffmpeg resolvable).
func FFmpegPlayable(path string) bool {
	return ffmpegPlayable[strings.ToLower(filepath.Ext(path))] && ffmpegAvailable()
}

// Playable reports whether ANY engine path (native decoder or ffmpeg fallback) can decode path -
// else the caller uses the external Open button.
func Playable(path string) bool { return Openable(path) || FFmpegPlayable(path) }

// ffmpegDecoder decodes an audio file to device-format PCM via an ffmpeg subprocess, as an
// audio.Decoder. Not safe for concurrent use; the engine (or newRAMSource's decode loop) serializes
// access. The mutex only guards the child lifecycle against Close.
type ffmpegDecoder struct {
	ffmpeg      string
	path        string
	total       int     // total frames from ffprobe (0 = unknown)
	leadSkipSec float64 // gapless encoder priming (s) added back on SEEK; 0 for lossless

	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	out    *readAhead
	pos    int // frames emitted since the last (re)start's base
	err    error
	closed bool
}

// OpenFFmpeg opens path via ffmpeg as an audio.Decoder (from frame 0; the engine seeks after load).
func OpenFFmpeg(path string) (Decoder, error) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, fmt.Errorf("ffmpeg not found (install it from Settings → Transcode)")
	}
	frames, codec := ffprobeInfo(path)
	d := &ffmpegDecoder{ffmpeg: ffmpeg, path: path, total: frames}
	d.leadSkipSec = mediatools.CodecLeadSkipMs(codec) / 1000
	if err := d.start(0); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *ffmpegDecoder) Format() Format {
	return Format{SampleRate: deviceRate, Channels: deviceChannels}
}

// TotalFrames returns the ffprobe frame count, or -1 when unknown (=> the engine streams).
func (d *ffmpegDecoder) TotalFrames() int64 {
	if d.total > 0 {
		return int64(d.total)
	}
	return -1
}

// start (re)spawns ffmpeg decoding from frame `from`. Caller holds d.mu (or is the constructor).
func (d *ffmpegDecoder) start(from int) error {
	d.stopChild()
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"-nostdin", "-v", "error"}
	if from > 0 {
		// Input seek targets file-PTS, but the from=0 decode dropped the gapless encoder priming;
		// add it back so an auditioned SEEK lands on the SAME origin the peaks/waveform use (else the
		// cue plays ~priming early). leadSkipSec=0 for lossless → no shift. pos stays `from` (displayed
		// frame): the first emitted sample now ≈ displayed time from/deviceRate.
		seekSec := float64(from)/float64(deviceRate) + d.leadSkipSec
		args = append(args, "-ss", strconv.FormatFloat(seekSec, 'f', 6, 64))
	}
	args = append(args, "-i", d.path, "-vn",
		"-f", "f32le", "-acodec", "pcm_f32le", "-ac", strconv.Itoa(deviceChannels), "-ar", strconv.Itoa(deviceRate), "pipe:1")
	cmd := exec.CommandContext(ctx, d.ffmpeg, args...)
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
	// CPU-capped job (waveform/probe decodes) throttles ffmpeg in bursts, drains the pipe, and makes
	// audio choppy under concurrent OBS/VRChat load.
	sysexec.AssignToJob(cmd.Process, false)
	// Background reader fills a bounded ring so the device pull NEVER blocks on ffmpeg's bursty pipe
	// writes (Windows anon-pipe buffers are small). Decouples decode jitter from realtime playback.
	d.cmd, d.cancel, d.out, d.pos = cmd, cancel, newReadAhead(stdout), from
	return nil
}

// stopChild kills the current ffmpeg (if any) and reaps it. Caller holds d.mu.
func (d *ffmpegDecoder) stopChild() {
	if d.cancel != nil {
		d.cancel()
	}
	if d.out != nil {
		d.out.stop() // unblock + exit the background reader before we drop it
	}
	if d.cmd != nil {
		_ = d.cmd.Wait()
	}
	d.cmd, d.cancel, d.out = nil, nil, nil
}

// ReadFrames fills dst (interleaved device stereo f32) from the current position. Returns (n, nil)
// for a full or short read and (0, io.EOF) at stream end (interface contract: a short read is not EOF).
func (d *ffmpegDecoder) ReadFrames(dst []float32) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.out == nil {
		return 0, io.EOF
	}
	frames := len(dst) / deviceChannels
	var buf [ffFrameBytes]byte
	for i := 0; i < frames; i++ {
		if _, err := io.ReadFull(d.out, buf[:]); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF && d.err == nil {
				d.err = err
			}
			if i > 0 {
				return i, nil // short read - more may follow only via seek; report frames, not EOF
			}
			return 0, io.EOF
		}
		dst[i*deviceChannels] = math.Float32frombits(binary.LittleEndian.Uint32(buf[0:4]))
		dst[i*deviceChannels+1] = math.Float32frombits(binary.LittleEndian.Uint32(buf[4:8]))
		d.pos++
	}
	return frames, nil
}

// SeekTo respawns ffmpeg at `frame` (input -ss). Exact-position re-seek (paused re-audition of the
// same cursor) is a no-op so it doesn't churn a ~230ms restart. Only reached for STREAMED (huge)
// files; a RAM-preloaded track seeks in its index and never calls this.
func (d *ffmpegDecoder) SeekTo(frame int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("seek on closed decoder")
	}
	p := int(frame)
	if p < 0 {
		p = 0
	}
	if d.total > 0 && p > d.total {
		p = d.total
	}
	if d.out != nil && p == d.pos {
		return nil // already exactly here - nothing to respawn
	}
	return d.start(p)
}

func (d *ffmpegDecoder) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.stopChild()
	return nil
}

// readAhead drains ffmpeg stdout in a background goroutine into a bounded ring so the realtime pull
// never blocks on the pipe (Windows anon-pipe buffers are tiny; a CPU-throttled or bursty ffmpeg
// would otherwise underrun → choppy audio). Single consumer: ReadFrames, under d.mu.
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

// Read fills p from buffered chunks, blocking until data or EOF. Single-consumer (ReadFrames/d.mu).
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

// stop signals the reader goroutine to exit (called once per readAhead).
func (ra *readAhead) stop() { close(ra.done) }

// ffprobe result cache: an audition restart must not pay probe spawns per Play. Keyed by path,
// validated by mtime; entries are ~100B and a session touches few files (unbounded OK).
type ffprobeEntry struct {
	mtime  int64
	frames int
	codec  string
}

var (
	ffprobeMu    sync.Mutex
	ffprobeCache = map[string]ffprobeEntry{}
)

// ffprobeInfo returns total frames (sec*deviceRate; 0 unknown) + first audio codec_name (lowercased;
// "" unknown) in ONE ffprobe call, cached by path+mtime. The codec feeds mediatools.CodecLeadSkipMs
// so a SEEK can compensate the gapless encoder priming.
func ffprobeInfo(path string) (frames int, codec string) {
	var mtime int64
	if fi, err := os.Stat(path); err == nil {
		mtime = fi.ModTime().UnixNano()
	}
	ffprobeMu.Lock()
	if e, ok := ffprobeCache[path]; ok && e.mtime == mtime {
		ffprobeMu.Unlock()
		return e.frames, e.codec
	}
	ffprobeMu.Unlock()
	probe, ok := mediatools.Resolve("ffprobe")
	if !ok {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "format=duration:stream=codec_name",
		"-of", "default=noprint_wrappers=1", path)
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(ln), "=")
		if !found {
			continue
		}
		switch k {
		case "codec_name":
			codec = strings.ToLower(v)
		case "duration":
			if sec, perr := strconv.ParseFloat(v, 64); perr == nil && sec > 0 {
				frames = int(sec * float64(deviceRate))
			}
		}
	}
	ffprobeMu.Lock()
	ffprobeCache[path] = ffprobeEntry{mtime: mtime, frames: frames, codec: codec}
	ffprobeMu.Unlock()
	return frames, codec
}
