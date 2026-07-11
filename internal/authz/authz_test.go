package authz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/totp"
)

// ── test doubles ─────────────────────────────────────────────────────────────

// memSealer is a Sealer that "seals" by tagging the blob. Lets the SEALED code path run on
// every platform (secureseal is Windows-DPAPI-only), so Linux CI exercises the same branches
// production Windows does.
type memSealer struct{ avail bool }

var sealTag = []byte("sealed:")

func (m memSealer) Available() bool { return m.avail }
func (m memSealer) Seal(p []byte) ([]byte, error) {
	if !m.avail {
		return nil, errors.New("no secure store")
	}
	return append(append([]byte{}, sealTag...), p...), nil
}
func (m memSealer) Unseal(b []byte) ([]byte, error) {
	if !m.avail || !strings.HasPrefix(string(b), string(sealTag)) {
		return nil, errors.New("not sealed")
	}
	return b[len(sealTag):], nil
}

// pipeChan is an in-memory authz.Channel pair - the "any transport" the gate is meant to run
// over. Proves the seam: no relay, no sockets, no rave.page.
type pipeChan struct {
	in  chan []byte
	out chan []byte
}

func newPipePair() (a, b *pipeChan) {
	x, y := make(chan []byte, 8), make(chan []byte, 8)
	return &pipeChan{in: x, out: y}, &pipeChan{in: y, out: x}
}

