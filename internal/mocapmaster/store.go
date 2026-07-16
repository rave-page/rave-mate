package mocapmaster

// store.go - PoseStore: latest sanity-gated pose per dancer, keyed by (sourceTag, sessionNonce)
// stream key then localId, with primary election across stream keys. v1 single-node collapse:
// one capture node feeds packets, so election degenerates to "the freshest live stream wins
// once the current primary goes stale" - a world restart (new sessionNonce) or a tag change
// hands over within one staleness window, without flapping while the primary is healthy.
//
// Sanity gates (contract §7.4 - volunteer nodes are untrusted): hips inside the MASTER's
// configured stage bounds (+5% slack; the packet header's own bounds only dequantize), the
// quaternion norm rule per bone (norm-invalid CORE bone rejects the dancer - undefined root is
// worse than no puppet; non-core just goes absent), staleness window on dancers and streams.
// Reject dancer, never packet; reject packet only when its header cannot be trusted at all
// (boneSlots != stream config, non-positive stage size).

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"rave.page/mate/internal/mocapnode"
	"rave.page/mate/internal/mocappanel"
)

// DefaultStaleness is the dancer/stream staleness window (~15 frames @30Hz - the panel
// decoder's DefaultStalenessWindow expressed in wall time).
const DefaultStaleness = 500 * time.Millisecond

// hipsSlackFrac widens the configured stage bounds by 5% of stage size per axis (contract §7.4).
const hipsSlackFrac = 0.05

// pruneAfter multiples of the staleness window before dead streams/dancers are dropped from the
// maps (bookkeeping only - staleness already excludes them from ActiveDancers).
const pruneAfter = 20

// StreamKey identifies one panel stream: (sourceTag, sessionNonce). localId is unique within
// SourceTag; the composite key (sourceTag, localId) is global (contract §4).
type StreamKey struct {
	SourceTag    uint32
	SessionNonce uint16
}

// StoreConfig wires a PoseStore. BoneSlots and the stage bounds come from event config
// (master-authoritative); packets are gated against them.
type StoreConfig struct {
	BoneSlots int           // S, fixed at stream start; packets with a different S are rejected (stream-restart semantics)
	StageMin  [3]float64    // configured stage bounds, metres (hips gate + region header)
	StageSize [3]float64    // all three must be > 0
	Staleness time.Duration // dancer/stream staleness window; 0 = DefaultStaleness
}

// ActiveDancer is one live pose. Dancer is store-normalized: Quats/Present recomputed from the
// wire Rots (incoming Present/Quats are untrusted). HipsPos is the world position dequantized
// via the PACKET's stage bounds - the region render requantizes it against the master's bounds.
// The embedded slices are shared read-only snapshots; do not mutate.
type ActiveDancer struct {
	mocappanel.Dancer
	HipsPos  [3]float64
	LastSeen time.Time
}

// StreamHealth is one stream's counters (primary flag as of the health call's election).
type StreamHealth struct {
	Key              StreamKey
	Primary          bool
	LastSeen         time.Time
	Packets          uint64 // packets seen (incl. rejected)
	PacketRejects    uint64
	DancerRejects    uint64
	LastFrameCounter uint32
}

// PoseStore keeps the latest pose per (streamKey, localId). Safe for concurrent use (packet
// callback goroutine vs encoder loop).
type PoseStore struct {
	cfg StoreConfig

	mu         sync.Mutex
	streams    map[StreamKey]*streamState
	primary    StreamKey
	hasPrimary bool
}

type streamState struct {
	lastSeen         time.Time // last ACCEPTED packet (rejects don't keep a stream alive)
	packets          uint64
	packetRejects    uint64
	dancerRejects    uint64
	lastFrameCounter uint32
	dancers          map[uint16]*ActiveDancer
}

// NewPoseStore validates cfg and builds an empty store.
func NewPoseStore(cfg StoreConfig) (*PoseStore, error) {
	if cfg.BoneSlots < 1 || cfg.BoneSlots > mocappanel.BoneSlotMax {
		return nil, fmt.Errorf("mocapmaster: boneSlots %d outside 1..%d", cfg.BoneSlots, mocappanel.BoneSlotMax)
	}
	for i := 0; i < 3; i++ {
		if !(cfg.StageSize[i] > 0) {
			return nil, fmt.Errorf("mocapmaster: stageSize[%d] = %v not positive", i, cfg.StageSize[i])
		}
	}
	return &PoseStore{cfg: cfg, streams: map[StreamKey]*streamState{}}, nil
}

// Accept ingests one decoded node packet. Packet-level rejects (returned error): boneSlots not
// matching the stream config, non-positive packet stage size. Dancer-level problems silently
// reject the dancer (counted in health). Time base is pkt.CapturedAt.
func (s *PoseStore) Accept(pkt mocapnode.Packet) error {
	h := pkt.Header
	key := StreamKey{SourceTag: h.SourceTag, SessionNonce: h.SessionNonce}
	now := pkt.CapturedAt

	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[key]
	if st == nil {
		st = &streamState{dancers: map[uint16]*ActiveDancer{}}
		s.streams[key] = st
	}
	st.packets++

	if h.BoneSlots != s.cfg.BoneSlots {
		st.packetRejects++
		return fmt.Errorf("mocapmaster: packet boneSlots %d != stream config %d (stride fixed at stream start)", h.BoneSlots, s.cfg.BoneSlots)
	}
	for i := 0; i < 3; i++ {
		if !(h.StageSize[i] > 0) {
			st.packetRejects++
			return fmt.Errorf("mocapmaster: packet stageSize[%d] = %v not positive", i, h.StageSize[i])
		}
	}

	st.lastSeen = now
	st.lastFrameCounter = h.FrameCounter
	for i := range pkt.Dancers {
		ad, ok := s.vetDancer(h, &pkt.Dancers[i], now)
		if !ok {
			st.dancerRejects++
			continue
		}
		st.dancers[ad.LocalID] = ad
	}
	s.prune(now)
	s.elect(now)
	return nil
}

