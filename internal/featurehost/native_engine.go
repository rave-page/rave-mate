package featurehost

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"rave.page/mate/internal/audio"
	"rave.page/mate/internal/audioengine"
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
	wasPlay  bool
}

func newNativeEngine(log *logbus.Bus, onTick func(cur, total float64), onEnd func(), onError func(path, msg string)) *nativeEngine {
	return &nativeEngine{log: log, eng: audio.NewEngine(), onTick: onTick, onEnd: onEnd, onError: onError}
}

// load opens path with the native decoder, falling back to the ffmpeg-backed audio.Decoder for
// unsupported formats. Skips the decode if path is already loaded. Returns which engine served it.
func (n *nativeEngine) load(path string) (served string, err error) {
	if n.eng.Loaded() == path {
		return "cached", nil
	}
	err = n.eng.Load(path)
	if err == nil {
		return "native", nil
	}
	if !errors.Is(err, audio.ErrUnsupported) {
		return "", err
	}
	dec, ferr := audioengine.NewFFmpegDecoder(path)
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

func (n *nativeEngine) State() audioengine.State {
	cur, total, ok := n.eng.Position()
	playing := n.eng.IsPlaying()
	return audioengine.State{
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
	n.wasPlay = true
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
			// Natural end: was playing, device drained, at/after the tail.
			playing := n.eng.IsPlaying()
			n.mu.Lock()
			ended := n.wasPlay && !playing && total > 0 && cur >= total-0.1 && n.tickStop == stop
			if playing {
				n.wasPlay = true
			}
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
