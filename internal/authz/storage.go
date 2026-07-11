package authz

import (
	"encoding/json"
	"fmt"

	"rave.page/mate/internal/shared/secureseal"
	"rave.page/mate/internal/store"
)

// Store keys inside store.BucketAuthz.
//
//	enrolment     - this instance's TOTP secret (SEALED; memory-only where sealing is absent)
//	self          - this instance's advertised gate id
//	tok:<sha256>  - a token WE issued to a caller. Hash + metadata only - not a secret, so it
//	                is always persisted in the clear; there is nothing presentable to steal.
//	peer:<nodeId> - a token ANOTHER instance issued to US. A bearer secret HERE, so it is
//	                SEALED (memory-only where sealing is absent).
const (
	keyEnrolment = "enrolment"
	keySelf      = "self"
	prefixToken  = "tok:"
	prefixPeer   = "peer:"
)

// Sealer is the at-rest crypto seam for the two things in this package that are genuinely
// secret: the TOTP enrolment secret, and tokens minted for us by OTHER instances.
//
// Default = the OS secret store (Windows DPAPI). Where no OS secret API exists (macOS and
// Linux today - see shared/secureseal), Available() is false and the gate keeps those secrets
// IN MEMORY for the process lifetime instead: degraded (re-enrol after a restart) but never a
// bearer secret sitting in plaintext on disk. Same policy shared/auth applies to tokens.
type Sealer interface {
	Seal(plain []byte) ([]byte, error)
	Unseal(blob []byte) ([]byte, error)
	Available() bool
}

// osSealer is the production Sealer.
type osSealer struct{}

func (osSealer) Seal(p []byte) ([]byte, error)   { return secureseal.Seal(p) }
func (osSealer) Unseal(b []byte) ([]byte, error) { return secureseal.Unseal(b) }
func (osSealer) Available() bool                 { return secureseal.Available() }

// Persistent reports whether secrets survive a restart on this platform. False → the UI must
// tell the user the enrolment is session-scoped (see the settings surface).
func (g *Gate) Persistent() bool { return g.sealer.Available() && g.st != nil }

// memMode: keep secrets in RAM for this process only - either the OS offers no secret store,
// or no bbolt store was opened. Never a reason to write a bearer secret in the clear.
func (g *Gate) memMode() bool { return !g.sealer.Available() || g.st == nil }

// ── enrolment (TOTP secret) ──────────────────────────────────────────────────

// enrolment reads the authenticator record: from the store when sealing works, else from the
// process-lifetime memory fallback.
func (g *Gate) enrolment() (enrolment, bool) {
	if g.memMode() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.memEnrol == nil {
			return enrolment{}, false
		}
		return *g.memEnrol, true
	}
	var e enrolment
	ok, err := g.st.GetJSON(store.BucketAuthz, keyEnrolment, &e)
	if err != nil || !ok {
		return enrolment{}, false
	}
	return e, true
}

func (g *Gate) putEnrolment(e enrolment) error {
	if g.memMode() {
		g.mu.Lock()
		cp := e
		g.memEnrol = &cp
		g.mu.Unlock()
		return nil
	}
	return g.st.PutJSON(store.BucketAuthz, keyEnrolment, e)
}

func (g *Gate) delEnrolment() error {
	g.mu.Lock()
	g.memEnrol = nil
	g.mu.Unlock()
	if g.st == nil {
		return nil
	}
	return g.st.Delete(store.BucketAuthz, keyEnrolment)
}

// sealSecret encrypts the TOTP secret for the store. Only called when sealing is available.
func (g *Gate) sealSecret(secret string) ([]byte, error) {
	blob, err := g.sealer.Seal([]byte(secret))
	if err != nil {
		return nil, fmt.Errorf("authz: seal secret: %w", err)
	}
	return blob, nil
}

// unsealSecret returns the plaintext base32 TOTP secret. NEVER log the result.
func (g *Gate) unsealSecret(e enrolment) (string, error) {
	if !e.Sealed {
		// Memory-fallback records carry the secret verbatim and are never written to disk.
		if !g.sealer.Available() {
			return string(e.Secret), nil
		}
		// Sealing works, yet this record is unsealed → a downgraded or hand-edited store.
		// Refuse it rather than trust a plaintext secret we never would have written.
		return "", ErrUnsealed
	}
	raw, err := g.sealer.Unseal(e.Secret)
	if err != nil {
		return "", fmt.Errorf("authz: unseal secret: %w", err)
	}
	return string(raw), nil
}

// ── advertised id ────────────────────────────────────────────────────────────

