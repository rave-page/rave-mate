package authz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"rave.page/mate/internal/totp"
	"rave.page/mate/internal/wirecrypto"
)

// Wire protocol. Runs over an ALREADY confidential + peer-authenticated Channel (see the
// package doc): the AEAD below us provides secrecy, integrity and replay defence, so these
// frames are plain JSON and carry no MAC of their own.
//
//	reached → caller  {"t":"authz-challenge","v":1,"methods":["token","totp"],"nodeId":"..."}
//	caller  → reached {"t":"authz-response","v":1,"method":"token","token":"..."}
//	                  {"t":"authz-response","v":1,"method":"totp","code":"123456","label":"..."}
//	reached → caller  {"t":"authz-grant","v":1,"token":"...","expiresAt":<unix ms>}
//	                  {"t":"authz-deny","v":1,"reason":"denied"}
const protocolVersion = 1

const (
	frameChallenge = "authz-challenge"
	frameResponse  = "authz-response"
	frameGrant     = "authz-grant"
	frameDeny      = "authz-deny"

	MethodToken = "token"
	MethodTOTP  = "totp"
)

type challengeFrame struct {
	T       string   `json:"t"`
	V       int      `json:"v"`
	Methods []string `json:"methods"`
	NodeID  string   `json:"nodeId"` // the REACHED instance's node id, so the caller can pick its stored token
}

type responseFrame struct {
	T      string `json:"t"`
	V      int    `json:"v"`
	Method string `json:"method"`
	Token  string `json:"token,omitempty"` // NEVER log
	Code   string `json:"code,omitempty"`  // NEVER log
	Label  string `json:"label,omitempty"` // caller's self-description, for the UI's session list
}

type grantFrame struct {
	T         string `json:"t"`
	V         int    `json:"v"`
	Token     string `json:"token"` // NEVER log
	ExpiresAt int64  `json:"expiresAt"`
}

type denyFrame struct {
	T      string `json:"t"`
	V      int    `json:"v"`
	Reason string `json:"reason"`
}

// failReason is deliberately uniform: a prober learns only "no", never WHY. Distinguishing
// "unknown peer" from "bad code" would turn the gate into an oracle.
const failReason = "denied"

// ── reached side ─────────────────────────────────────────────────────────────

// Verify runs on the instance BEING REACHED. It challenges the caller, checks the presented
// credential against the local enrolment / token store, and on success mints (or refreshes)
// the caller's pairwise token and returns the Grant.
//
// peerID is the caller's already-authenticated identity (peerlink node id) - the Channel is
// responsible for having proven it (Ed25519) before we get here. We bind the token to it, so
// a token stolen from peer A is useless when presented by peer B.
func (g *Gate) Verify(ctx context.Context, peerID string, tr Transport, ch Channel) (Grant, error) {
	ctx, cancel := context.WithTimeout(ctx, gateTimeout)
	defer cancel()

	if locked, until := g.lockedOut(peerID); locked {
		_ = sendJSON(ctx, ch, denyFrame{T: frameDeny, V: protocolVersion, Reason: failReason})
		g.log.Warn(logTag, "authorization refused: locked out", map[string]any{
			"peer": peerID, "until": until.UTC().Format(time.RFC3339),
		})
		return Grant{}, ErrLockedOut
	}

	e, enrolled := g.enrolment()
	methods := []string{MethodToken}
	if enrolled && e.Confirmed {
		methods = append(methods, MethodTOTP)
	}
	if err := sendJSON(ctx, ch, challengeFrame{
		T: frameChallenge, V: protocolVersion, Methods: methods, NodeID: g.selfID(),
	}); err != nil {
		return Grant{}, err
	}

	var resp responseFrame
	if err := recvJSON(ctx, ch, &resp); err != nil {
		return Grant{}, err
	}
	if resp.T != frameResponse || resp.V != protocolVersion {
		return Grant{}, errors.New("authz: protocol error")
	}

	grant, err := g.check(peerID, tr, resp)
	if err != nil {
		g.noteFail(peerID)
		_ = sendJSON(ctx, ch, denyFrame{T: frameDeny, V: protocolVersion, Reason: failReason})
		// Log the verdict, never the credential.
		g.log.Warn(logTag, "authorization denied", map[string]any{"peer": peerID, "method": resp.Method})
		return Grant{}, err
	}
	g.clearFails(peerID)

	if err := sendJSON(ctx, ch, grantFrame{
		T: frameGrant, V: protocolVersion, Token: grant.Token, ExpiresAt: grant.ExpiresAt.UnixMilli(),
	}); err != nil {
		return Grant{}, err
	}
	g.log.Info(logTag, "authorization granted", map[string]any{
		"peer": peerID, "method": grant.Method, "transport": string(tr),
	})
	return grant, nil
}

