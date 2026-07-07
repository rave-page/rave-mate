package studio

import "testing"

// TestCanonicalJSON pins byte-exact agreement with the TS canonicalJson (encoding.ts).
// Reference strings were produced by the actual sortDeep+JSON.stringify logic via node.
func TestCanonicalJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"b":"z","a":1,"m":{"y":2,"x":3}}`,
			`{"a":1,"b":"z","m":{"x":3,"y":2}}`},
		{`{"t":"req","id":"a1","method":"localMedia.listDirectory","params":{"path":"C:/x","includeHidden":false},"seq":0}`,
			`{"id":"a1","method":"localMedia.listDirectory","params":{"includeHidden":false,"path":"C:/x"},"seq":0,"t":"req"}`},
		{`{"s":"a<b>c&d","n":123.456,"e":1.5,"neg":-30,"big":1717459200000}`,
			`{"big":1717459200000,"e":1.5,"n":123.456,"neg":-30,"s":"a<b>c&d"}`},
		{`[{"clientNonce":"AAA","clientEcdhPubJwk":{"kty":"EC","crv":"P-256","x":"X1","y":"Y1","key_ops":[],"ext":true}},{"t":"server-hello","port":47615,"pid":1234}]`,
			`[{"clientEcdhPubJwk":{"crv":"P-256","ext":true,"key_ops":[],"kty":"EC","x":"X1","y":"Y1"},"clientNonce":"AAA"},{"pid":1234,"port":47615,"t":"server-hello"}]`},
	}
	for i, c := range cases {
		got, err := canonicalJSON([]byte(c.in))
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if string(got) != c.want {
			t.Errorf("case %d:\n got %s\nwant %s", i, got, c.want)
		}
	}
}

// TestEcdhJwkRoundtrip checks export→import→shared-secret symmetry (both directions of
// the ECDH agreement must yield the same Z).
func TestEcdhJwkRoundtrip(t *testing.T) {
	aPriv, aJwk, err := generateEcdh()
	if err != nil {
		t.Fatal(err)
	}
	bPriv, bJwk, err := generateEcdh()
	if err != nil {
		t.Fatal(err)
	}
	aPub, err := publicKeyFromJwk(aJwk)
	if err != nil {
		t.Fatal(err)
	}
	bPub, err := publicKeyFromJwk(bJwk)
	if err != nil {
		t.Fatal(err)
	}
	z1, err := deriveSharedSecret(aPriv, bPub)
	if err != nil {
		t.Fatal(err)
	}
	z2, err := deriveSharedSecret(bPriv, aPub)
	if err != nil {
		t.Fatal(err)
	}
	if len(z1) != 32 || string(z1) != string(z2) {
		t.Fatalf("ECDH mismatch: len=%d equal=%v", len(z1), string(z1) == string(z2))
	}
}

func TestTokenBindId(t *testing.T) {
	// header.payload.sig with payload {"jti":"abc"} (base64url, unpadded).
	withJti := "eyJhbGciOiJSUzI1NiJ9.eyJqdGkiOiJhYmMifQ.sig"
	if got := tokenBindId(withJti); got != "abc" {
		t.Errorf("jti token: got %q want abc", got)
	}
	noJti := "opaque-token-no-dots"
	if got := tokenBindId(noJti); got != noJti {
		t.Errorf("opaque token: got %q want %q", got, noJti)
	}
}
