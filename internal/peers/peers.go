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

// Peer is a remembered peer.
type Peer struct {
	NodeID      string            `json:"nodeId"`
	IdentityPub ed25519.PublicKey `json:"identityPub"` // the key SAS-verified at pairing
	Nickname    string            `json:"nickname"`
	LastAddress string            `json:"lastAddress"` // host:port last seen/connected
	LastSeen    time.Time         `json:"lastSeen"`
	PairedAt    time.Time         `json:"pairedAt"`
	Trusted     bool              `json:"trusted"`
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

// Forget removes a remembered peer.
func (s *Store) Forget(nodeID string) error {
	if s == nil || s.st == nil {
		return nil
	}
	return s.st.Delete(store.BucketPeers, nodeID)
}
