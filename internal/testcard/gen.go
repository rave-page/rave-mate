package testcard

import (
	"crypto/rand"
	"fmt"
	"image"
	"sync"
	"time"
)

// SenderName is the fixed Spout name the generator publishes under. Add it in OBS as a Spout2
// Capture source (stretched to the canvas), or route it directly over a media route to bypass OBS.
const SenderName = "rave-mate testcard"

// Bounds. The generator is a diagnostic, not a media source: it must never become the load.
const (
	minDim, maxW, maxH = 240, 3840, 2160
	minFPS, maxFPS     = 1, 60
	DefaultW, DefaultH = 1280, 720
	DefaultFPS         = 30
)

// FrameSink is the sender seam (videoshare.FrameSender shaped; a test passes a func).
type FrameSink interface {
	Send(img *image.NRGBA) error
	Close()
}

// GenStats is the generator's own ground truth. Receiver verdicts are judged AGAINST this: a gap
// the generator itself logged (Skips, or FlagBehind in-frame) is the generator's fault, not the
// pipeline's.
type GenStats struct {
	Name      string    `json:"name"`
	W         int       `json:"w"`
	H         int       `json:"h"`
	FPS       int       `json:"fps"`
	Session   uint16    `json:"session"`
	Frames    uint64    `json:"frames"`
	Skips     uint64    `json:"skips"` // ticks missed because render+send overran the period
	StartedAt time.Time `json:"startedAt"`
	// SendMaxMs is the slowest single render+send observed - a sender blocking in a driver call
	// shows up here first.
	SendMaxMs int64 `json:"sendMaxMs"`
}

// AchievedFPS is frames over wall time since start.
func (s GenStats) AchievedFPS() float64 {
	el := time.Since(s.StartedAt).Seconds()
	if el <= 0 {
		return 0
	}
	return float64(s.Frames) / el
}

// Gen runs one testcard sender. One reusable frame buffer, synchronous Send (the FrameSender
// contract returns the buffer to us when Send returns) - no queue, nothing to bound.
type Gen struct {
	mu    sync.Mutex
	stats GenStats
	stop  chan struct{}
	done  chan struct{}
}

// NewGen starts rendering to sink at spec until Stop. Ownership of sink transfers (closed on Stop).
func NewGen(sink FrameSink, w, h, fps int) (*Gen, error) {
	if w < minDim || w > maxW || h < minDim || h > maxH || fps < minFPS || fps > maxFPS {
		return nil, fmt.Errorf("testcard: size/fps out of range (%dx%d@%d; allowed %d..%dx%d @ %d..%d)",
			w, h, fps, minDim, maxW, maxH, minFPS, maxFPS)
	}
	var sb [2]byte
	_, _ = rand.Read(sb[:])
	g := &Gen{
		stats: GenStats{Name: SenderName, W: w, H: h, FPS: fps,
			Session: (uint16(sb[0])<<8 | uint16(sb[1])) & 0x0FFF, StartedAt: time.Now()},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go g.run(sink)
	return g, nil
}

func (g *Gen) run(sink FrameSink) {
	defer close(g.done)
	defer sink.Close()
	g.mu.Lock()
	w, h, fps, session := g.stats.W, g.stats.H, g.stats.FPS, g.stats.Session
	g.mu.Unlock()

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	period := time.Second / time.Duration(fps)
	tick := time.NewTicker(period)
	defer tick.Stop()

	var seq uint32
	var flags uint8
	for {
		select {
		case <-g.stop:
			return
		case <-tick.C:
			// Drain ticks we overran: count them as skips and tell the RECEIVER in-frame, so a
			// seq gap caused here is attributable here.
			skipped := 0
			for {
				select {
				case <-tick.C:
					skipped++
				default:
					goto render
				}
			}
		render:
			if skipped > 0 {
				flags |= FlagBehind
			}
			now := time.Now()
			Render(img, Payload{Session: session, Seq: seq, T0ms: uint32(now.UnixMilli()),
				FPS: uint8(fps), Flags: flags}, now)
			t := time.Now()
			_ = sink.Send(img)
			sendMs := time.Since(t).Milliseconds()
			flags = 0
			seq++
			g.mu.Lock()
			g.stats.Frames++
			g.stats.Skips += uint64(skipped)
			g.stats.SendMaxMs = max(g.stats.SendMaxMs, sendMs)
			g.mu.Unlock()
		}
	}
}

// Stop ends the run and joins the loop (bounded by one Send).
func (g *Gen) Stop() {
	g.mu.Lock()
	select {
	case <-g.stop:
	default:
		close(g.stop)
	}
	g.mu.Unlock()
	<-g.done
}

// Stats snapshots the ground truth.
func (g *Gen) Stats() GenStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stats
}