// vetDancer applies the per-dancer gates and rebuilds the pose from the wire truth (BoneMask +
// Rots); incoming Quats/Present are recomputed, never trusted.
func (s *PoseStore) vetDancer(h mocappanel.Header, d *mocappanel.Dancer, now time.Time) (*ActiveDancer, bool) {
	if d.Flags&mocappanel.DancerPresent == 0 || d.BoneMask&mocappanel.CoreMask != mocappanel.CoreMask {
		return nil, false
	}

	// Hips gate: dequantize via the packet's bounds, check against the CONFIGURED bounds +5%.
	var pos [3]float64
	for i := 0; i < 3; i++ {
		pos[i] = h.StageMin[i] + float64(d.HipsQ[i])/65535*h.StageSize[i]
		slack := hipsSlackFrac * s.cfg.StageSize[i]
		if pos[i] < s.cfg.StageMin[i]-slack || pos[i] > s.cfg.StageMin[i]+s.cfg.StageSize[i]+slack {
			return nil, false
		}
	}

	sl := s.cfg.BoneSlots
	nd := mocappanel.Dancer{
		LocalID: d.LocalID, Flags: d.Flags, BoneMask: d.BoneMask, HipsQ: d.HipsQ,
		Rots:    make([]uint32, sl),
		Quats:   make([][4]float64, sl),
		Present: make([]bool, sl),
	}
	for k := 0; k < sl; k++ {
		if d.BoneMask>>k&1 == 0 {
			continue
		}
		var w uint32
		if k < len(d.Rots) {
			w = d.Rots[k]
		}
		nd.Rots[k] = w
		q, ok := mocappanel.UnpackQuat(w)
		if !ok {
			if mocappanel.CoreMask>>k&1 == 1 {
				return nil, false // norm-invalid core bone: undefined root -> reject dancer
			}
			continue // non-core: bone absent this frame
		}
		nd.Quats[k] = q
		nd.Present[k] = true
	}
	return &ActiveDancer{Dancer: nd, HipsPos: pos, LastSeen: now}, true
}

// ActiveDancers returns up to MaxDancers live dancers of the primary stream, ordered by
// localId; dancers (or the whole stream) past the staleness window are excluded. Elements are
// snapshots (embedded slices shared read-only).
func (s *PoseStore) ActiveDancers(now time.Time) []ActiveDancer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.elect(now)
	if !s.hasPrimary {
		return nil
	}
	st := s.streams[s.primary]
	if st == nil || !s.fresh(st.lastSeen, now) {
		return nil
	}
	out := make([]ActiveDancer, 0, len(st.dancers))
	for _, d := range st.dancers {
		if s.fresh(d.LastSeen, now) {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocalID < out[j].LocalID })
	if len(out) > mocappanel.MaxDancers {
		out = out[:mocappanel.MaxDancers]
	}
	return out
}

// Live reports whether a fresh primary stream exists at now (the region header's live flag).
func (s *PoseStore) Live(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.elect(now)
	if !s.hasPrimary {
		return false
	}
	st := s.streams[s.primary]
	return st != nil && s.fresh(st.lastSeen, now)
}

// Health snapshots every known stream (sorted by tag, nonce) after electing at now.
func (s *PoseStore) Health(now time.Time) []StreamHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.elect(now)
	out := make([]StreamHealth, 0, len(s.streams))
	for k, st := range s.streams {
		out = append(out, StreamHealth{
			Key: k, Primary: s.hasPrimary && k == s.primary,
			LastSeen: st.lastSeen, Packets: st.packets,
			PacketRejects: st.packetRejects, DancerRejects: st.dancerRejects,
			LastFrameCounter: st.lastFrameCounter,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.SourceTag != out[j].Key.SourceTag {
			return out[i].Key.SourceTag < out[j].Key.SourceTag
		}
		return out[i].Key.SessionNonce < out[j].Key.SessionNonce
	})
	return out
}

// elect keeps the current primary while it is fresh (no flapping); once stale/absent, the
// freshest live stream takes over. Callers hold s.mu.
func (s *PoseStore) elect(now time.Time) {
	if s.hasPrimary {
		if st, ok := s.streams[s.primary]; ok && s.fresh(st.lastSeen, now) {
			return
		}
	}
	var best StreamKey
	var bestSeen time.Time
	found := false
	for k, st := range s.streams {
		if s.fresh(st.lastSeen, now) && (!found || st.lastSeen.After(bestSeen)) {
			best, bestSeen, found = k, st.lastSeen, true
		}
	}
	s.primary, s.hasPrimary = best, found
}

// prune drops long-dead streams and dancer entries (bounded maps across world restarts /
// dancer churn). Callers hold s.mu.
func (s *PoseStore) prune(now time.Time) {
	limit := time.Duration(pruneAfter) * s.staleness()
	for k, st := range s.streams {
		if now.Sub(st.lastSeen) > limit {
			delete(s.streams, k)
			continue
		}
		for id, d := range st.dancers {
			if now.Sub(d.LastSeen) > limit {
				delete(st.dancers, id)
			}
		}
	}
}

func (s *PoseStore) fresh(seen time.Time, now time.Time) bool {
	return !seen.IsZero() && now.Sub(seen) <= s.staleness()
}

func (s *PoseStore) staleness() time.Duration {
	if s.cfg.Staleness > 0 {
		return s.cfg.Staleness
	}
	return DefaultStaleness
}
