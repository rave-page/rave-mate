// Package eventbus is a topic-based pub/sub bus shared across linked rave-mate instances. Local
// subscribers receive events published anywhere on the mesh; producers publish once and the bus
// fans out locally + over the peerlink ChanBus to every connected peer. This is the spine that
// lets a capability owned by one instance (Twitch on the stream PC, MIDI on the DJ PC) be consumed
// transparently by another (VR overlays on a third PC).
//
// Transport is optional: with no peers wired the bus is a local-only pub/sub. Wire SetTransport to
// go networked. Loop/duplicate suppression is per-origin monotonic seq, so the bus relays events
// it receives to its other peers (transitive delivery across a non-fully-meshed set) without storms.
package eventbus

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"

	"rave.page/mate/internal/logbus"
)

const logTag = "eventbus"

// TopicCaps is the control topic carrying a node's capability advertisement (Envelope.Caps).
const TopicCaps = "__caps"

// Envelope is the ChanBus wire format: a topic + opaque payload tagged with the originating node
// and a per-origin monotonic seq for dedup/relay. Caps is set only on TopicCaps control frames.
type Envelope struct {
	Topic  string          `json:"topic"`
	Origin string          `json:"origin"`          // originating node id
	Epoch  uint64          `json:"epoch,omitempty"` // origin's per-process boot id (seq resets on restart)
	Seq    uint64          `json:"seq"`             // per-origin monotonic within an epoch
	Caps   []string        `json:"caps,omitempty"`  // capabilities (TopicCaps only)
	Data   json.RawMessage `json:"data,omitempty"`
}

// originSeen tracks the current epoch + last seq accepted from one origin, plus recently-superseded
// epochs so a straggler from a peer's previous process is dropped (not mistaken for another restart).
type originSeen struct {
	epoch   uint64
	seq     uint64
	retired []uint64 // superseded epochs, newest last, capped (straggler suppression)
}

// Event is what a local subscriber receives.
type Event struct {
	Topic  string
	Origin string // node id of the producer
	Local  bool   // produced on this node
	Data   json.RawMessage
}

// Bus is the local hub + peer fan-out. Create with New; safe for concurrent use.
type Bus struct {
	log  *logbus.Bus
	self string // local node id

	mu       sync.Mutex
	subs     map[string]map[int]func(Event)
	nextID   int
	epoch    uint64                 // this process's boot id (stamped on every outbound frame)
	seq      uint64                 // per-process monotonic (resets each start; scoped by epoch)
	seen     map[string]*originSeen // origin → current epoch + last seq (dedup across restarts)
	localCap []string               // capabilities this node owns
	peerCap  map[string][]string    // peer node id → advertised capabilities

	broadcast func(payload []byte)                // send to all peers (nil = local-only)
	sendTo    func(nodeID string, payload []byte) // send to one peer (nil = local-only)

	// cumulative publish-path counters (Stats - perfmon probe)
	nPub, nInbound, nDup, nRelayed uint64
}

// New builds a local-only bus for the given node id. Call SetTransport to make it networked.
func New(log *logbus.Bus, selfNodeID string) *Bus {
	return &Bus{
		log: log, self: selfNodeID,
		epoch:   bootEpoch(),
		subs:    map[string]map[int]func(Event){},
		seen:    map[string]*originSeen{},
		peerCap: map[string][]string{},
	}
}

// bootEpoch is a random nonzero per-process id so a peer can tell our restarted process (seq reset
// to 1) from our old one and stop dropping our fresh low-seq frames as stale duplicates.
func bootEpoch() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	e := binary.LittleEndian.Uint64(b[:])
	if e == 0 {
		e = 1 // 0 means "legacy peer, no epoch" on the wire - never collide with it
	}
	return e
}

// noteSelfLocked records our own latest outbound seq so a peer relaying our frame back is dropped
// as our own echo. Caller holds b.mu.
func (b *Bus) noteSelfLocked() {
	st := b.seen[b.self]
	if st == nil || st.epoch != b.epoch {
		st = &originSeen{epoch: b.epoch}
		b.seen[b.self] = st
	}
	st.seq = b.seq
}

