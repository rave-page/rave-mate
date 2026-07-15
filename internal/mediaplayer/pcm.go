package mediaplayer

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// sampleRate is the fixed audio output rate (Hz); ffmpeg resamples to it, the oto device runs at it.
const sampleRate = 48000

// bytesPerFrame = 2 channels × int16 (the ffmpeg s16le output the oto player consumes).
const bytesPerFrame = 4

// pcmReader passes an ffmpeg s16le/48k/stereo stdout through to the oto player and counts the stereo
// sample-frames consumed. samples (atomic) is the master-clock source: pos = samples/rate. A paused
// oto player stops pulling, so the count freezes - no extra pause logic here. Volume is applied by
// the oto player (SetVolume), not here. rem carries bytes that don't complete a frame across reads
// (oto reads arbitrary lengths).
type pcmReader struct {
	r       io.Reader
	samples *int64 // atomic counter of stereo frames consumed
	rem     int    // sub-frame bytes carried to the next read
}

// Read passes bytes straight to oto, advancing the master-clock counter by whole frames consumed.
func (s *pcmReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.rem += n
		if frames := s.rem / bytesPerFrame; frames > 0 {
			s.rem -= frames * bytesPerFrame
			atomic.AddInt64(s.samples, int64(frames))
		}
	}
	return n, err
}

// wallClock is a pausable wall-time master clock for video-only (no-audio) playback.
type wallClock struct {
	mu      sync.Mutex
	offset  float64 // seek start position (s)
	acc     float64 // accumulated running time while not paused (s)
	start   time.Time
	running bool
}

func newWallClock(offset float64) *wallClock { return &wallClock{offset: offset} }

// pos returns the current position in seconds.
func (w *wallClock) pos() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.offset + w.acc
	if w.running {
		p += time.Since(w.start).Seconds()
	}
	return p
}

// resume starts/continues the clock.
func (w *wallClock) resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		w.start = time.Now()
		w.running = true
	}
}

// pause freezes the clock.
func (w *wallClock) pause() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		w.acc += time.Since(w.start).Seconds()
		w.running = false
	}
}
