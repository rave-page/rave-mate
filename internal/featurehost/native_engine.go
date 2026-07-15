package featurehost

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"rave.page/mate/internal/audio"
	"rave.page/mate/internal/logbus"
)

// nativeEngine adapts internal/audio.Engine to playerBackend: it runs the position tick loop +
// natural-end detection the callback-less native engine lacks, and falls back to the ffmpeg
// decoder for formats the native path can't decode (AAC/M4A — audio.ErrUnsupported). One clear
// logbus line names which engine served each file.
type nativeEngine struct {
	log     *logbus.Bus
	eng     *audio.Engine
	onTick  func(cur, total float64)
	onEnd   func()
	onError func(path, msg string)

	mu       sync.Mutex
	tickStop chan struct{}

	loadMu  sync.Mutex
	loading map[string]*loadCall // in-flight decodes keyed by path (dedup concurrent same-path load)
}

// loadCall is one in-flight decode. A concurrent load() of the same path waits on it instead of
// launching a duplicate full decode (2× RAM, and the late decode's source-swap would Close the
// playing one - clobbering an in-progress audition).
type loadCall struct {
	done chan struct{}
	err  error
}

func newNativeEngine(log *logbus.Bus, onTick func(cur, total float64), onEnd func(), onError func(path, msg string)) *nativeEngine {
	return &nativeEngine{log: log, eng: audio.NewEngine(), onTick: onTick, onEnd: onEnd, onError: onError}
}

// load opens path (native decoder, ffmpeg fallback), deduping concurrent loads of the SAME path so
// a background Preload racing the first hold-Space (which routes to PreviewFrom while the mirror
// still reads not-loaded) doesn't decode the whole file twice. Returns which engine served it.
func (n *nativeEngine) load(path string) (served string, err error) {
	if n.eng.Loaded() == path {
		return "cached", nil
	}
	n.loadMu.Lock()
	if n.eng.Loaded() == path { // completed between the check above and this lock
		n.loadMu.Unlock()
		return "cached", nil
	}
	if n.loading == nil {
		n.loading = map[string]*loadCall{}
	}
	if lc, ok := n.loading[path]; ok { // an in-flight decode of this path - share its result
		n.loadMu.Unlock()
		<-lc.done
		if lc.err != nil {
			return "", lc.err
		}
		return "cached", nil
	}
	lc := &loadCall{done: make(chan struct{})}
	n.loading[path] = lc
	n.loadMu.Unlock()

	served, err = n.decode(path)

	n.loadMu.Lock()
	delete(n.loading, path)
	n.loadMu.Unlock()
	lc.err = err
	close(lc.done)
	return served, err
}

// decode does the actual open: native decoder first, ffmpeg fallback on ErrUnsupported (AAC/M4A/…)
// OR any native failure on a format ffmpeg handles - covers a mis-sniff (opus in an Ogg container
// routes to the vorbis decoder and fails). A genuine native-format error (corrupt FLAC/MP3/WAV,
// which ffmpeg can't rescue here) still surfaces.
func (n *nativeEngine) decode(path string) (served string, err error) {
	if err = n.eng.Load(path); err == nil {
		return "native", nil
	}
	if !errors.Is(err, audio.ErrUnsupported) && !audio.FFmpegPlayable(path) {
		return "", err
	}
	dec, ferr := audio.OpenFFmpeg(path)
	if ferr != nil {
		return "", fmt.Errorf("native decode unavailable + ffmpeg fallback failed: %w", ferr)
	}
	if err = n.eng.LoadDecoder(dec, path); err != nil {
		return "", err
	}
	return "ffmpeg", nil
}

func (n *nativeEngine) logServed(path, served string) {
	if served == "cached" || n.log == nil {
		return
	}
	n.log.Info("player", "served "+filepath.Base(path), map[string]any{
		"engine": served, "ramPreload": n.eng.PreloadedRAM(),
	})
}

func (n *nativeEngine) PlayFrom(path string, startSec float64) error {
	served, err := n.load(path)
	if err != nil {
		n.reportErr(path, err)
		return err
	}
	n.logServed(path, served)
	n.eng.PlayFrom(startSec)
	n.startTicks()
	return nil
}

func (n *nativeEngine) PreviewFrom(path string, startSec float64) error {
	served, err := n.load(path)
	if err != nil {
		n.reportErr(path, err)
		return err
	}
	n.logServed(path, served)
	n.eng.PreviewFrom(startSec)
	n.startTicks()
	return nil
}

// Preload decodes path (RAM preload if it fits) without playing, so the first cue-edit Space is
// 0-latency. Idempotent for the already-loaded track.
func (n *nativeEngine) Preload(path string) error {
	served, err := n.load(path)
	if err != nil {
		n.reportErr(path, err)
		return err
	}
	n.logServed(path, served)
	return nil
}

func (n *nativeEngine) PreviewRelease(fallbackSec float64) {
	n.eng.PreviewRelease(fallbackSec)
	cur, total, ok := n.eng.Position()
	if ok && n.onTick != nil {
		n.onTick(cur, total) // reflect the snap-back position immediately
	}
}

func (n *nativeEngine) SeekTo(sec float64, _ bool) {
	n.eng.SeekTo(sec) // native seek is always sample-exact; the explicit flag is a beep-only guard
	cur, total, ok := n.eng.Position()
	if ok && n.onTick != nil {
		n.onTick(cur, total)
	}
}

func (n *nativeEngine) TogglePause() bool { return n.eng.TogglePause() }

func (n *nativeEngine) Stop() {
	n.stopTicks()
	n.eng.Stop()
}

func (n *nativeEngine) State() State {
	cur, total, ok := n.eng.Position()
	playing := n.eng.IsPlaying()
	return State{
		Path:    n.eng.Loaded(),
		Playing: ok,
		Paused:  ok && !playing,
		Cur:     cur,
		Total:   total,
	}
}

func (n *nativeEngine) reportErr(path string, err error) {
	if n.log != nil {
		n.log.Error("player", "native playback failed", map[string]any{"file": filepath.Base(path), "err": err.Error()})
	}
	if n.onError != nil {
		n.onError(path, filepath.Base(path)+" couldn't be played.")
	}
}

// startTicks (re)starts the 200ms position poll + natural-end detector (the native engine has no
// callbacks). One loop at a time.
func (n *nativeEngine) startTicks() {
	n.mu.Lock()
	if n.tickStop != nil {
		close(n.tickStop)
	}
	stop := make(chan struct{})
	n.tickStop = stop
	n.mu.Unlock()
	go n.tickLoop(stop)
}

func (n *nativeEngine) stopTicks() {
	n.mu.Lock()
	if n.tickStop != nil {
		close(n.tickStop)
		n.tickStop = nil
	}
	n.mu.Unlock()
}

func (n *nativeEngine) tickLoop(stop chan struct{}) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			cur, total, ok := n.eng.Position()
			if !ok {
				continue
			}
			if n.onTick != nil {
				n.onTick(cur, total)
			}
			// Natural end: the source drained to EOF (authoritative Drained flag) and the device
			// played it out. A cur>=total heuristic would also fire on a hold-audition release that
			// pauses/snaps near the tail - which wipes the mirror + kills this loop, dropping the next
			// spam-press off the warm-unpause path. Drained() is only set by a genuine read-to-EOF.
			drained := n.eng.Drained() && !n.eng.IsPlaying()
			n.mu.Lock()
			ended := drained && n.tickStop == stop
			if ended {
				n.tickStop = nil
			}
			n.mu.Unlock()
			if ended {
				if n.onEnd != nil {
					n.onEnd()
				}
				return
			}
		}
	}
}