func (p *pipeChan) Send(ctx context.Context, b []byte) error {
	select {
	case p.out <- append([]byte{}, b...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pipeChan) Recv(ctx context.Context) ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// newTestGate builds a store-less gate (memory fallbacks) with a working sealer.
func newTestGate(t *testing.T, sealed bool) *Gate {
	t.Helper()
	return newGate(nil, logbus.New(64), "rave-mate", "Test PC", memSealer{avail: sealed})
}

// enrol runs the full enrolment: mint → confirm with a real code.
func enrol(t *testing.T, g *Gate) string {
	t.Helper()
	_, secret, err := g.BeginEnrolment()
	if err != nil {
		t.Fatalf("BeginEnrolment: %v", err)
	}
	if g.Enrolled() {
		t.Fatal("gate is live before the user confirmed the code - a mis-scan would lock them out")
	}
	code, err := totp.CodeAt(secret, time.Now())
	if err != nil {
		t.Fatalf("CodeAt: %v", err)
	}
	if err := g.ConfirmEnrolment(code); err != nil {
		t.Fatalf("ConfirmEnrolment: %v", err)
	}
	if !g.Enrolled() {
		t.Fatal("gate not live after confirmation")
	}
	return secret
}

// nextCode returns a code for the NEXT time step. ConfirmEnrolment burns the step it was
// confirmed with (replay defence), so a pairing inside that same 30s window must use the next
// code - which the ±1 skew still accepts. Mirrors what a user does moments after enrolling.
func nextCode(t *testing.T, secret string) string {
	t.Helper()
	c, err := totp.CodeAt(secret, time.Now().Add(totp.StepSeconds*time.Second))
	if err != nil {
		t.Fatalf("CodeAt: %v", err)
	}
	return c
}

// runGate drives Verify (reached) and Prove (caller) concurrently over a pipe, as a real
// transport would. Returns the reached side's grant + each side's error.
func runGate(t *testing.T, reached, caller *Gate, peerID string, codeFn CredentialFunc) (Grant, error, error) {
	t.Helper()
	rc, cc := newPipePair()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		wg          sync.WaitGroup
		grant       Grant
		vErr, pErr  error
		callerLabel = "rave-mate on Caller PC"
	)
	wg.Add(2)
	go func() { defer wg.Done(); grant, vErr = reached.Verify(ctx, peerID, TransportBridge, rc) }()
	go func() { defer wg.Done(); pErr = caller.Prove(ctx, cc, callerLabel, codeFn) }()
	wg.Wait()
	return grant, vErr, pErr
}

// ── enrolment ────────────────────────────────────────────────────────────────

func TestEnrolmentRequiresConfirmation(t *testing.T) {
	g := newTestGate(t, true)
	_, secret, err := g.BeginEnrolment()
	if err != nil {
		t.Fatalf("BeginEnrolment: %v", err)
	}
	if g.Enrolled() {
		t.Fatal("unconfirmed enrolment must not arm the gate")
	}
	if err := g.ConfirmEnrolment("000000"); !errors.Is(err, ErrDenied) {
		t.Errorf("bad confirmation code: err = %v, want ErrDenied", err)
	}
	if g.Enrolled() {
		t.Fatal("failed confirmation armed the gate")
	}
	code, _ := totp.CodeAt(secret, time.Now())
	if err := g.ConfirmEnrolment(code); err != nil {
		t.Fatalf("ConfirmEnrolment: %v", err)
	}
	if !g.Enrolled() {
		t.Fatal("gate not armed after a valid confirmation")
	}
}

func TestBeginEnrolmentRefusesToClobberConfirmed(t *testing.T) {
	g := newTestGate(t, true)
	enrol(t, g)
	if _, _, err := g.BeginEnrolment(); err == nil {
		t.Fatal("re-enrolling over a confirmed authenticator must fail - it would strand every paired device")
	}
}

// TestEnrolmentWithoutSecureStore: no OS secret store → the secret is held in memory, never
// written in plaintext, and the gate still works for this process.
func TestEnrolmentWithoutSecureStore(t *testing.T) {
	g := newTestGate(t, false)
	if g.Persistent() {
		t.Fatal("Persistent() must be false without an OS secure store")
	}
	secret := enrol(t, g)

	e, ok := g.enrolment()
	if !ok {
		t.Fatal("no enrolment record")
	}
	if e.Sealed {
		t.Error("record claims Sealed without a secure store")
	}
	// The whole point: it works, but only in RAM.
	code, _ := totp.CodeAt(secret, time.Now())
	if _, valid := totp.Validate(secret, code, time.Now()); !valid {
		t.Fatal("secret unusable")
	}
}

// TestUnsealedSecretRejectedWhenSealingWorks: a plaintext secret in a store on a machine that
// CAN seal is a downgraded/tampered record - refuse it rather than trust it.
func TestUnsealedSecretRejectedWhenSealingWorks(t *testing.T) {
	g := newTestGate(t, true)
	_, err := g.unsealSecret(enrolment{Secret: []byte("PLAINTEXTSECRET"), Sealed: false})
	if !errors.Is(err, ErrUnsealed) {
		t.Errorf("err = %v, want ErrUnsealed", err)
	}
}

// ── the gate, end to end, over an arbitrary transport ────────────────────────

// TestGateTOTPBootstrapThenToken is the headline flow: an unpaired caller presents a TOTP
// code, gets a token, and every later connect uses the token with no code.
func TestGateTOTPBootstrapThenToken(t *testing.T) {
	reached, caller := newTestGate(t, true), newTestGate(t, true)
	secret := enrol(t, reached)
	reached.SetSelfID("node-reached")

	if caller.HasPeerToken("node-reached") {
		t.Fatal("caller holds a token before ever pairing")
	}

	// 1st connect: no token → TOTP.
	asked := 0
	codeFn := func(peerID string) string {
		asked++
		if peerID != "node-reached" {
			t.Errorf("challenge nodeId = %q, want node-reached", peerID)
		}
		return nextCode(t, secret)
	}
	grant, vErr, pErr := runGate(t, reached, caller, "node-caller", codeFn)
	if vErr != nil || pErr != nil {
		t.Fatalf("TOTP bootstrap failed: verify=%v prove=%v", vErr, pErr)
	}
	if grant.Method != MethodTOTP {
		t.Errorf("method = %q, want totp", grant.Method)
	}
	if grant.PeerID != "node-caller" {
		t.Errorf("grant peer = %q, want node-caller", grant.PeerID)
	}
	if asked != 1 {
		t.Errorf("code prompts = %d, want 1", asked)
	}
	if !caller.HasPeerToken("node-reached") {
		t.Fatal("caller did not persist the minted token")
	}

	// The reached side lists it as a trusted session, with the caller's label.
	sess := reached.Sessions()
	if len(sess) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sess))
	}
	if sess[0].PeerID != "node-caller" || sess[0].Transport != TransportBridge {
		t.Errorf("session = %+v, want peer node-caller over bridge", sess[0])
	}
	if !strings.Contains(sess[0].Label, "Caller PC") {
		t.Errorf("session label = %q, want the caller's self-description", sess[0].Label)
	}

	// 2nd connect: token, no prompt.
	asked = 0
	grant2, vErr, pErr := runGate(t, reached, caller, "node-caller", codeFn)
	if vErr != nil || pErr != nil {
		t.Fatalf("token reconnect failed: verify=%v prove=%v", vErr, pErr)
	}
	if grant2.Method != MethodToken {
		t.Errorf("method = %q, want token", grant2.Method)
	}
	if asked != 0 {
		t.Errorf("code prompts on token reconnect = %d, want 0 (the whole point of the token)", asked)
	}
	// Refresh-on-use: still exactly one live session, not a second one.
	if got := len(reached.Sessions()); got != 1 {
		t.Errorf("sessions after reconnect = %d, want 1 (refreshed, not duplicated)", got)
	}
}

