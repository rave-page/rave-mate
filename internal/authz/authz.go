// Package authz is the transport-agnostic authorization gate for reaching THIS instance
// from somewhere else.
//
// The trust root is the instance, never rave.page. The gate runs over any bidirectional
// framed Channel - the account-bridge relay today, a LAN peer link, a future direct
// ip:port dial with no rave.page account at all. rave.page is ONE transport plugin; it is
// deliberately not a party to the decision, and nothing here calls it.
//
// Two credentials, in order of preference:
//
//	token - a random 256-bit pairwise secret this instance MINTED for one caller after it
//	        passed a TOTP challenge. Held by both ends; stored HASHED here (a store leak
//	        yields no usable token), sealed at rest on the caller. Refreshed on every use,
//	        hard-expired after IdleExpiry of disuse, revocable from the UI.
//	totp  - a 6-digit RFC 6238 code from the authenticator the user enrolled against this
//	        instance. The bootstrap: proving possession of the enrolled authenticator IS
//	        the pairing authorization (the SAS-compare equivalent for a link with no human
//	        at the far end), after which the peer's Ed25519 identity is pinned and a token
//	        is issued so TOTP isn't needed again.
//
// SECURITY PRECONDITION - the caller MUST run the gate over a Channel that is already
// confidential and bound to the peer's verified identity (peerlink does this: Ed25519-authed
// ECDH, then an AEAD upgrade). A bearer credential on a cleartext channel would be handed
// straight to the relay operator. See bridge.Conn.Upgrade.
//
// Never log a secret, a code, or a token.
package authz

import (
	"context"
	"errors"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/totp"
)

const logTag = "authz"

const (
	// IdleExpiry hard-expires a trusted-session token after this long without use. A stolen
	// token therefore has a bounded life even if the theft is never noticed.
	IdleExpiry = 7 * 24 * time.Hour
	// tokenBytes is the minted token's entropy. 256 bits - unguessable, and it never has to
	// be typed by a human.
	tokenBytes = 32

	// maxFails / lockoutBase throttle TOTP guessing. A 6-digit code is 10^6 wide and stays
	// valid ~90s across the skew window, so an unthrottled verifier is brute-forceable by
	// anyone who can reach the channel. After maxFails consecutive failures the caller is
	// locked out for lockoutBase, doubling each further failure up to lockoutMax.
	maxFails    = 5
	lockoutBase = 30 * time.Second
	lockoutMax  = 30 * time.Minute

	// gateTimeout bounds one challenge/response exchange - a peer that opens a channel and
	// then stalls must not pin a goroutine forever.
	gateTimeout = 30 * time.Second
	// maxFrame bounds a gate frame. The protocol carries a code, a token and short strings;
	// anything larger is a malformed or hostile peer.
	maxFrame = 4 << 10
)

// Errors surfaced to callers. Deliberately coarse on the wire (see failReason) so a prober
// can't distinguish "no such peer" from "wrong code".
var (
	ErrNotEnrolled = errors.New("authz: no authenticator enrolled on this instance")
	ErrDenied      = errors.New("authz: credential rejected")
	ErrLockedOut   = errors.New("authz: too many failed attempts; locked out")
	ErrNoCred      = errors.New("authz: no credential available for this peer")
	ErrUnsealed    = errors.New("authz: no OS secure store; refusing to persist secrets in the clear")
)

// Channel is any bidirectional framed transport the gate can run over. peerlink.Conn and
// bridge.Conn both satisfy it; so would a raw TLS socket on a direct dial. The gate never
// learns which - that's the whole point.
type Channel interface {
	Send(ctx context.Context, b []byte) error
	Recv(ctx context.Context) ([]byte, error)
}

// Transport names the medium a grant was made over. Recorded for the UI ("trusted via
// rave.page bridge" vs "on the LAN") and for revocation; it never influences the decision.
type Transport string

const (
	TransportBridge Transport = "bridge" // rave.page account relay (WAN)
	TransportLAN    Transport = "lan"    // direct LAN peer link
	TransportDirect Transport = "direct" // future: direct ip:port dial, no rave.page account
)