// SetTransport wires the peer link. broadcast sends a ChanBus payload to all peers; sendTo to one.
// Pass nil,nil to detach (go local-only). Re-advertises local capabilities after wiring.
func (b *Bus) SetTransport(broadcast func(payload []byte), sendTo func(nodeID string, payload []byte)) {
	b.mu.Lock()
	b.broadcast, b.sendTo = broadcast, sendTo
	b.mu.Unlock()
	b.Advertise()
}

// Subscribe registers fn for a topic; returns an unsubscribe func. fn runs on the publisher's
// goroutine - keep it quick or hand off to a channel.
func (b *Bus) Subscribe(topic string, fn func(Event)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[topic] == nil {
		b.subs[topic] = map[int]func(Event){}
	}
	id := b.nextID
	b.nextID++
	b.subs[topic][id] = fn
	return func() {
		b.mu.Lock()
		if m := b.subs[topic]; m != nil {
			delete(m, id)
			if len(m) == 0 {
				delete(b.subs, topic)
			}
		}
		b.mu.Unlock()
	}
}

// Publish produces an event on this node: fans out to local subscribers + broadcasts to peers.
func (b *Bus) Publish(topic string, data json.RawMessage) {
	b.mu.Lock()
	b.seq++
	b.nPub++
	env := Envelope{Topic: topic, Origin: b.self, Epoch: b.epoch, Seq: b.seq, Data: data}
	b.noteSelfLocked() // drop our own echo if a peer relays it back
	bc := b.broadcast
	b.mu.Unlock()

	b.fanout(Event{Topic: topic, Origin: b.self, Local: true, Data: data})
	if bc != nil {
		if raw, err := json.Marshal(env); err == nil {
			bc(raw)
		}
	}
}

// maxRetiredEpochs caps the per-origin superseded-epoch memory (bounds a peer's restart history).
const maxRetiredEpochs = 16

// acceptLocked decides whether a frame (origin,epoch,seq) is fresh, updating dedup state. Caller
// holds b.mu. Epoch 0 = a legacy peer with no boot id: single-epoch, seq-only dedup (old behaviour).
//   - first frame from an origin → accept.
//   - same epoch → accept iff seq strictly advances (drops dups + reorder).
//   - a superseded (retired) epoch → drop: a straggler from the peer's previous process.
//   - a new epoch → the peer restarted: retire the old epoch and adopt the new one (accept), so its
//     reset-to-1 seq is no longer dropped as stale. This is the fix for one-way silence after a
//     restart, where a long-running peer held a stale-high last-seq for us.
func (b *Bus) acceptLocked(origin string, epoch, seq uint64) bool {
	// Our own current epoch is authoritative: a peer relaying back a frame from our previous process
	// (foreign epoch, origin==self) is always a stale echo - never let it flip our self-dedup state.
	if origin == b.self && epoch != b.epoch {
		return false
	}
	st := b.seen[origin]
	if st == nil {
		b.seen[origin] = &originSeen{epoch: epoch, seq: seq}
		return true
	}
	if epoch == st.epoch {
		if seq <= st.seq {
			return false
		}
		st.seq = seq
		return true
	}
	if slices.Contains(st.retired, epoch) {
		return false // straggler from an already-superseded process
	}
	st.retired = append(st.retired, st.epoch)
	if len(st.retired) > maxRetiredEpochs {
		st.retired = st.retired[len(st.retired)-maxRetiredEpochs:]
	}
	st.epoch, st.seq = epoch, seq
	return true
}

// Inbound feeds a ChanBus payload received from a peer. Dedups by (origin,epoch,seq), fans out
// locally, and relays to other peers for transitive delivery. Wire this to the peerlink data handler.
func (b *Bus) Inbound(peerNodeID string, payload []byte) {
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil || env.Origin == "" {
		return
	}
	b.mu.Lock()
	if !b.acceptLocked(env.Origin, env.Epoch, env.Seq) {
		b.nDup++
		b.mu.Unlock()
		return // duplicate / out-of-order / superseded-epoch straggler
	}
	b.nInbound++
	bc := b.broadcast
	if env.Topic == TopicCaps {
		b.peerCap[env.Origin] = append([]string(nil), env.Caps...)
	}
	b.mu.Unlock()

	if env.Topic == TopicCaps {
		b.log.Info(logTag, "peer capabilities", map[string]any{"node": env.Origin, "caps": env.Caps})
	} else {
		b.fanout(Event{Topic: env.Topic, Origin: env.Origin, Local: false, Data: env.Data})
	}
	// Relay onward (dedup at every hop prevents loops; the original sender drops its own echo).
	if bc != nil {
		b.mu.Lock()
		b.nRelayed++
		b.mu.Unlock()
		bc(payload)
	}
}

