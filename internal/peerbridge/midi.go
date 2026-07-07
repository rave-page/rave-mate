package peerbridge

import (
	"encoding/json"
	"sync"

	"rave.page/mate/internal/peerlink"
)

// MIDIMsg is a single bridged MIDI channel message (3 status/data bytes).
type MIDIMsg struct {
	S  byte `json:"s"`
	D1 byte `json:"d1"`
	D2 byte `json:"d2"`
}

// midiState gates outbound MIDI forwarding + the current control target. Off until the user
// activates the "control the other PC" context. mesh is the independent always-on fan-out
// (mirror local MIDI to every connected peer), persisted via config MIDIFeature.MeshForward.
type midiState struct {
	mu     sync.RWMutex
	on     bool
	mesh   bool
	target string // peer node id; "" = all connected peers
}

// SetMIDIForwarding enables/disables forwarding local MIDI to the target peer(s).
func (b *Bridge) SetMIDIForwarding(on bool) {
	b.midi.mu.Lock()
	b.midi.on = on
	b.midi.mu.Unlock()
}

// SetMIDIMesh enables/disables mesh forwarding (broadcast local MIDI to all connected peers,
// independent of the directed control target).
func (b *Bridge) SetMIDIMesh(on bool) {
	b.midi.mu.Lock()
	b.midi.mesh = on
	b.midi.mu.Unlock()
}

// MIDIMesh reports whether mesh forwarding is on.
func (b *Bridge) MIDIMesh() bool {
	b.midi.mu.RLock()
	defer b.midi.mu.RUnlock()
	return b.midi.mesh
}

// SetControlTarget sets which peer receives forwarded MIDI/control ("" = broadcast).
func (b *Bridge) SetControlTarget(nodeID string) {
	b.midi.mu.Lock()
	b.midi.target = nodeID
	b.midi.mu.Unlock()
}

// Forwarding reports whether MIDI is currently being forwarded, and to whom.
func (b *Bridge) Forwarding() (on bool, target string) {
	b.midi.mu.RLock()
	defer b.midi.mu.RUnlock()
	return b.midi.on, b.midi.target
}

// ForwardMIDI sends a local MIDI message to peers: mesh broadcasts to all connected peers
// (regardless of the directed target); otherwise the directed control target when forwarding
// is on. Wired to the midisrc tap, which fires ONLY for locally-sourced port messages -
// peer-injected MIDI bypasses the tap (midisrc pump), so a bridged-in message is never
// re-forwarded (no mesh loops). A no-op when both gates are off, so the hot path stays cheap.
func (b *Bridge) ForwardMIDI(status, d1, d2 byte) {
	b.midi.mu.RLock()
	on, mesh, target := b.midi.on, b.midi.mesh, b.midi.target
	b.midi.mu.RUnlock()
	if !on && !mesh {
		return
	}
	raw, err := json.Marshal(MIDIMsg{S: status, D1: d1, D2: d2})
	if err != nil {
		return
	}
	// mesh (or directed-with-no-target) fans out; a broadcast covers the directed target too.
	if mesh || target == "" {
		b.mgr.Broadcast(peerlink.ChanMIDI, raw)
		return
	}
	_ = b.mgr.SendTo(target, peerlink.ChanMIDI, raw)
}