// Grant is a completed authorization.
type Grant struct {
	PeerID    string    // the caller's stable id (peerlink node id, or a browser session id)
	Transport Transport // how it reached us
	Method    string    // "token" | "totp" - how it proved itself
	Token     string    // the token to present next time (issued/refreshed); NEVER log
	ExpiresAt time.Time // idle expiry of that token
}

// ── enrolment (the TOTP secret) ──────────────────────────────────────────────

// enrolment is the sealed on-disk shape of this instance's authenticator secret.
type enrolment struct {
	Secret      []byte `json:"secret"`      // base32 TOTP secret, sealed iff Sealed
	Sealed      bool   `json:"sealed"`      // true → secureseal-encrypted (Windows DPAPI)
	Confirmed   bool   `json:"confirmed"`   // user typed a valid code back → enrolment proven
	CreatedAt   int64  `json:"createdAt"`   // unix
	LastCounter uint64 `json:"lastCounter"` // highest TOTP step ACCEPTED - replay defence
}

// trustedToken is the sealed on-disk record of one issued session token. The token itself is
// NOT stored - only its SHA-256 - so a store compromise yields nothing presentable.
type trustedToken struct {
	Hash      string    `json:"hash"`      // hex sha256 of the token
	PeerID    string    `json:"peerId"`    // peerlink node id / browser session id
	Label     string    `json:"label"`     // human label for the UI ("Chrome on Studio PC")
	Transport Transport `json:"transport"` // how it was granted
	IssuedAt  time.Time `json:"issuedAt"`
	LastUsed  time.Time `json:"lastUsed"` // refreshed on every successful presentation
}

// Expired reports a token past its idle horizon.
func (t trustedToken) Expired(now time.Time) bool {
	return now.Sub(t.LastUsed) > IdleExpiry
}

// Session is a UI-facing view of one trusted session. Carries no secret.
type Session struct {
	PeerID    string
	Label     string
	Transport Transport
	IssuedAt  time.Time
	LastUsed  time.Time
	ExpiresAt time.Time
}

// Gate owns the enrolment secret + the trusted-token store and renders the authorization
// verdict. Safe for concurrent use.
type Gate struct {
	st     *store.Store
	log    *logbus.Bus
	sealer Sealer

	mu     sync.Mutex
	fails  map[string]*failState // by peer id - TOTP throttle
	issuer string                // otpauth issuer ("rave-mate")
	label  string                // otpauth account label (the instance's display name)
	self   string                // our node id, advertised in the challenge (SetSelfID)

	// Process-lifetime fallbacks, used where the OS has no secret store (see Sealer) or no
	// bbolt store was opened. Bounded by the peer count, not by traffic.
	memEnrol  *enrolment
	memPeer   map[string]string       // peer node id → token they minted for us
	memTokens map[string]trustedToken // hash → token we issued
}

type failState struct {
	n     int
	until time.Time // locked out until
}

// New builds the gate over the bbolt store. A nil store yields a gate that verifies normally
// but persists nothing (degraded - everything is re-pairable after a restart).
func New(st *store.Store, log *logbus.Bus, issuer, label string) *Gate {
	return newGate(st, log, issuer, label, osSealer{})
}

// newGate is the injectable constructor (tests supply a Sealer so the sealed path is
// exercised on every platform, not only Windows).
func newGate(st *store.Store, log *logbus.Bus, issuer, label string, sealer Sealer) *Gate {
	return &Gate{
		st: st, log: log, sealer: sealer,
		fails: map[string]*failState{}, issuer: issuer, label: label,
		memPeer: map[string]string{}, memTokens: map[string]trustedToken{},
	}
}

// SetLabel updates the otpauth account label (the instance display name) for future URIs.
func (g *Gate) SetLabel(label string) {
	g.mu.Lock()
	g.label = label
	g.mu.Unlock()
}

// ── enrolment API (UI) ───────────────────────────────────────────────────────

// Enrolled reports whether an authenticator secret exists AND the user has confirmed it by
// typing back a valid code. An unconfirmed secret does NOT gate anything - it would lock the
// user out of their own instance if they mis-scanned the URI.
func (g *Gate) Enrolled() bool {
	e, ok := g.enrolment()
	return ok && e.Confirmed
}