// TestTokenIsPairwise: a token minted for peer A must not authorize peer B, even though the
// token value is valid. Without this a token lifted off one device authorizes any other.
func TestTokenIsPairwise(t *testing.T) {
	reached, caller := newTestGate(t, true), newTestGate(t, true)
	secret := enrol(t, reached)
	reached.SetSelfID("node-reached")
	codeFn := func(string) string { return nextCode(t, secret) }

	if _, vErr, pErr := runGate(t, reached, caller, "node-A", codeFn); vErr != nil || pErr != nil {
		t.Fatalf("bootstrap: verify=%v prove=%v", vErr, pErr)
	}
	// caller now holds A's token. Replay it while claiming to be node-B.
	// Deny TOTP fallback by removing the enrolment, so the only credential is the token.
	if err := reached.Unenrol(); err != nil {
		t.Fatalf("Unenrol: %v", err)
	}
	_, vErr, pErr := runGate(t, reached, caller, "node-B", nil)
	if vErr == nil {
		t.Fatal("a token minted for node-A authorized node-B")
	}
	if !errors.Is(vErr, ErrDenied) {
		t.Errorf("verify err = %v, want ErrDenied", vErr)
	}
	if pErr == nil {
		t.Error("caller side did not see the denial")
	}
}

// TestTOTPReplayRejected: the same code must not authorize two channels, even inside its
// ~90s validity window.
func TestTOTPReplayRejected(t *testing.T) {
	reached := newTestGate(t, true)
	secret := enrol(t, reached)
	reached.SetSelfID("node-reached")
	code := nextCode(t, secret)
	fixed := func(string) string { return code }

	c1 := newTestGate(t, true)
	if _, vErr, _ := runGate(t, reached, c1, "node-1", fixed); vErr != nil {
		t.Fatalf("first use of the code failed: %v", vErr)
	}
	// Same code, different caller, within the window.
	c2 := newTestGate(t, true)
	_, vErr, _ := runGate(t, reached, c2, "node-2", fixed)
	if vErr == nil {
		t.Fatal("a replayed TOTP code authorized a second channel - the step was not burned")
	}
}

// TestTOTPThrottle: repeated bad codes lock the caller out, so a 10^6-wide code space can't
// be brute-forced by anyone who can reach the channel.
func TestTOTPThrottle(t *testing.T) {
	reached := newTestGate(t, true)
	enrol(t, reached)
	reached.SetSelfID("node-reached")
	bad := func(string) string { return "000000" }

	for i := range maxFails {
		caller := newTestGate(t, true)
		_, vErr, _ := runGate(t, reached, caller, "attacker", bad)
		if vErr == nil {
			t.Fatalf("attempt %d: a wrong code was accepted", i)
		}
	}
	// Locked out now: even a CORRECT code is refused.
	if locked, _ := reached.lockedOut("attacker"); !locked {
		t.Fatal("no lockout after maxFails wrong codes - the gate is brute-forceable")
	}
	caller := newTestGate(t, true)
	_, vErr, _ := runGate(t, reached, caller, "attacker", bad)
	if !errors.Is(vErr, ErrLockedOut) {
		t.Errorf("err = %v, want ErrLockedOut", vErr)
	}

	// The lockout is per-peer: an innocent third party is unaffected.
	if locked, _ := reached.lockedOut("someone-else"); locked {
		t.Error("lockout leaked to an unrelated peer")
	}
}

// ── token lifecycle ──────────────────────────────────────────────────────────

