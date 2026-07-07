package timecode

import (
	"sync"
	"time"

	"rave.page/mate/internal/medialink"
)

// Generator is the master house clock: a monotonic frame counter driven by time.Now, from which
// every sink (LTC/MTC/Art-Net) derives the current frame. Start/Jam set the epoch + starting frame;
// Now returns the current Timecode; FrameNow the absolute frame index. Reads are lock-guarded and
// cheap, so ticker-paced sinks can resync against it every tick with no cumulative drift.
type Generator struct {
	mu         sync.Mutex
	rate       Rate
	startFrame int64     // absolute frame index at epoch
	epoch      time.Time // monotonic instant the counter was (re)based
	running    bool
	now        func() time.Time // injectable clock (tests)
}

// NewGenerator builds a stopped generator.
func NewGenerator() *Generator { return &Generator{now: time.Now} }

// SetNow injects the time source the frame counter advances on (nil = time.Now) - the medialink-
// disciplined clock seam. Set at wiring time, before Start/Jam: a running epoch stays in the old
// clock's domain.
func (g *Generator) SetNow(now func() time.Time) {
	g.mu.Lock()
	g.now = now
	g.mu.Unlock()
}

func (g *Generator) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// Start begins the clock at rate, based at startTC (its absolute frame index). Restarts if already
// running.
func (g *Generator) Start(rate Rate, startTC Timecode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rate = rate
	g.startFrame = startTC.Frames()
	g.epoch = g.clock()
	g.running = true
}

// Jam re-bases the clock to tc without stopping - the "locate" operation. Rate follows tc's rate
// if set, else keeps the current rate.
func (g *Generator) Jam(tc Timecode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if tc.Rate.Nominal != 0 {
		g.rate = tc.Rate
	}
	g.startFrame = tc.Frames()
	g.epoch = g.clock()
}

// Stop halts the clock (Now freezes at the last position; Running reports false).
func (g *Generator) Stop() {
	g.mu.Lock()
	g.running = false
	g.mu.Unlock()
}

// Running reports whether the clock is advancing.
func (g *Generator) Running() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running
}

// Rate returns the current frame rate.
func (g *Generator) Rate() Rate {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rate
}

// FrameNow returns the current absolute frame index (startFrame + elapsed frames). Frozen while
// stopped.
func (g *Generator) FrameNow() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.frameLocked()
}

func (g *Generator) frameLocked() int64 { return g.subFrameLocked(1) }

// subFrameLocked returns the current position in 1/div frame units (div=1 frames, div=4
// quarter-frames - MTC needs sub-frame resolution so the 8 pieces step within a frame pair).
func (g *Generator) subFrameLocked(div int64) int64 {
	if !g.running || g.rate.Nominal == 0 {
		return g.startFrame * div
	}
	num, den := g.rate.Exact()
	if num == 0 {
		return g.startFrame * div
	}
	elapsed := g.clock().Sub(g.epoch)
	if elapsed < 0 {
		elapsed = 0
	}
	// units = elapsed_seconds × fps × div = elapsed × num × div / (den × 1s)
	return g.startFrame*div + int64(elapsed)*num*div/(int64(time.Second)*den)
}

// QuarterFrameNow returns the current absolute quarter-frame index (4 per frame; frozen while
// stopped).
func (g *Generator) QuarterFrameNow() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.subFrameLocked(4)
}

// Now returns the current Timecode (drop-frame aware, wraps at 24h via the frame math).
func (g *Generator) Now() Timecode {
	g.mu.Lock()
	fr, rate := g.frameLocked(), g.rate
	g.mu.Unlock()
	return medialink.TimecodeFromFrames(fr, rate)
}
