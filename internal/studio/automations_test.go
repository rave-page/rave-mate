package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/store"
	"rave.page/mate/internal/transcode"
)

// testClient is the web side of the studio protocol, post-handshake: it tracks seq + the
// jti-bind key so it can send MAC'd frames and read responses/notifications.
type testClient struct {
	t          *testing.T
	ctx        context.Context
	c          *websocket.Conn
	jtiBindKey []byte
	seq        int
}

// connectClient runs the full handshake against srv and returns an open testClient.
func connectClient(t *testing.T, srv *Server, desktopJWT, webJWT string) *testClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	c, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d", srv.port), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://localhost"}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(websocket.StatusNormalClosure, "") })

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

	clientPriv, clientJwk, _ := generateEcdh()
	clientNonce := encB64url(randomBytes(32))
	clientHelloMap := map[string]any{
		"t": "client-hello", "protocolVersion": protocolVersion, "clientNonce": clientNonce,
		"clientEcdhPubJwk": clientJwk, "clientInstanceId": "ci", "origin": "https://localhost",
	}
	helloRaw, _ := marshalNoHTMLEscape(clientHelloMap)
	write(json.RawMessage(helloRaw))

	serverHelloRaw := read()
	var sh struct {
		ServerNonce      string `json:"serverNonce"`
		ServerEcdhPubJwk jwk    `json:"serverEcdhPubJwk"`
	}
	_ = json.Unmarshal(serverHelloRaw, &sh)
	serverPub, _ := publicKeyFromJwk(sh.ServerEcdhPubJwk)
	z, _ := deriveSharedSecret(clientPriv, serverPub)
	t12, _ := canonicalJSON(wrapArray(helloRaw, serverHelloRaw))
	cn, _ := decB64url(clientNonce)
	sn, _ := decB64url(sh.ServerNonce)
	salt := append(append([]byte{}, cn...), sn...)
	info := append(append([]byte{}, t12...), []byte(hkdfInfo)...)
	sessionKey, _ := hkdfSha256(z, salt, info, 32)

	authTag := encB64url(hmacSha256(sessionKey, []byte("client-auth"+string(t12))))
	clientAuthMap := map[string]any{"t": "client-auth", "accessToken": webJWT, "jti": "jti-web", "authTag": authTag}
	authRaw, _ := marshalNoHTMLEscape(clientAuthMap)
	write(json.RawMessage(authRaw))

	_ = read() // server-auth (verified in TestHandshakeAndRPC)
	okRaw := read()
	var ok struct {
		T string `json:"t"`
	}
	if json.Unmarshal(okRaw, &ok) != nil || ok.T != "handshake-ok" {
		t.Fatalf("expected handshake-ok, got %s", okRaw)
	}
	jtiBindKey, _ := hkdfSha256(sessionKey, []byte("jti-bind"),
		[]byte(tokenBindId(webJWT)+"."+tokenBindId(desktopJWT)), 32)
	return &testClient{t: t, ctx: ctx, c: c, jtiBindKey: jtiBindKey}
}

// send writes a MAC'd req frame (sub=true for streaming subscriptions).
func (cl *testClient) send(id, method string, params map[string]any, sub bool) {
	frame := map[string]any{"t": "req", "id": id, "method": method, "params": params, "seq": cl.seq}
	if sub {
		frame["sub"] = true
	}
	cl.seq++
	canon, _ := canonicalJSONValue(frame)
	frame["mac"] = encB64url(hmacSha256(cl.jtiBindKey, []byte(numToString(frame["seq"])+"."+string(canon))))
	raw, _ := marshalNoHTMLEscape(frame)
	if err := cl.c.Write(cl.ctx, websocket.MessageText, raw); err != nil {
		cl.t.Fatalf("write: %v", err)
	}
}