// check renders the verdict on a presented credential and, on success, issues/refreshes the
// caller's token. No wire I/O.
func (g *Gate) check(peerID string, tr Transport, resp responseFrame) (Grant, error) {
	switch resp.Method {
	case MethodToken:
		tok, ok := g.lookupToken(resp.Token)
		if !ok {
			return Grant{}, ErrDenied
		}
		// A token is pairwise: it authorizes exactly the peer it was minted for. Without this
		// check a token lifted from one device would authorize any other.
		if tok.PeerID != peerID {
			return Grant{}, ErrDenied
		}
		if tok.Expired(time.Now()) {
			_ = g.revokeHash(tok.Hash)
			return Grant{}, ErrDenied
		}
		// Refresh-on-use: extends the idle horizon and re-mints, so a token captured from the
		// wire (impossible under the AEAD, but defence in depth) has a short useful life.
		fresh, exp, err := g.issue(peerID, tok.Label, tr)
		if err != nil {
			return Grant{}, err
		}
		_ = g.revokeHash(tok.Hash) // one-time use: the old token dies with the handshake
		return Grant{PeerID: peerID, Transport: tr, Method: MethodToken, Token: fresh, ExpiresAt: exp}, nil

	case MethodTOTP:
		e, ok := g.enrolment()
		if !ok || !e.Confirmed {
			return Grant{}, ErrNotEnrolled
		}
		secret, err := g.unsealSecret(e)
		if err != nil {
			return Grant{}, err
		}
		ctr, valid := totp.Validate(secret, resp.Code, time.Now())
		if !valid {
			return Grant{}, ErrDenied
		}
		// Replay defence: a code is valid for up to 90s across the skew window, so the same
		// code could otherwise authorize a second, hostile channel within that window. Burn
		// the step - each counter authorizes at most one pairing, ever.
		if ctr <= e.LastCounter {
			return Grant{}, ErrDenied
		}
		e.LastCounter = ctr
		if err := g.putEnrolment(e); err != nil {
			return Grant{}, err
		}
		label := strings.TrimSpace(resp.Label)
		if len(label) > 64 {
			label = label[:64]
		}
		fresh, exp, err := g.issue(peerID, label, tr)
		if err != nil {
			return Grant{}, err
		}
		return Grant{PeerID: peerID, Transport: tr, Method: MethodTOTP, Token: fresh, ExpiresAt: exp}, nil
	}
	return Grant{}, ErrDenied
}

// ── calling side ─────────────────────────────────────────────────────────────

// CredentialFunc supplies a TOTP code for a peer we have no stored token for. The UI prompts
// the user for the code from the authenticator enrolled against THAT instance. Returning ""
// aborts the connection (user cancelled).
type CredentialFunc func(peerID string) string

// Prove runs on the CALLING side: answer the challenge with a stored token if we have one,
// else with a TOTP code from codeFn, and persist whatever token the peer mints for us.
//
// label describes us to the peer for its trusted-session list ("rave-mate on Studio PC").
func (g *Gate) Prove(ctx context.Context, ch Channel, label string, codeFn CredentialFunc) error {
	ctx, cancel := context.WithTimeout(ctx, gateTimeout)
	defer cancel()

	var chal challengeFrame
	if err := recvJSON(ctx, ch, &chal); err != nil {
		return err
	}
	if chal.T != frameChallenge || chal.V != protocolVersion {
		return errors.New("authz: protocol error")
	}

	resp := responseFrame{T: frameResponse, V: protocolVersion, Label: label}
	// The peer's node id keys OUR side of the token store - one token per instance we reach.
	if tok, ok := g.peerToken(chal.NodeID); ok && allows(chal.Methods, MethodToken) {
		resp.Method, resp.Token = MethodToken, tok
	} else {
		if !allows(chal.Methods, MethodTOTP) {
			return ErrNoCred // peer won't take TOTP and we have no token → nothing to present
		}
		if codeFn == nil {
			return ErrNoCred
		}
		code := strings.TrimSpace(codeFn(chal.NodeID))
		if code == "" {
			return ErrNoCred // user cancelled
		}
		resp.Method, resp.Code = MethodTOTP, code
	}
	if err := sendJSON(ctx, ch, resp); err != nil {
		return err
	}

	raw, err := recvFrame(ctx, ch)
	if err != nil {
		return err
	}
	var tag struct {
		T string `json:"t"`
	}
	if json.Unmarshal(raw, &tag) != nil {
		return errors.New("authz: protocol error")
	}
	if tag.T == frameDeny {
		// A stored token that the peer rejected is dead (revoked there, or expired). Drop it so
		// the next attempt falls through to TOTP instead of looping on a token that can't work.
		if resp.Method == MethodToken {
			_ = g.forgetPeerToken(chal.NodeID)
		}
		return ErrDenied
	}
	var grant grantFrame
	if json.Unmarshal(raw, &grant) != nil || grant.T != frameGrant || grant.Token == "" {
		return errors.New("authz: protocol error")
	}
	// Persist the freshly minted token for silent reconnects.
	if err := g.savePeerToken(chal.NodeID, grant.Token); err != nil {
		// Non-fatal: the link is authorized, we just can't skip TOTP next time.
		g.log.Warn(logTag, "could not persist peer token; TOTP will be required again",
			map[string]any{"peer": chal.NodeID, "error": err.Error()})
	}
	return nil
}

