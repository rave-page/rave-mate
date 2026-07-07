package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"

	"rave.page/mate/internal/logbus"
)

// mocks: both tokens resolve to the same user id, so the identity match passes.
type idResolver struct{ id string }

func (r idResolver) WhoAmI(_ context.Context, _ string) (string, error) { return r.id, nil }

type tokenSrc struct{ tok string }

func (t tokenSrc) Token() string { return t.tok }

// makeJWT builds an unsigned-but-structural JWT with a jti + far-future exp.
func makeJWT(jti string) string {
	payload, _ := marshalNoHTMLEscape(map[string]any{"jti": jti, "exp": time.Now().Add(time.Hour).Unix()})
	return "eyJhbGciOiJub25lIn0." + encB64url(payload) + ".sig"
}

// TestHandshakeAndRPC drives the full client side of the protocol against the real
// server: ECDH + HKDF session key, mutual authTag verification, jti-bound per-frame MAC,
// monotonic seq, then a real RPC round-trip (localMedia.getDefaults).
func TestHandshakeAndRPC(t *testing.T) {
	desktopJWT := makeJWT("jti-desktop")
	webJWT := makeJWT("jti-web")

	srv := New(logbus.New(100), idResolver{"user-1"}, tokenSrc{desktopJWT}, nil, nil, nil, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d", srv.port), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://localhost"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	write := func(v any) {
		raw, _ := marshalNoHTMLEscape(v)
		if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	read := func() []byte {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return data
	}

	// 1. client-hello
	clientPriv, clientJwk, _ := generateEcdh()
	clientNonce := encB64url(randomBytes(32))
	clientHelloMap := map[string]any{
		"t": "client-hello", "protocolVersion": protocolVersion, "clientNonce": clientNonce,
		"clientEcdhPubJwk": clientJwk, "clientInstanceId": "ci", "origin": "https://localhost",
	}
	helloRaw, _ := marshalNoHTMLEscape(clientHelloMap)
	write(json.RawMessage(helloRaw))

	// 2. server-hello → derive session key
	serverHelloRaw := read()
	var sh struct {
		T                string `json:"t"`
		ServerNonce      string `json:"serverNonce"`
		ServerEcdhPubJwk jwk    `json:"serverEcdhPubJwk"`
	}
	if json.Unmarshal(serverHelloRaw, &sh) != nil || sh.T != "server-hello" {
		t.Fatalf("expected server-hello, got %s", serverHelloRaw)
	}
	serverPub, err := publicKeyFromJwk(sh.ServerEcdhPubJwk)
	if err != nil {
		t.Fatalf("server pub: %v", err)
	}
	z, _ := deriveSharedSecret(clientPriv, serverPub)
	t12, _ := canonicalJSON(wrapArray(helloRaw, serverHelloRaw))
	cn, _ := decB64url(clientNonce)
	sn, _ := decB64url(sh.ServerNonce)
	salt := append(append([]byte{}, cn...), sn...)
	info := append(append([]byte{}, t12...), []byte(hkdfInfo)...)
	sessionKey, _ := hkdfSha256(z, salt, info, 32)

	// 3. client-auth
	authTag := encB64url(hmacSha256(sessionKey, []byte("client-auth"+string(t12))))
	clientAuthMap := map[string]any{"t": "client-auth", "accessToken": webJWT, "jti": "jti-web", "authTag": authTag}
	authRaw, _ := marshalNoHTMLEscape(clientAuthMap)
	write(json.RawMessage(authRaw))

	// 4. server-auth → verify the server proved ECDH possession
	serverAuthRaw := read()
	var sa struct {
		T       string `json:"t"`
		AuthTag string `json:"authTag"`
	}
	if json.Unmarshal(serverAuthRaw, &sa) != nil || sa.T != "server-auth" {
		t.Fatalf("expected server-auth, got %s", serverAuthRaw)
	}
	t123, _ := canonicalJSON(wrapArray(helloRaw, serverHelloRaw, authRaw))
	expectServerTag := encB64url(hmacSha256(sessionKey, []byte("server-auth"+string(t123))))
	if !constantTimeEqualStr(sa.AuthTag, expectServerTag) {
		t.Fatalf("server authTag mismatch:\n got %s\nwant %s", sa.AuthTag, expectServerTag)
	}

	// jti-bind key (web . desktop order)
	jtiBindKey, _ := hkdfSha256(sessionKey, []byte("jti-bind"),
		[]byte(tokenBindId(webJWT)+"."+tokenBindId(desktopJWT)), 32)

	// 5. handshake-ok
	okRaw := read()
	var ok struct {
		T            string   `json:"t"`
		Sub          string   `json:"sub"`
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(okRaw, &ok) != nil || ok.T != "handshake-ok" {
		t.Fatalf("expected handshake-ok, got %s", okRaw)
	}
	if ok.Sub != "user-1" || len(ok.Capabilities) == 0 {
		t.Fatalf("bad handshake-ok: sub=%q caps=%d", ok.Sub, len(ok.Capabilities))
	}

	// 6. RPC: localMedia.getDefaults, seq=0, MAC'd with jtiBindKey.
	reqMap := map[string]any{"t": "req", "id": "r1", "method": "localMedia.getDefaults", "params": map[string]any{}, "seq": 0}
	canonReq, _ := canonicalJSONValue(reqMap)
	reqMap["mac"] = encB64url(hmacSha256(jtiBindKey, []byte("0."+string(canonReq))))
	write(reqMap)

	// 7. response: verify seq + MAC + result.
	resRaw := read()
	var resMap map[string]any
	dec := json.NewDecoder(bytes.NewReader(resRaw))
	dec.UseNumber()
	_ = dec.Decode(&resMap)
	if resMap["t"] != "res" {
		t.Fatalf("expected res, got %s", resRaw)
	}
	claimedMac, _ := resMap["mac"].(string)
	rest := map[string]any{}
	for k, v := range resMap {
		if k != "mac" {
			rest[k] = v
		}
	}
	canonRes, _ := canonicalJSONValue(rest)
	expectMac := encB64url(hmacSha256(jtiBindKey, []byte(numToString(rest["seq"])+"."+string(canonRes))))
	if !constantTimeEqualStr(claimedMac, expectMac) {
		t.Fatalf("res MAC mismatch")
	}
	if resMap["ok"] != true {
		t.Fatalf("res not ok: %s", resRaw)
	}
	result, _ := resMap["result"].(map[string]any)
	if _, hasHome := result["home"]; !hasHome {
		t.Fatalf("getDefaults result missing home: %v", result)
	}
}
