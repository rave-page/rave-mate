// Package peers persists remembered LAN peers (those paired via the SAS check) so a
// rediscovered trusted peer reconnects without a fresh pairing. Backed by the bbolt store
// (BucketPeers, node-id-keyed) - app infrastructure, not library data.
package peers

import (
	"crypto/ed25519"
	"encoding/json"
	"time"

	"rave.page/mate/internal/store"
)

// Transfer-type planes an encryption opt-out can target (keys of Peer.EncOff).
const (
	PlaneControl = "control" // the peerlink control plane (now-playing, MIDI, remote control, bus)
	PlaneFiles   = "files"   // the filexfer data plane
	PlaneMedia   = "media"   // the medialink A/V data plane
)

// Peer is a remembered peer.
type Peer struct {
	NodeID      string            `json:"nodeId"`
	IdentityPub ed25519.PublicKey `json:"identityPub"` // the key SAS-verified at pairing
	Nickname    string            `json:"nickname"`
	LastAddress string            `json:"lastAddress"` // host:port last seen/connected
	LastSeen    time.Time         `json:"lastSeen"`
	PairedAt    time.Time         `json:"pairedAt"`
	Trusted     bool              `json:"trusted"`
	// EncOff is the per-transfer-type encryption opt-out (keys: PlaneControl/PlaneFiles/
	// PlaneMedia). Encryption is DEFAULT ON: a plane absent (or false) means encrypted. A wire
	// opt-out takes effect only when BOTH ends set it, so a lone true here never downgrades.
	EncOff map[string]bool `json:"encOff,omitempty"`
}

// Store is the remembered-peers persistence facade over the bbolt store.
type Store struct {
	st *store.Store
}

// New wraps the bbolt store. A nil store yields a Store whose methods are safe no-ops.
func New(st *store.Store) *Store { return &Store{st: st} }

// List returns every remembered peer.
func (s *Store) List() ([]Peer, error) {
	if s == nil || s.st == nil {
		return nil, nil
	}
	raws, err := s.st.ListJSON(store.BucketPeers)
	if err != nil {
		return nil, err
	}
	out := make([]Peer, 0, len(raws))
	for _, raw := range raws {
		var p Peer
		if json.Unmarshal(raw, &p) == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// Get returns the remembered peer for nodeID, ok=false if absent.
func (s *Store) Get(nodeID string) (Peer, bool) {
	if s == nil || s.st == nil {
		return Peer{}, false
	}
	var p Peer
	ok, err := s.st.GetJSON(store.BucketPeers, nodeID, &p)
	if err != nil || !ok {
		return Peer{}, false
	}
	return p, true
}

// TrustedKey returns a trusted peer's identity key (for peerlink.TrustLookup).
func (s *Store) TrustedKey(nodeID string) (ed25519.PublicKey, bool) {
	p, ok := s.Get(nodeID)
	if !ok || !p.Trusted || len(p.IdentityPub) != ed25519.PublicKeySize {
		return nil, false
	}
	return p.IdentityPub, true
}

// Save upserts a peer.
func (s *Store) Save(p Peer) error {
	if s == nil || s.st == nil {
		return nil
	}
	return s.st.PutJSON(store.BucketPeers, p.NodeID, p)
}

// EncOff reports the per-plane encryption opt-out for a remembered peer. Default (unknown peer,
// no map, or absent plane) is false = encrypted.
func (s *Store) EncOff(nodeID, plane string) bool {
	p, ok := s.Get(nodeID)
	if !ok {
		return false
	}
	return p.EncOff[plane]
}

// SetEncOff records a peer's opt-out for one plane (read-modify-write). No-op on an unknown peer
// (opt-outs are offered only for remembered peers), so it never creates a bare entry.
func (s *Store) SetEncOff(nodeID, plane string, off bool) error {
	if s == nil || s.st == nil {
		return nil
	}
	p, ok := s.Get(nodeID)
	if !ok {
		return nil
	}
	if p.EncOff == nil {
		if !off {
			return nil // nothing to clear
		}
		p.EncOff = map[string]bool{}
	}
	if off {
		p.EncOff[plane] = true
	} else {
		delete(p.EncOff, plane)
	}
	if len(p.EncOff) == 0 {
		p.EncOff = nil
	}
	return s.Save(p)
}

// Forget removes a remembered peer.
func (s *Store) Forget(nodeID string) error {
	if s == nil || s.st == nil {
		return nil
	}
	return s.st.Delete(store.BucketPeers, nodeID)
}
