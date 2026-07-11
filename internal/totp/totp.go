// Package totp is a stdlib-only RFC 6238 TOTP verifier (HMAC-SHA1, 6 digits, 30s step).
//
// It is the ACCESS GATE for reaching this instance from anywhere: the secret is generated
// ON THIS INSTANCE, sealed at rest (internal/shared/secureseal), and verified HERE. rave.page
// never sees it and never verifies a code - the account bridge is a blind relay, so a code
// that leaked to the server would be worthless to it. Keep it that way: never log a secret,
// a code, or an otpauth URI.
//
// Verification accepts a ±1 step window (RFC 6238 §5.2 clock skew) and compares in constant
// time across the whole window - no early exit on the first match, so a timing observer can't
// learn WHICH step matched (which would leak the verifier's clock offset).
//
// Replay: a code stays valid for up to 90s across the skew window. Callers that authorize
// state changes (see internal/authz) must additionally refuse a counter they've already
// accepted - Validate alone is stateless and cannot detect a replay.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// Digits in a generated/accepted code. 6 = the otpauth default every authenticator app
	// assumes when the URI omits the parameter.
	Digits = 6
	// StepSeconds is the RFC 6238 time step (X). 30 = the otpauth default.
	StepSeconds = 30
	// Skew is the number of steps accepted either side of the current one. 1 → a code lives
	// ~90s worst case, absorbing a couple of minutes of client/server clock drift.
	Skew = 1
	// secretBytes is the generated shared-secret length. RFC 4226 §4 R6 requires ≥128 bits and
	// recommends 160 - the HMAC-SHA1 block feed size.
	secretBytes = 20
)

// b32 is RFC 4648 base32 WITHOUT padding - what authenticator apps expect in an otpauth
// secret= parameter (a trailing '=' is rejected by several of them).
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a fresh 160-bit secret, base32-encoded (unpadded). NEVER log it.
func GenerateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("totp: generate secret: %w", err)
	}
	return b32.EncodeToString(b), nil
}

// decodeSecret parses a base32 secret, tolerating the formatting authenticator apps and
// humans introduce: lowercase, spaces, and padding.
func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(secret))
	s = strings.TrimRight(s, "=")
	if s == "" {
		return nil, fmt.Errorf("totp: empty secret")
	}
	key, err := b32.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("totp: bad base32 secret")
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("totp: empty secret")
	}
	return key, nil
}

// counter is the RFC 6238 time counter T = floor((unix - T0) / X), with T0 = 0.
func counter(t time.Time) uint64 {
	return uint64(t.Unix() / StepSeconds)
}

// CounterAt exposes the time-step counter for a moment - callers persist the last ACCEPTED
// counter to reject replays (see internal/authz).
func CounterAt(t time.Time) uint64 { return counter(t) }

// hotp is RFC 4226: dynamic truncation of HMAC-SHA1(key, counter) to `digits` decimal digits.
func hotp(key []byte, ctr uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], ctr)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 §5.3): low nibble of the last byte selects a 4-byte window;
	// mask off the sign bit so the result is a positive 31-bit int on every platform.
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff

	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// CodeAt returns the code for a secret at an instant. Test/enrolment helper - the verifier
// path is Validate. NEVER log the result.
func CodeAt(secret string, t time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, counter(t), Digits), nil
}

// Validate reports whether code is valid for secret at time t, accepting ±Skew steps. On
// success it also returns the matched counter so the caller can persist it and refuse a
// replay of the same (or an older) step.
//
// Constant-time: every candidate step is evaluated and folded into one accumulator - no
// early return - so neither validity nor WHICH step matched is observable by timing.
func Validate(secret, code string, t time.Time) (matched uint64, ok bool) {
	key, err := decodeSecret(secret)
	if err != nil {
		return 0, false
	}
	got := strings.TrimSpace(code)
	if len(got) != Digits {
		return 0, false // length is public (it's a format check, not a secret comparison)
	}

	base := counter(t)
	hit := 0
	for i := -Skew; i <= Skew; i++ {
		ctr := base + uint64(i) // wraps harmlessly for pre-1970 clocks; HMAC input is still well-defined
		eq := subtle.ConstantTimeCompare([]byte(hotp(key, ctr, Digits)), []byte(got))
		// Fold without branching: mask is all-ones on a hit, zero otherwise, so the matching
		// counter is selected without a compare-and-jump the CPU could leak.
		mask := uint64(-int64(eq))
		matched = (matched &^ mask) | (ctr & mask)
		hit |= eq
	}
	return matched, hit == 1
}

// URI builds the otpauth:// enrolment URI for an authenticator app. Contains the SECRET -
// treat it exactly like the secret: show it to the user, never log it, never send it anywhere.
//
// Params are written explicitly (algorithm/digits/period) even though all three are the
// otpauth defaults: several apps mis-handle an omitted parameter, and pinning them documents
// what the verifier actually enforces.
func URI(secret, issuer, account string) string {
	label := account
	if issuer != "" {
		label = issuer + ":" + account
	}
	q := url.Values{}
	q.Set("secret", secret)
	if issuer != "" {
		q.Set("issuer", issuer)
	}
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", Digits))
	q.Set("period", fmt.Sprintf("%d", StepSeconds))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + q.Encode()
}
