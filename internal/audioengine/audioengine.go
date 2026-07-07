// Package audioengine is the native audio playback engine (beep + oto), decoupled from the UI
// so it can run inside the `player` feature subprocess (featurehost) - a decode hang, codec
// fault, or oto crash then kills only that child, never the daemon/UI. One file plays at a time.
// No Fyne here: progress/end/errors are reported via plain callbacks the host turns into events.
// beep decodes mp3/wav/flac/ogg natively; anything else beep can't (aac/m4a/opus/aiff/alac/wma)
// decodes through ffmpeg (ffmpegdecode.go) on the SAME native transport. Only true video falls
// back to the app's external "Open".
package audioengine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gopxl/beep/v2"
	"github.com/gopxl/beep/v2/flac"
	"github.com/gopxl/beep/v2/mp3"
	"github.com/gopxl/beep/v2/speaker"
	"github.com/gopxl/beep/v2/vorbis"
	"github.com/gopxl/beep/v2/wav"

	"rave.page/mate/internal/logbus"
)

const speakerRate = beep.SampleRate(48000)

var (
	speakerOnce sync.Once
	speakerErr  error
)

func initSpeaker() error {
	speakerOnce.Do(func() {
		speakerErr = speaker.Init(speakerRate, speakerRate.N(time.Second/10))
	})
	return speakerErr
}

var playableExt = map[string]bool{
	".mp3": true, ".wav": true, ".flac": true, ".ogg": true, ".oga": true,
}

// IsPlayable reports whether the native engine can decode this file (else use "Open"). beep's
// built-ins always; the ffmpeg-only formats when ffmpeg is resolvable.
func IsPlayable(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return playableExt[ext] || (ffmpegPlayable[ext] && ffmpegAvailable())
}

func decodeAudio(path string) (s beep.StreamSeekCloser, format beep.Format, err error) {
	ext := strings.ToLower(filepath.Ext(path))
	// Prefer ffmpeg for anything it can decode: it streams and input-seeks (-ss) in ~0.2s even on a
	// large recording with no seek table, decoding straight to the 48k speaker rate (no resample).
	// beep's native FLAC/MP3/OGG seek rescans the WHOLE file on the first seek to build a seek index
	// - a raw device-capture FLAC (recorder writes no SEEKTABLE) froze playback ~15s per seek, on the
	// speaker lock. beep is the fallback only when ffmpeg isn't resolvable (WAV/MP3/FLAC/OGG still play).
	if (playableExt[ext] || ffmpegPlayable[ext]) && ffmpegAvailable() {
		return newFFmpegSource(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, beep.Format{}, err
	}
	// beep's codec init reads + parses headers and can PANIC on a malformed/truncated file (the
	// FLAC slice-bounds bug, bad MP3 frames). This runs inside Play → a panic here would kill the
	// player child (host restart = the "player keeps crashing" loop). Recover → clean error so the
	// corrupt file is skipped with a toast instead. The streaming phase is guarded by safeStreamer.
	defer func() {
		if r := recover(); r != nil {
			_ = f.Close()
			s, format, err = nil, beep.Format{}, fmt.Errorf("decode %s: %v", filepath.Base(path), r)
		}
	}()
	switch ext {
	case ".mp3":
		s, format, err = mp3.Decode(f)
	case ".wav":
		s, format, err = wav.Decode(f)
	case ".flac":
		s, format, err = flac.Decode(f)
	case ".ogg", ".oga":
		s, format, err = vorbis.Decode(f)
	default:
		_ = f.Close()
		return nil, beep.Format{}, fmt.Errorf("unsupported format")
	}
	if err != nil {
		_ = f.Close() // decoder doesn't own the file on error - close it ourselves (no leak)
	}
	return s, format, err
}

// State is a snapshot of the engine for the UI mirror.
type State struct {
	Path    string  `json:"path"`
	Playing bool    `json:"playing"`
	Paused  bool    `json:"paused"`
	Cur     float64 `json:"cur"`
	Total   float64 `json:"total"`
}

// Engine owns the currently-playing stream. Callbacks (all may be nil) fire from the engine's
// own goroutines - the host marshals them onto the wire as events; the daemon proxy re-dispatches
// onto the Fyne thread. onError carries a decode-failure (file base name + reason) for a toast.
type Engine struct {
	log     *logbus.Bus
	onTick  func(cur, total float64)
	onEnd   func()
	onError func(path, msg string)

	mu       sync.Mutex
	streamer beep.StreamSeekCloser
	format   beep.Format
	ctrl     *beep.Ctrl
	path     string
	paused   bool
	total    int // cached sample length (0 = not yet known); never computed on a hot path
	tickStop chan struct{}
}

// New builds an engine. Any callback may be nil.
func New(log *logbus.Bus, onTick func(cur, total float64), onEnd func(), onError func(path, msg string)) *Engine {
	return &Engine{log: log, onTick: onTick, onEnd: onEnd, onError: onError}
}

// safeStreamer wraps a decoder so a panic in beep's codec (e.g. the malformed-FLAC-frame
// slice-bounds bug) is recovered ON the oto audio goroutine instead of killing the child. On
// panic it marks itself drained so playback ends cleanly + onEnd fires, and reports once.
type safeStreamer struct {
	inner beep.Streamer
	e     *Engine
	path  string
	dead  bool
}

func (s *safeStreamer) Stream(samples [][2]float64) (n int, ok bool) {
	if s.dead {
		return 0, false
	}
	defer func() {
		if r := recover(); r != nil {
			s.dead = true
			n, ok = 0, false
			s.e.reportDecodePanic(s.path, r)
		}
	}()
	return s.inner.Stream(samples)
}

func (s *safeStreamer) Err() error {
	if e, ok := s.inner.(interface{ Err() error }); ok {
		return e.Err()
	}
	return nil
}

func (e *Engine) reportDecodePanic(path string, r any) {
	name := filepath.Base(path)
	if e.log != nil {
		e.log.Error("player", "audio decode failed - file skipped",
			map[string]any{"file": name, "panic": fmt.Sprintf("%v", r)})
	}
	if e.onError != nil {
		e.onError(path, name+" couldn't be decoded and was stopped.")
	}
}

func safeLen(s beep.StreamSeekCloser) (n int) {
	defer func() {
		if recover() != nil {
			n = 0
		}
	}()
	return s.Len()
}

// Play decodes + starts path, replacing any current playback.
func (e *Engine) Play(path string) error {
	if err := initSpeaker(); err != nil {
		return err
	}
	e.Stop()
	s, format, err := decodeAudio(path)
	if err != nil {
		return err
	}

	var stream beep.Streamer = s
	if format.SampleRate != speakerRate {
		stream = beep.Resample(4, format.SampleRate, speakerRate, s)
	}
	stream = &safeStreamer{inner: stream, e: e, path: path}
	ctrl := &beep.Ctrl{Streamer: stream}
	stop := make(chan struct{})

	e.mu.Lock()
	e.streamer, e.format, e.ctrl, e.path, e.paused, e.total, e.tickStop = s, format, ctrl, path, false, 0, stop
	e.mu.Unlock()

	// Prime the cached length off the hot path (s.Len can be O(file) for MP3).
	go func() {
		speaker.Lock()
		l := safeLen(s)
		speaker.Unlock()
		e.mu.Lock()
		if e.tickStop == stop {
			e.total = l
		}
		e.mu.Unlock()
	}()

	done := func() {
		e.mu.Lock()
		mine := e.tickStop == stop
		if mine {
			e.streamer, e.ctrl, e.path, e.total, e.tickStop = nil, nil, "", 0, nil
		}
		e.mu.Unlock()
		if !mine {
			return
		}
		close(stop)
		_ = s.Close()
		if e.onEnd != nil {
			e.onEnd()
		}
	}
	speaker.Play(beep.Seq(ctrl, beep.Callback(done)))
	go e.tickLoop(stop)
	return nil
}

func (e *Engine) tickLoop(stop chan struct{}) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			cur, total, ok := e.position()
			if ok && e.onTick != nil {
				e.onTick(cur, total)
			}
		}
	}
}