func (cl *testClient) readFrame() map[string]any {
	_, data, err := cl.c.Read(cl.ctx)
	if err != nil {
		cl.t.Fatalf("read: %v", err)
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	_ = dec.Decode(&m)
	return m
}

// call sends a unary req and returns its res result, skipping ping/unrelated notify frames.
func (cl *testClient) call(method string, params map[string]any) map[string]any {
	id := fmt.Sprintf("r%d", cl.seq)
	cl.send(id, method, params, false)
	for {
		f := cl.readFrame()
		if f["t"] == "res" && f["id"] == id {
			if f["ok"] != true {
				cl.t.Fatalf("%s failed: %v", method, f["error"])
			}
			res, _ := f["result"].(map[string]any)
			return res
		}
		// ignore pings + stream notifies that arrive between request and response
	}
}

// waitStream reads until a stream notify (for==subID) whose payload.state == want; returns it.
func (cl *testClient) waitStreamState(subID string, want automation.RunState) map[string]any {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		f := cl.readFrame()
		if f["t"] != "notify" || f["for"] != subID {
			continue
		}
		payload, _ := f["payload"].(map[string]any)
		if payload["state"] == string(want) {
			return payload
		}
	}
	cl.t.Fatalf("timed out waiting for stream state %q", want)
	return nil
}

// TestStudioAutomationsRoundTrip drives create → subscribe → runManual → awaiting →
// commitStep → completed over the real wire, against a real automation engine.
func TestStudioAutomationsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()
	autos := automation.NewManager(st, nil, func(string) (transcode.Preset, bool) { return transcode.Preset{}, false }, logbus.New(50))

	desktopJWT, webJWT := makeJWT("jti-desktop"), makeJWT("jti-web")
	srv := New(logbus.New(100), idResolver{"user-1"}, tokenSrc{desktopJWT}, nil, nil, st, autos)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	cl := connectClient(t, srv, desktopJWT, webJWT)

	// 1. list (empty)
	cl.call("automations.list", map[string]any{})

	// 2. create a move-to automation (no ffmpeg needed)
	destDir := filepath.Join(dir, "done")
	created := cl.call("automations.create", map[string]any{
		"input": map[string]any{
			"label":          "mv",
			"watchDirectory": dir,
			"actions":        []any{map[string]any{"type": "move-to", "outputDirectory": destDir}},
		},
	})
	autoID, _ := created["id"].(string)
	if autoID == "" {
		t.Fatalf("create returned no id: %v", created)
	}
	if created["watchDirectory"] != dir {
		t.Fatalf("watchDirectory not mapped: %v", created)
	}

	// 3. subscribe to run events (streaming; no terminal res)
	cl.send("sub1", "automations.subscribe", map[string]any{}, true)

	// 4. runManual over a real file → expect {runId} + an awaiting-confirmation event
	src := filepath.Join(dir, "set.wav")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := cl.call("automations.runManual", map[string]any{"id": autoID, "filePath": src})
	runID, _ := run["runId"].(string)
	if runID == "" {
		t.Fatalf("runManual returned no runId: %v", run)
	}

	awaiting := cl.waitStreamState("sub1", automation.StateAwaiting)
	if awaiting["awaitingConfirmation"] != true {
		t.Fatalf("expected awaitingConfirmation flag: %v", awaiting)
	}
	prop, _ := awaiting["proposal"].(map[string]any)
	if prop["kind"] != "move" {
		t.Fatalf("expected move proposal: %v", prop)
	}
	// File must NOT have moved before commit.
	if _, err := os.Stat(filepath.Join(destDir, "set.wav")); err == nil {
		t.Fatal("file moved before commit")
	}

	// 5. commit → run completes + file relocates
	cl.call("automations.commitStep", map[string]any{"runId": runID})
	cl.waitStreamState("sub1", automation.StateCompleted)
	if _, err := os.Stat(filepath.Join(destDir, "set.wav")); err != nil {
		t.Fatalf("file not moved after commit: %v", err)
	}

	// 6. delete → empty list returned
	del := cl.call("automations.delete", map[string]any{"id": autoID})
	_ = del
	if got := autos.List(); len(got) != 0 {
		t.Fatalf("expected 0 automations after delete, got %d", len(got))
	}
}