// BeginEnrolment mints a fresh (unconfirmed) secret and returns the otpauth URI + the raw
// secret for the user to scan/type. Replaces any UNCONFIRMED secret; refuses to clobber a
// confirmed one (use Unenrol first) so a stray click can't silently rotate the gate and
// strand every paired device.
//
// The returned URI and secret are shown to the user and MUST NOT be logged or persisted
// anywhere else. Where the OS has no secret store the secret lives in memory only for this
// process (Persistent() == false) - never plaintext on disk.
func (g *Gate) BeginEnrolment() (uri, secret string, err error) {
	if e, ok := g.enrolment(); ok && e.Confirmed {
		return "", "", errors.New("authz: an authenticator is already enrolled; remove it first")
	}
	secret, err = totp.GenerateSecret()
	if err != nil {
		return "", "", err
	}
	rec := enrolment{CreatedAt: time.Now().Unix()}
	if g.sealer.Available() {
		blob, err := g.sealSecret(secret)
		if err != nil {
			return "", "", err
		}
		rec.Secret, rec.Sealed = blob, true
	} else {
		rec.Secret = []byte(secret) // memory-only fallback; putEnrolment never writes it to disk
	}
	if err := g.putEnrolment(rec); err != nil {
		return "", "", err
	}
	g.mu.Lock()
	label, issuer := g.label, g.issuer
	g.mu.Unlock()
	return totp.URI(secret, issuer, label), secret, nil
}

// ConfirmEnrolment verifies a code against the pending secret and, on success, arms the gate.
// This is the only place an enrolment becomes live.
//
// The confirming step is BURNED (LastCounter), so the same code can't then be replayed to
// authorize a pairing by someone who shoulder-surfed the enrolment screen. Consequence the UI
// must state: a pairing attempted inside that same 30s window needs the NEXT code.
func (g *Gate) ConfirmEnrolment(code string) error {
	e, ok := g.enrolment()
	if !ok {
		return ErrNotEnrolled
	}
	secret, err := g.unsealSecret(e)
	if err != nil {
		return err
	}
	ctr, valid := totp.Validate(secret, code, time.Now())
	if !valid {
		return ErrDenied
	}
	e.Confirmed = true
	e.LastCounter = ctr // burn the confirming step - it can't be replayed as an auth
	if err := g.putEnrolment(e); err != nil {
		return err
	}
	g.log.Info(logTag, "authenticator enrolled", nil) // no secret, no code
	return nil
}

// Unenrol removes the authenticator secret. Trusted tokens survive - pulling the
// authenticator must not lock the user out of devices they already paired. Revoke those
// explicitly if that's the intent.
func (g *Gate) Unenrol() error {
	if err := g.delEnrolment(); err != nil {
		return err
	}
	g.log.Info(logTag, "authenticator removed", nil)
	return nil
}

// ── trusted sessions API (UI) ────────────────────────────────────────────────

// Sessions lists live trusted sessions, dropping (and reaping) any past their idle expiry.
func (g *Gate) Sessions() []Session {
	toks, _ := g.tokens()
	now := time.Now()
	out := make([]Session, 0, len(toks))
	for _, t := range toks {
		if t.Expired(now) {
			_ = g.revokeHash(t.Hash) // lazily reap - no separate sweeper needed
			continue
		}
		out = append(out, Session{
			PeerID: t.PeerID, Label: t.Label, Transport: t.Transport,
			IssuedAt: t.IssuedAt, LastUsed: t.LastUsed, ExpiresAt: t.LastUsed.Add(IdleExpiry),
		})
	}
	return out
}

// Revoke drops every trusted session for a peer id. The peer must pass TOTP again.
func (g *Gate) Revoke(peerID string) error {
	toks, err := g.tokens()
	if err != nil {
		return err
	}
	for _, t := range toks {
		if t.PeerID == peerID {
			if err := g.revokeHash(t.Hash); err != nil {
				return err
			}
		}
	}
	g.log.Info(logTag, "trusted session revoked", map[string]any{"peer": peerID})
	return nil
}

// RevokeAll drops every trusted session on this instance.
func (g *Gate) RevokeAll() error {
	toks, err := g.tokens()
	if err != nil {
		return err
	}
	for _, t := range toks {
		if err := g.revokeHash(t.Hash); err != nil {
			return err
		}
	}
	g.log.Info(logTag, "all trusted sessions revoked", map[string]any{"count": len(toks)})
	return nil
}
