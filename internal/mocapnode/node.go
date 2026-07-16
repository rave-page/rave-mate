package mocapnode

// node.go - the capture-node loop: Source -> (locked ? cheap anchor revalidation : full
// locate) -> rectified stateful decode -> packet callback (+ optional JSON-lines dump). Health
// mirrors the watchdog needs from the design doc: fps, lock state, decode success rate, last
// frameCounter (a rotten node auto-retires master-side on these).

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"rave.page/mate/internal/mocappanel"
)

// relocateAfter forces a full re-locate after this many consecutive decode failures while the
// cheap revalidation still passes (subtle drift the five-point check can miss).
const relocateAfter = 5

// Packet is one decoded panel frame, as delivered to OnPacket and the JSON-lines dump.
type Packet struct {
	CapturedAt time.Time           `json:"capturedAt"`
	Header     mocappanel.Header   `json:"header"`
	Dancers    []mocappanel.Dancer `json:"dancers"`
}

// Health is the node's live snapshot (all counters since Run started).
type Health struct {
	FPS         float64 // ingested frames/s over the last measurement window
	Locked      bool
	Identity    bool // identity fast-path active
	Live        bool // decoder liveness: MAGIC locked AND frameCounter advancing
	Frames      uint64
	Decoded     uint64
	Failed      uint64  // locate + decode failures
	SuccessRate float64 // Decoded / (Decoded + Failed)
	LastCounter uint32  // last decoded frameCounter
	LastErr     string
}

// Config wires a Node. Only Source is mandatory.
type Config struct {
	Source   Source
	OnPacket func(Packet)                     // per decoded frame; nil = health/dump only
	Dump     io.Writer                        // optional JSON-lines packet dump
	Logf     func(format string, args ...any) // optional log sink
}

// Node runs the capture loop. One Node per capture stream; Run once.
type Node struct {
	cfg Config
	dec *mocappanel.Decoder

	mu         sync.Mutex
	health     Health
	lock       Lock
	locked     bool
	failStreak int
	fpsN       int
	fpsMark    time.Time
}

// New builds a Node.
func New(cfg Config) *Node {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Node{cfg: cfg, dec: mocappanel.NewDecoder()}
}

// Run pumps the source until ctx cancels (nil) or the source fails fatally.
func (n *Node) Run(ctx context.Context) error {
	n.mu.Lock()
	n.fpsMark = time.Now()
	n.mu.Unlock()
	return n.cfg.Source.Frames(ctx, n.handle)
}

// Health returns the live snapshot.
func (n *Node) Health() Health {
	n.mu.Lock()
	defer n.mu.Unlock()
	h := n.health
	h.Locked = n.locked
	h.Identity = n.locked && n.lock.Identity
	h.Live = n.dec.Live()
	if total := h.Decoded + h.Failed; total > 0 {
		h.SuccessRate = float64(h.Decoded) / float64(total)
	}
	return h
}

// handle processes one captured frame (called from the source's goroutine).
func (n *Node) handle(f Frame) {
	n.tickFPS()

	if n.locked && !n.lock.Revalidate(&f) {
		n.cfg.Logf("mocapnode: anchors moved - re-locating")
		n.unlock()
	}
	if !n.locked {
		lk, err := Locate(&f)
		if err != nil {
			n.fail(err.Error())
			return
		}
		n.mu.Lock()
		n.lock, n.locked, n.failStreak = lk, true, 0
		n.mu.Unlock()
		n.cfg.Logf("mocapnode: panel locked (identity=%v anchors=%.1f)", lk.Identity, lk.Anchors)
	}

	h, dancers, err := n.dec.DecodeSampled(n.lock.Sampler(&f))
	if err != nil {
		n.fail(err.Error())
		n.mu.Lock()
		n.failStreak++
		relocate := n.failStreak >= relocateAfter
		n.mu.Unlock()
		if relocate {
			n.cfg.Logf("mocapnode: %d consecutive decode failures - dropping lock", relocateAfter)
			n.unlock()
		}
		return
	}

	n.mu.Lock()
	n.failStreak = 0
	n.health.Decoded++
	n.health.LastCounter = h.FrameCounter
	n.health.LastErr = ""
	n.mu.Unlock()

	pkt := Packet{CapturedAt: time.Now(), Header: h, Dancers: dancers}
	if n.cfg.OnPacket != nil {
		n.cfg.OnPacket(pkt)
	}
	n.dumpPacket(pkt)
}

// dumpPacket appends one JSON line; a write error disables the dump (never kills the loop).
func (n *Node) dumpPacket(pkt Packet) {
	if n.cfg.Dump == nil {
		return
	}
	line, err := json.Marshal(pkt)
	if err == nil {
		line = append(line, '\n')
		_, err = n.cfg.Dump.Write(line)
	}
	if err != nil {
		n.cfg.Logf("mocapnode: packet dump failed (%v) - dump disabled", err)
		n.cfg.Dump = nil
	}
}

func (n *Node) fail(msg string) {
	n.mu.Lock()
	n.health.Failed++
	n.health.LastErr = msg
	n.mu.Unlock()
}

func (n *Node) unlock() {
	n.mu.Lock()
	n.locked = false
	n.failStreak = 0
	n.mu.Unlock()
}

// tickFPS counts ingested frames and folds them into Health.FPS once per second.
func (n *Node) tickFPS() {
	n.mu.Lock()
	n.health.Frames++
	n.fpsN++
	if el := time.Since(n.fpsMark); el >= time.Second {
		n.health.FPS = float64(n.fpsN) / el.Seconds()
		n.fpsN = 0
		n.fpsMark = time.Now()
	}
	n.mu.Unlock()
}