// selfID is this gate's advertised id - the Ed25519 node id, pinned by SetSelfID. Sent in the
// challenge so a caller can look up the token it stored for US.
func (g *Gate) selfID() string {
	g.mu.Lock()
	id := g.self
	g.mu.Unlock()
	if id != "" || g.st == nil {
		return id
	}
	var persisted string
	if ok, err := g.st.GetJSON(store.BucketAuthz, keySelf, &persisted); err == nil && ok {
		return persisted
	}
	return ""
}

// SetSelfID pins the gate's advertised id to the node identity, so the challenge's nodeId is
// the same id the peer just authenticated over Ed25519. Call once at construction.
func (g *Gate) SetSelfID(id string) {
	if id == "" {
		return
	}
	g.mu.Lock()
	g.self = id
	g.mu.Unlock()
	if g.st != nil {
		_ = g.st.PutJSON(store.BucketAuthz, keySelf, id)
	}
}

// ── tokens we issued (hash + metadata; not secret) ───────────────────────────

func (g *Gate) tokens() ([]trustedToken, error) {
	if g.st == nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		out := make([]trustedToken, 0, len(g.memTokens))
		for _, t := range g.memTokens {
			out = append(out, t)
		}
		return out, nil
	}
	raws, err := g.st.ListJSON(store.BucketAuthz)
	if err != nil {
		return nil, err
	}
	out := make([]trustedToken, 0, len(raws))
	for _, raw := range raws {
		var t trustedToken
		// The bucket also holds the enrolment + peer tokens + self id; a record without both a
		// Hash and a PeerID isn't one of ours.
		if json.Unmarshal(raw, &t) == nil && t.Hash != "" && t.PeerID != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

func (g *Gate) putToken(t trustedToken) error {
	if g.st == nil {
		g.mu.Lock()
		g.memTokens[t.Hash] = t
		g.mu.Unlock()
		return nil
	}
	return g.st.PutJSON(store.BucketAuthz, prefixToken+t.Hash, t)
}

func (g *Gate) revokeHash(hash string) error {
	if g.st == nil {
		g.mu.Lock()
		delete(g.memTokens, hash)
		g.mu.Unlock()
		return nil
	}
	return g.st.Delete(store.BucketAuthz, prefixToken+hash)
}

// ── tokens another instance issued to us (bearer secrets here → sealed) ──────

// sealedPeerToken is the at-rest shape of a token we hold FOR a remote instance.
type sealedPeerToken struct {
	Blob   []byte `json:"blob"`   // the token, sealed
	Sealed bool   `json:"sealed"` // always true on disk - we never write it otherwise
}

func (g *Gate) peerToken(peerNodeID string) (string, bool) {
	if peerNodeID == "" {
		return "", false
	}
	if g.memMode() {
		g.mu.Lock()
		defer g.mu.Unlock()
		tok, ok := g.memPeer[peerNodeID]
		return tok, ok
	}
	var rec sealedPeerToken
	ok, err := g.st.GetJSON(store.BucketAuthz, prefixPeer+peerNodeID, &rec)
	if err != nil || !ok || !rec.Sealed {
		return "", false
	}
	raw, err := g.sealer.Unseal(rec.Blob)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// savePeerToken stores a token a remote instance minted for us: sealed on disk where the OS
// offers a secret store, in memory otherwise. Never plaintext on disk.
func (g *Gate) savePeerToken(peerNodeID, tok string) error {
	if peerNodeID == "" {
		return fmt.Errorf("authz: no peer id")
	}
	if g.memMode() {
		g.mu.Lock()
		g.memPeer[peerNodeID] = tok
		g.mu.Unlock()
		return nil
	}
	blob, err := g.sealer.Seal([]byte(tok))
	if err != nil {
		return fmt.Errorf("authz: seal peer token: %w", err)
	}
	return g.st.PutJSON(store.BucketAuthz, prefixPeer+peerNodeID, sealedPeerToken{Blob: blob, Sealed: true})
}

func (g *Gate) forgetPeerToken(peerNodeID string) error {
	g.mu.Lock()
	delete(g.memPeer, peerNodeID)
	g.mu.Unlock()
	if g.st == nil {
		return nil
	}
	return g.st.Delete(store.BucketAuthz, prefixPeer+peerNodeID)
}

// HasPeerToken reports whether we can reach peerNodeID without asking the user for a code.
// Drives the UI ("Connect" vs "Connect (needs a code)").
func (g *Gate) HasPeerToken(peerNodeID string) bool {
	_, ok := g.peerToken(peerNodeID)
	return ok
}

// ForgetPeer drops the token we hold for a remote instance - the local half of "unpair".
func (g *Gate) ForgetPeer(peerNodeID string) error { return g.forgetPeerToken(peerNodeID) }
