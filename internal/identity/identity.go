// Package identity is this rave-mate instance's stable, long-term node identity: an
// Ed25519 keypair generated once and persisted, plus a NodeID derived from the public key.
// The LAN peer link (internal/peerlink) signs handshake transcripts with this key so a
// remembered peer can be re-authenticated on reconnect without re-pairing.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"

	"rave.page/mate/internal/shared/secureseal"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/wirecrypto"
)

const storeKey = "self"

// Identity is the node's long-term signing key + derived id.
type Identity struct {
	NodeID string             // EncB64url(sha256(pub)) - stable, shareable node id
	Pub    ed25519.PublicKey  // verify key (advertised + sent in handshakes)
	priv   ed25519.PrivateKey // sign key (never leaves the process)
}

// persisted is the on-disk shape in the bbolt identity bucket.
type persisted struct {
	Seed   []byte `json:"seed"`   // 32-byte Ed25519 seed, sealed iff Sealed
	Sealed bool   `json:"sealed"` // true → Seed is secureseal-encrypted (Windows DPAPI)
}

// NodeIDFromPub derives the stable node id from a public key.
func NodeIDFromPub(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return wirecrypto.EncB64url(sum[:])
}

// LoadOrCreate returns the persisted node identity, generating + storing a new one on
// first run. The 32-byte seed is sealed at rest via secureseal where available (Windows
// DPAPI); elsewhere it's stored raw in the 0600 bbolt file - acceptable for a local,
// re-pairable key (unlike a bearer token). A nil store yields an ephemeral identity that
// is NOT persisted (degraded mode; peers won't survive a restart) - callers should log it.
func LoadOrCreate(st *store.Store) (*Identity, error) {
	if st == nil {
		return generate()
	}
	var p persisted
	ok, err := st.GetJSON(store.BucketIdentity, storeKey, &p)
	if err != nil {
		return nil, fmt.Errorf("identity: read store: %w", err)
	}
	if ok {
		seed := p.Seed
		if p.Sealed {
			seed, err = secureseal.Unseal(p.Seed)
			if err != nil {
				return nil, fmt.Errorf("identity: unseal seed: %w", err)
			}
		}
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("identity: bad seed length %d", len(seed))
		}
		return fromSeed(seed), nil
	}

	// First run: generate + persist.
	id, err := generate()
	if err != nil {
		return nil, err
	}
	seed := id.priv.Seed()
	rec := persisted{Seed: seed}
	if secureseal.Available() {
		if sealed, e := secureseal.Seal(seed); e == nil {
			rec.Seed, rec.Sealed = sealed, true
		}
		// On seal failure fall through with the raw seed - better a stored-raw key than
		// regenerating (which would void remembered-peer trust) every launch.
	}
	if err := st.PutJSON(store.BucketIdentity, storeKey, rec); err != nil {
		return nil, fmt.Errorf("identity: persist: %w", err)
	}
	return id, nil
}

func generate() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate: %w", err)
	}
	return fromSeed(priv.Seed()), nil
}

func fromSeed(seed []byte) *Identity {
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	return &Identity{NodeID: NodeIDFromPub(pub), Pub: pub, priv: priv}
}

// Sign signs msg with the node's private key.
func (i *Identity) Sign(msg []byte) []byte { return ed25519.Sign(i.priv, msg) }

// PubFingerprint is EncB64url(sha256(pub)) - identical to NodeID (kept explicit for the
// discovery TXT record / forward compat).
func (i *Identity) PubFingerprint() string { return i.NodeID }

// VerifyPeer reports whether sig is a valid signature of msg under a peer's public key.
func VerifyPeer(pub ed25519.PublicKey, msg, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, msg, sig)
}
