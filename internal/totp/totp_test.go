package totp

import (
	"encoding/base32"
	"net/url"
	"strings"
	"testing"
	"time"
)

// rfcSecret is the RFC 6238 Appendix B seed: the ASCII string "12345678901234567890".
var rfcSecret = b32.EncodeToString([]byte("12345678901234567890"))

// TestRFC6238Vectors pins hotp() against the SHA-1 rows of RFC 6238 Appendix B (T0=0, X=30).
// The RFC prints 8 digits; our 6-digit codes are the same value mod 10^6, i.e. the last 6
// digits - asserted together so a truncation bug can't hide behind the digit count.
func TestRFC6238Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	cases := []struct {
		unix   int64
		want8  string
		wantHx uint64 // the T value the RFC tabulates, to pin the counter derivation itself
	}{
		{59, "94287082", 0x0000000000000001},
		{1111111109, "07081804", 0x00000000023523EC},
		{1111111111, "14050471", 0x00000000023523ED},
		{1234567890, "89005924", 0x000000000273EF07},
		{2000000000, "69279037", 0x0000000003F940AA},
		{20000000000, "65353130", 0x0000000027BC86AA},
	}
	for _, c := range cases {
		at := time.Unix(c.unix, 0).UTC()
		if got := counter(at); got != c.wantHx {
			t.Errorf("counter(%d) = %#x, want %#x", c.unix, got, c.wantHx)
		}
		if got := hotp(key, c.wantHx, 8); got != c.want8 {
			t.Errorf("hotp(T=%#x, 8) = %s, want %s", c.wantHx, got, c.want8)
		}
		want6 := c.want8[len(c.want8)-6:]
		if got := hotp(key, c.wantHx, 6); got != want6 {
			t.Errorf("hotp(T=%#x, 6) = %s, want %s", c.wantHx, got, want6)
		}
		// End-to-end through the public API (base32 secret in, code out).
		got, err := CodeAt(rfcSecret, at)
		if err != nil {
			t.Fatalf("CodeAt: %v", err)
		}
		if got != want6 {
			t.Errorf("CodeAt(%d) = %s, want %s", c.unix, got, want6)
		}
	}
}

// TestValidateSkewWindow: ±1 step accepted, ±2 rejected, and the matched counter is reported
// so the caller can pin it for replay defence.
func TestValidateSkewWindow(t *testing.T) {
	now := time.Unix(1111111111, 0).UTC()
	base := counter(now)

	for _, off := range []int64{-1, 0, 1} {
		at := time.Unix(1111111111+off*StepSeconds, 0).UTC()
		code, err := CodeAt(rfcSecret, at)
		if err != nil {
			t.Fatalf("CodeAt: %v", err)
		}
		matched, ok := Validate(rfcSecret, code, now)
		if !ok {
			t.Errorf("step %+d: want accepted", off)
		}
		if want := uint64(int64(base) + off); matched != want {
			t.Errorf("step %+d: matched = %d, want %d", off, matched, want)
		}
	}

	for _, off := range []int64{-2, 2, 10} {
		at := time.Unix(1111111111+off*StepSeconds, 0).UTC()
		code, _ := CodeAt(rfcSecret, at)
		if _, ok := Validate(rfcSecret, code, now); ok {
			t.Errorf("step %+d: want rejected (outside ±%d window)", off, Skew)
		}
	}
}

func TestValidateRejectsGarbage(t *testing.T) {
	now := time.Unix(1111111111, 0).UTC()
	good, _ := CodeAt(rfcSecret, now)

	cases := []struct{ name, secret, code string }{
		{"wrong code", rfcSecret, "000000"},
		{"too short", rfcSecret, "12345"},
		{"too long", rfcSecret, "1234567"},
		{"empty", rfcSecret, ""},
		{"non-digits", rfcSecret, "abcdef"},
		{"empty secret", "", good},
		{"bad base32", "!!!!!!!!", good},
		{"wrong secret", b32.EncodeToString([]byte("09876543210987654321")), good},
	}
	for _, c := range cases {
		if _, ok := Validate(c.secret, c.code, now); ok {
			t.Errorf("%s: want rejected", c.name)
		}
	}
}

// TestDecodeSecretTolerance: authenticator apps and humans paste secrets lowercased, spaced,
// and padded - all must still verify.
func TestDecodeSecretTolerance(t *testing.T) {
	now := time.Unix(1111111111, 0).UTC()
	code, _ := CodeAt(rfcSecret, now)

	padded := base32.StdEncoding.EncodeToString([]byte("12345678901234567890")) // with '=' padding
	variants := map[string]string{
		"canonical": rfcSecret,
		"lowercase": strings.ToLower(rfcSecret),
		"spaced":    strings.Join([]string{rfcSecret[:4], rfcSecret[4:8], rfcSecret[8:]}, " "),
		"padded":    padded,
	}
	for name, s := range variants {
		if _, ok := Validate(s, code, now); !ok {
			t.Errorf("%s secret form: want accepted", name)
		}
	}
}

func TestGenerateSecretIsFreshAndUsable(t *testing.T) {
	a, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	b, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if a == b {
		t.Fatal("two generated secrets are identical - not random")
	}
	raw, err := b32.DecodeString(a)
	if err != nil {
		t.Fatalf("generated secret is not unpadded base32: %v", err)
	}
	if len(raw) != secretBytes {
		t.Errorf("secret = %d bytes, want %d (RFC 4226 §4 R6)", len(raw), secretBytes)
	}
	if strings.Contains(a, "=") {
		t.Error("generated secret carries base32 padding - authenticator apps reject it")
	}

	now := time.Now()
	code, err := CodeAt(a, now)
	if err != nil {
		t.Fatalf("CodeAt: %v", err)
	}
	if _, ok := Validate(a, code, now); !ok {
		t.Error("freshly generated secret does not validate its own code")
	}
	if _, ok := Validate(b, code, now); ok {
		t.Error("code from secret A validated against secret B")
	}
}

func TestURI(t *testing.T) {
	u := URI(rfcSecret, "rave.page", "Studio PC")
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatalf("URI is not parseable: %v", err)
	}
	if parsed.Scheme != "otpauth" || parsed.Host != "totp" {
		t.Errorf("scheme/host = %s://%s, want otpauth://totp", parsed.Scheme, parsed.Host)
	}
	if got := parsed.Path; got != "/rave.page:Studio PC" {
		t.Errorf("label = %q, want issuer-prefixed", got)
	}
	q := parsed.Query()
	for k, want := range map[string]string{
		"secret": rfcSecret, "issuer": "rave.page", "algorithm": "SHA1", "digits": "6", "period": "30",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("param %s = %q, want %q", k, got, want)
		}
	}

	// The parameters must describe what the verifier actually enforces, or an app enrols
	// against a different algorithm and every code fails.
	if q.Get("digits") != "6" || Digits != 6 {
		t.Error("URI digits must match the Digits the verifier enforces")
	}
	if q.Get("period") != "30" || StepSeconds != 30 {
		t.Error("URI period must match the StepSeconds the verifier enforces")
	}
}