// Stats is the perfmon probe body: cumulative publish-path counters + subscriber count.
func (b *Bus) Stats() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := 0
	for _, m := range b.subs {
		subs += len(m)
	}
	return fmt.Sprintf("published=%d inbound=%d dupDropped=%d relayed=%d topics=%d subscribers=%d",
		b.nPub, b.nInbound, b.nDup, b.nRelayed, len(b.subs), subs)
}

// SetLocalCaps replaces the full capability list this node owns, then advertises.
func (b *Bus) SetLocalCaps(caps ...string) {
	b.mu.Lock()
	b.localCap = append([]string(nil), caps...)
	b.mu.Unlock()
	b.Advertise()
}

// AddCap adds one capability (idempotent) + advertises. Use this per-feature so independent
// features (twitch, vr, midi) don't clobber each other's advertisements.
func (b *Bus) AddCap(capability string) {
	b.mu.Lock()
	if slices.Contains(b.localCap, capability) {
		b.mu.Unlock()
		return
	}
	b.localCap = append(b.localCap, capability)
	b.mu.Unlock()
	b.Advertise()
}

// RemoveCap drops one capability + advertises.
func (b *Bus) RemoveCap(capability string) {
	b.mu.Lock()
	if i := slices.Index(b.localCap, capability); i >= 0 {
		b.localCap = append(b.localCap[:i], b.localCap[i+1:]...)
	} else {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	b.Advertise()
}

// Advertise broadcasts this node's capability list (called on cap change + peer-state change).
func (b *Bus) Advertise() {
	b.mu.Lock()
	caps := append([]string(nil), b.localCap...)
	bc := b.broadcast
	if bc == nil {
		b.mu.Unlock()
		return
	}
	b.seq++
	env := Envelope{Topic: TopicCaps, Origin: b.self, Epoch: b.epoch, Seq: b.seq, Caps: caps}
	b.noteSelfLocked()
	b.mu.Unlock()
	if raw, err := json.Marshal(env); err == nil {
		bc(raw)
	}
}

// Owners returns the node ids of peers advertising the given capability.
func (b *Bus) Owners(capability string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for node, caps := range b.peerCap {
		if slices.Contains(caps, capability) {
			out = append(out, node)
		}
	}
	sort.Strings(out)
	return out
}

// HasLocal reports whether this node owns the capability.
func (b *Bus) HasLocal(capability string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Contains(b.localCap, capability)
}

// PeerCaps returns a snapshot of peer node id → advertised capabilities (for the UI).
func (b *Bus) PeerCaps() map[string][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string][]string, len(b.peerCap))
	for k, v := range b.peerCap {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// SendToCapability routes a directed command to the peer(s) owning capability. Used when a local
// surface (e.g. a VR overlay's "send chat") must act through a remote owner. Returns the number of
// peers it was sent to.
func (b *Bus) SendToCapability(capability, topic string, data json.RawMessage) int {
	owners := b.Owners(capability)
	b.mu.Lock()
	b.seq++
	env := Envelope{Topic: topic, Origin: b.self, Epoch: b.epoch, Seq: b.seq, Data: data}
	b.noteSelfLocked()
	send := b.sendTo
	b.mu.Unlock()
	if send == nil {
		return 0
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return 0
	}
	n := 0
	for _, node := range owners {
		send(node, raw)
		n++
	}
	return n
}

// fanout delivers an event to local subscribers of its topic.
func (b *Bus) fanout(ev Event) {
	b.mu.Lock()
	hs := make([]func(Event), 0, len(b.subs[ev.Topic]))
	for _, fn := range b.subs[ev.Topic] {
		hs = append(hs, fn)
	}
	b.mu.Unlock()
	for _, fn := range hs {
		fn(ev)
	}
}
