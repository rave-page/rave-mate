package mocapmaster

// master.go - Master wires the capture node's packet callback into the PoseStore and renders
// the composite mocap region each encoder frame. boneSlots and stage bounds are event config,
// fixed at stream start; frameCounter advances once per rendered frame (the spillover
// consumers' liveness signal, same rule as everywhere else in the contract).

import (
	"image"
	"image/draw"
	"sync"
	"time"

	"rave.page/mate/internal/mocapnode"
	"rave.page/mate/internal/mocappanel"
)

// Config wires a Master.
type Config struct {
	BoneSlots int           // S, 1..32 - fixed for the stream (region stride = 8 + 2*S)
	StageMin  [3]float64    // configured stage bounds, metres
	StageSize [3]float64    // all three > 0
	Staleness time.Duration // 0 = DefaultStaleness

	Now  func() time.Time                 // clock seam; nil = time.Now
	Logf func(format string, args ...any) // optional log sink (packet rejects)
}

// Master is the single-node mocap master: PoseStore + region renderer.
type Master struct {
	cfg   Config
	store *PoseStore

	mu           sync.Mutex
	frameCounter uint32
}

// New validates cfg and builds a Master.
func New(cfg Config) (*Master, error) {
	store, err := NewPoseStore(StoreConfig{
		BoneSlots: cfg.BoneSlots,
		StageMin:  cfg.StageMin, StageSize: cfg.StageSize,
		Staleness: cfg.Staleness,
	})
	if err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Master{cfg: cfg, store: store}, nil
}

// OnPacket is the mocapnode callback (mocapnode.Config.OnPacket = m.OnPacket). Packet-level
// rejects are logged, never fatal - a rotten node degrades, the master keeps rendering.
func (m *Master) OnPacket(pkt mocapnode.Packet) {
	if err := m.store.Accept(pkt); err != nil {
		m.cfg.Logf("mocapmaster: packet rejected: %v", err)
	}
}

// Store exposes the PoseStore (health surface, diagnostics).
func (m *Master) Store() *PoseStore { return m.store }

// RenderInto draws the current region state into one composite frame (the encoder loop's
// *image.RGBA satisfies draw.Image; see Overlay for the vrslgrid seam). Hips are requantized
// from world metres against the MASTER's bounds - the region header carries those, not any
// packet's. frameCounter++ per rendered frame.
func (m *Master) RenderInto(frame draw.Image) {
	now := m.cfg.Now()
	active := m.store.ActiveDancers(now)
	dancers := make([]mocappanel.Dancer, len(active))
	for i := range active {
		dc := active[i].Dancer
		for a := 0; a < 3; a++ {
			dc.HipsQ[a] = quantizeHips(active[i].HipsPos[a], m.cfg.StageMin[a], m.cfg.StageSize[a])
		}
		dancers[i] = dc
	}

	var flags uint16
	if m.store.Live(now) {
		flags |= RegionFlagLive
	}

	m.mu.Lock()
	fc := m.frameCounter
	m.frameCounter++
	m.mu.Unlock()

	renderRegionInto(frame, RegionHeader{
		Version:      RegionVersion,
		Flags:        flags,
		BoneSlots:    m.cfg.BoneSlots,
		DancerCount:  len(dancers),
		FrameCounter: fc,
		StageMin:     m.cfg.StageMin,
		StageSize:    m.cfg.StageSize,
	}, dancers)
}

// Overlay adapts RenderInto to the vrslgrid seam: vrslgrid.CompositeSpec{Overlay: m.Overlay()}.
// The region needs the extended composite (its calibration rides the composite's meta triad).
func (m *Master) Overlay() func(*image.RGBA) {
	return func(img *image.RGBA) { m.RenderInto(img) }
}

// quantizeHips maps a world coordinate onto the 16-bit hips lattice of the given bounds
// (q = round((p - min) / size * 65535), clamped - contract §4).
func quantizeHips(p, min, size float64) uint16 {
	if !(size > 0) {
		return 0
	}
	q := (p - min) / size * 65535
	if q < 0 {
		q = 0
	} else if q > 65535 {
		q = 65535
	}
	// round half away from zero matches math.Round on the non-negative clamped range
	return uint16(q + 0.5)
}