// TestTokenIdleExpiry: a token unused for longer than IdleExpiry is dead and reaped.
func TestTokenIdleExpiry(t *testing.T) {
	g := newTestGate(t, true)
	tok, _, err := g.issue("node-x", "Old Laptop", TransportBridge)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, ok := g.lookupToken(tok); !ok {
		t.Fatal("freshly issued token not found")
	}

	// Backdate LastUsed past the idle horizon.
	rec, _ := g.lookupToken(tok)
	rec.LastUsed = time.Now().Add(-IdleExpiry - time.Hour)
	if err := g.putToken(rec); err != nil {
		t.Fatalf("putToken: %v", err)
	}
	if !rec.Expired(time.Now()) {
		t.Fatal("Expired() false past the idle horizon")
	}

	// The verdict path rejects it...
	if _, err := g.check("node-x", TransportBridge, responseFrame{Method: MethodToken, Token: tok}); !errors.Is(err, ErrDenied) {
		t.Errorf("expired token: err = %v, want ErrDenied", err)
	}
	// ...and Sessions() reaps it rather than showing a dead entry.
	if got := len(g.Sessions()); got != 0 {
		t.Errorf("sessions = %d, want 0 (expired token should be reaped)", got)
	}
	if _, ok := g.lookupToken(tok); ok {
		t.Error("expired token still present after the reap")
	}
}

// TestTokenRefreshRotates: using a token mints a new one and kills the old, so a captured
// token has a short useful life.
func TestTokenRefreshRotates(t *testing.T) {
	g := newTestGate(t, true)
	old, _, err := g.issue("node-x", "PC", TransportBridge)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	grant, err := g.check("node-x", TransportBridge, responseFrame{Method: MethodToken, Token: old})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if grant.Token == old {
		t.Fatal("token was not rotated on use")
	}
	if _, ok := g.lookupToken(old); ok {
		t.Error("the old token still validates after rotation - it must be one-time")
	}
	if _, ok := g.lookupToken(grant.Token); !ok {
		t.Error("the new token does not validate")
	}
	if got := len(g.Sessions()); got != 1 {
		t.Errorf("sessions = %d, want 1", got)
	}
}

func TestRevoke(t *testing.T) {
	g := newTestGate(t, true)
	a, _, _ := g.issue("node-a", "A", TransportBridge)
	b, _, _ := g.issue("node-b", "B", TransportLAN)

	if err := g.Revoke("node-a"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := g.lookupToken(a); ok {
		t.Error("revoked token still validates")
	}
	if _, ok := g.lookupToken(b); !ok {
		t.Error("Revoke dropped an unrelated peer's token")
	}

	if err := g.RevokeAll(); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if got := len(g.Sessions()); got != 0 {
		t.Errorf("sessions after RevokeAll = %d, want 0", got)
	}
}

// TestTokensStoredHashedOnly: the at-rest record must not contain anything presentable.
func TestTokensStoredHashedOnly(t *testing.T) {
	g := newTestGate(t, true)
	tok, _, err := g.issue("node-x", "PC", TransportBridge)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	recs, err := g.tokens()
	if err != nil || len(recs) != 1 {
		t.Fatalf("tokens() = %d recs, err %v", len(recs), err)
	}
	if recs[0].Hash == tok {
		t.Fatal("the raw token is stored - a store leak would yield usable credentials")
	}
	if strings.Contains(recs[0].Hash, tok) {
		t.Fatal("the stored hash embeds the raw token")
	}
	if recs[0].Hash != hashToken(tok) {
		t.Error("stored hash is not sha256(token)")
	}
}

// TestProveWithoutCredentialAborts: no stored token and no code from the user → clean refusal,
// not a hang or a panic.
func TestProveWithoutCredentialAborts(t *testing.T) {
	reached, caller := newTestGate(t, true), newTestGate(t, true)
	enrol(t, reached)
	reached.SetSelfID("node-reached")

	// User cancels the prompt.
	_, _, pErr := runGate(t, reached, caller, "node-caller", func(string) string { return "" })
	if !errors.Is(pErr, ErrNoCred) {
		t.Errorf("prove err = %v, want ErrNoCred", pErr)
	}
}

// TestNoEnrolmentNoTOTPMethod: an instance with no authenticator must not offer totp, so a
// caller can't bootstrap into an ungated instance.
func TestNoEnrolmentNoTOTPMethod(t *testing.T) {
	reached, caller := newTestGate(t, true), newTestGate(t, true)
	reached.SetSelfID("node-reached")
	// No enrolment at all.
	_, vErr, pErr := runGate(t, reached, caller, "node-caller", func(string) string { return "123456" })
	if vErr == nil {
		t.Fatal("an instance with no enrolled authenticator granted access")
	}
	if !errors.Is(pErr, ErrNoCred) {
		t.Errorf("prove err = %v, want ErrNoCred (peer offered no method we can satisfy)", pErr)
	}
}