// TogglePause flips play/pause; returns the resulting paused state.
func (e *Engine) TogglePause() bool {
	e.mu.Lock()
	ctrl := e.ctrl
	e.mu.Unlock()
	if ctrl == nil {
		return false
	}
	speaker.Lock()
	ctrl.Paused = !ctrl.Paused
	paused := ctrl.Paused
	speaker.Unlock()
	e.mu.Lock()
	e.paused = paused
	e.mu.Unlock()
	return paused
}

// State snapshots current playback.
func (e *Engine) State() State {
	cur, total, ok := e.position()
	e.mu.Lock()
	defer e.mu.Unlock()
	return State{Path: e.path, Playing: ok && e.streamer != nil, Paused: e.paused, Cur: cur, Total: total}
}

// Seek jumps to sec seconds into the current track. Uses the cached length (no s.Len() here).
func (e *Engine) Seek(sec float64) {
	defer func() {
		if r := recover(); r != nil && e.log != nil {
			e.log.Warn("player", "seek failed", map[string]any{"panic": fmt.Sprintf("%v", r)})
		}
	}()
	e.mu.Lock()
	s, format, total := e.streamer, e.format, e.total
	e.mu.Unlock()
	if s == nil {
		return
	}
	n := format.SampleRate.N(time.Duration(sec * float64(time.Second)))
	if n < 0 {
		n = 0
	}
	if total > 0 && n >= total {
		n = total - 1
	}
	// Re-check under e.mu (while holding speaker.Lock, matching done's lock order) that s is still
	// the live streamer - Stop/done swap e.streamer→nil under e.mu *before* Close, so e.streamer==s
	// guarantees s isn't closed. Without this, a seek racing a stop seeks a closed decoder.
	speaker.Lock()
	e.mu.Lock()
	if e.streamer == s {
		_ = s.Seek(n)
	}
	e.mu.Unlock()
	speaker.Unlock()
}

// position returns current + total seconds and whether a track is loaded (cached total → O(1)).
func (e *Engine) position() (cur, total float64, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			cur, total, ok = 0, 0, false
		}
	}()
	e.mu.Lock()
	s, format, length := e.streamer, e.format, e.total
	e.mu.Unlock()
	if s == nil {
		return 0, 0, false
	}
	// Same live-streamer re-check as Seek (speaker.Lock → e.mu): avoids Position() on a decoder
	// that Stop/done closed between the e.mu read above and acquiring speaker.Lock.
	speaker.Lock()
	e.mu.Lock()
	live := e.streamer == s
	var pos int
	if live {
		pos = s.Position()
	}
	e.mu.Unlock()
	speaker.Unlock()
	if !live {
		return 0, 0, false
	}
	return format.SampleRate.D(pos).Seconds(), format.SampleRate.D(length).Seconds(), true
}

// Stop halts playback and frees the stream. Safe to call repeatedly / when idle.
func (e *Engine) Stop() {
	e.mu.Lock()
	s, stop := e.streamer, e.tickStop
	e.streamer, e.ctrl, e.path, e.paused, e.total, e.tickStop = nil, nil, "", false, 0, nil
	e.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if s != nil {
		speaker.Clear()
		_ = s.Close()
	}
}
