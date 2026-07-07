package mediaplayer

import (
	"encoding/binary"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// sampleRate is the fixed audio output rate (Hz); ffmpeg resamples to it, the speaker runs at it.
const sampleRate = 48000

// pcmStreamer adapts an ffmpeg s16le/48k/stereo stdout to a beep.Streamer and counts the
// stereo sample-frames consumed. samples (atomic) is the master-clock source: pos = samples/rate.
// On EOF it drains (returns ok=false); a paused beep.Ctrl simply stops pulling so the count
// freezes - no extra pause logic needed here.
type pcmStreamer struct {
	r       io.Reader
	samples *int64 // atomic counter of stereo frames streamed
	vol     *int64 // atomic volume 0–100 (nil = full)
	buf     []byte
	eof     bool
}

// Stream fills out with decoded samples, advancing the master-clock counter; ok=false when drained.
func (s *pcmStreamer) Stream(out [][2]float64) (n int, ok bool) {
	if s.eof {
		return 0, false
	}
	need := len(out) * 4 // 2 channels × int16
	if cap(s.buf) < need {
		s.buf = make([]byte, need)
	}
	b := s.buf[:need]
	got, err := io.ReadFull(s.r, b)
	frames := got / 4
	gain := 1.0
	if s.vol != nil {
		gain = float64(atomic.LoadInt64(s.vol)) / 100
	}
	for i := 0; i < frames; i++ {
		l := int16(binary.LittleEndian.Uint16(b[i*4:]))
		r := int16(binary.LittleEndian.Uint16(b[i*4+2:]))
		out[i][0] = float64(l) / 32768 * gain
		out[i][1] = float64(r) / 32768 * gain
	}
	if frames > 0 {
		atomic.AddInt64(s.samples, int64(frames))
	}
	if err != nil { // EOF / ErrUnexpectedEOF / closed pipe → drain after these frames
		s.eof = true
	}
	if frames == 0 {
		return 0, false
	}
	return frames, true
}

// Err satisfies beep.Streamer (no streaming error surface - ffmpeg failures show as EOF).
func (s *pcmStreamer) Err() error { return nil }

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