func allows(methods []string, m string) bool {
	for _, x := range methods {
		if x == m {
			return true
		}
	}
	return false
}

// ── token minting ────────────────────────────────────────────────────────────

// issue mints a fresh pairwise token for peerID and stores its hash. Returns the RAW token -
// the only moment it exists here; it is handed to the peer and then forgotten.
func (g *Gate) issue(peerID, label string, tr Transport) (string, time.Time, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	tok := wirecrypto.EncB64url(b)
	now := time.Now().UTC()
	rec := trustedToken{
		Hash: hashToken(tok), PeerID: peerID, Label: label, Transport: tr,
		IssuedAt: now, LastUsed: now,
	}
	if err := g.putToken(rec); err != nil {
		return "", time.Time{}, err
	}
	return tok, now.Add(IdleExpiry), nil
}

// hashToken is the at-rest form: only SHA-256(token) is ever written.
func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// lookupToken finds a stored token record by the presented raw token. The hash lookup is by
// key, so the comparison is on the (public) hash - but we still compare in constant time to
// keep the code honest if this ever moves to a scan.
func (g *Gate) lookupToken(presented string) (trustedToken, bool) {
	if presented == "" {
		return trustedToken{}, false
	}
	want := hashToken(presented)
	toks, err := g.tokens()
	if err != nil {
		return trustedToken{}, false
	}
	var found trustedToken
	hit := 0
	for _, t := range toks {
		eq := subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want))
		if eq == 1 {
			found = t
		}
		hit |= eq
	}
	return found, hit == 1
}

// ── TOTP throttle ────────────────────────────────────────────────────────────

func (g *Gate) lockedOut(peerID string) (bool, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.fails[peerID]
	if f == nil || f.until.IsZero() || time.Now().After(f.until) {
		return false, time.Time{}
	}
	return true, f.until
}

// noteFail records a failed attempt and, past maxFails, arms an exponential lockout.
func (g *Gate) noteFail(peerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.fails[peerID]
	if f == nil {
		f = &failState{}
		g.fails[peerID] = f
	}
	f.n++
	if f.n < maxFails {
		return
	}
	back := lockoutBase << min(f.n-maxFails, 16) // cap the shift before it overflows
	if back > lockoutMax || back <= 0 {
		back = lockoutMax
	}
	f.until = time.Now().Add(back)
}

func (g *Gate) clearFails(peerID string) {
	g.mu.Lock()
	delete(g.fails, peerID)
	g.mu.Unlock()
}

// ── framing ──────────────────────────────────────────────────────────────────

func sendJSON(ctx context.Context, ch Channel, v any) error {
	b, err := wirecrypto.MarshalNoHTMLEscape(v)
	if err != nil {
		return err
	}
	return ch.Send(ctx, b)
}

func recvFrame(ctx context.Context, ch Channel) ([]byte, error) {
	b, err := ch.Recv(ctx)
	if err != nil {
		return nil, err
	}
	if len(b) > maxFrame {
		return nil, errors.New("authz: frame too large")
	}
	return b, nil
}

func recvJSON(ctx context.Context, ch Channel, v any) error {
	b, err := recvFrame(ctx, ch)
	if err != nil {
		return err
	}
	if json.Unmarshal(b, v) != nil {
		return errors.New("authz: protocol error")
	}
	return nil
}
