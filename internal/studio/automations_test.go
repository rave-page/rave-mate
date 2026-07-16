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

// testPresets resolves the real builtin presets, so the loudness wire check sees the same codecs
// production does ("remux" copies audio; "audioAac" re-encodes it).
var testPresets automation.PresetResolver = func(id string) (transcode.Preset, bool) {
	return transcode.Find(id)
}

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

// callList is call() for methods whose result is an array (automations.list).
func (cl *testClient) callList(method string, params map[string]any) []map[string]any {
	id := fmt.Sprintf("r%d", cl.seq)
	cl.send(id, method, params, false)
	for {
		f := cl.readFrame()
		if f["t"] != "res" || f["id"] != id {
			continue
		}
		if f["ok"] != true {
			cl.t.Fatalf("%s failed: %v", method, f["error"])
		}
		arr, _ := f["result"].([]any)
		out := make([]map[string]any, 0, len(arr))
		for _, it := range arr {
			m, _ := it.(map[string]any)
			out = append(out, m)
		}
		return out
	}
}

// callErr sends a unary req expected to FAIL and returns the error message ("" if it succeeded).
func (cl *testClient) callErr(method string, params map[string]any) string {
	id := fmt.Sprintf("r%d", cl.seq)
	cl.send(id, method, params, false)
	for {
		f := cl.readFrame()
		if f["t"] != "res" || f["id"] != id {
			continue
		}
		if f["ok"] == true {
			return ""
		}
		return fmt.Sprint(f["error"])
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
	srv := New(logbus.New(100), idResolver{"user-1"}, tokenSrc{desktopJWT}, nil, nil, st, autos, testPresets)
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

// TestStudioAutomationsFieldParity round-trips the fields the automation backend grew (the delete
// action, match.minAgeDays, the per-action loudness override) through the real camel-case wire:
// create → engine → list. Guards the mappers, which are hand-written per field and so are exactly
// where a new field silently goes missing.
func TestStudioAutomationsFieldParity(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()
	autos := automation.NewManager(st, nil, func(string) (transcode.Preset, bool) { return transcode.Preset{}, false }, logbus.New(50))

	desktopJWT, webJWT := makeJWT("jti-desktop"), makeJWT("jti-web")
	srv := New(logbus.New(100), idResolver{"user-1"}, tokenSrc{desktopJWT}, nil, nil, st, autos, testPresets)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()
	cl := connectClient(t, srv, desktopJWT, webJWT)

	created := cl.call("automations.create", map[string]any{
		"input": map[string]any{
			"label":          "archive",
			"watchDirectory": dir,
			"match":          map[string]any{"extensions": []any{".WAV"}, "minAgeDays": 30},
			"actions": []any{
				map[string]any{
					"type": "transcode", "presetId": "p1",
					"loudnessOn": true, "loudnessI": -9, "loudnessTP": -0.5, "loudnessRaiseOnly": true,
				},
				map[string]any{"type": "delete"}, // terminal
			},
		},
	})
	autoID, _ := created["id"].(string)
	if autoID == "" {
		t.Fatalf("create returned no id: %v", created)
	}

	// The engine's own view: the wire→Go mapper must have carried every field.
	got, ok := autos.Get(autoID)
	if !ok {
		t.Fatalf("automation %s not persisted", autoID)
	}
	if got.Match.MinAgeDays != 30 {
		t.Fatalf("minAgeDays not mapped: %+v", got.Match)
	}
	if len(got.Match.Extensions) != 1 || got.Match.Extensions[0] != ".wav" {
		t.Fatalf("extensions not lower-cased: %+v", got.Match) // engine matches on lower-case ext
	}
	if len(got.Actions) != 2 {
		t.Fatalf("actions=%+v", got.Actions)
	}
	if a := got.Actions[0]; !a.LoudnessOn || a.LoudnessI != -9 || a.LoudnessTP != -0.5 || !a.LoudnessRaiseOnly {
		t.Fatalf("loudness override not mapped: %+v", a)
	}
	if got.Actions[1].Type != automation.ActionDelete {
		t.Fatalf("delete action not mapped: %+v", got.Actions[1])
	}

	// ...and back out over the wire (Go→wire mapper), in the shape the web client reads.
	items := cl.callList("automations.list", map[string]any{})
	if len(items) != 1 {
		t.Fatalf("list len=%d", len(items))
	}
	match, _ := items[0]["match"].(map[string]any)
	if fmt.Sprint(match["minAgeDays"]) != "30" {
		t.Fatalf("minAgeDays missing from the wire: %v", match)
	}
	acts, _ := items[0]["actions"].([]any)
	if len(acts) != 2 {
		t.Fatalf("wire actions=%v", acts)
	}
	tc, _ := acts[0].(map[string]any)
	if tc["loudnessOn"] != true || fmt.Sprint(tc["loudnessI"]) != "-9" ||
		fmt.Sprint(tc["loudnessTP"]) != "-0.5" || tc["loudnessRaiseOnly"] != true {
		t.Fatalf("loudness override missing from the wire: %v", tc)
	}
	if del, _ := acts[1].(map[string]any); del["type"] != "delete" {
		t.Fatalf("delete action missing from the wire: %v", acts[1])
	}
}

// newAutosServer boots a studio server over a real automation engine + a connected client.
func newAutosServer(t *testing.T) (*testClient, automation.Manager, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	autos := automation.NewManager(st, nil, func(string) (transcode.Preset, bool) { return transcode.Preset{}, false }, logbus.New(50))

	desktopJWT, webJWT := makeJWT("jti-desktop"), makeJWT("jti-web")
	srv := New(logbus.New(100), idResolver{"user-1"}, tokenSrc{desktopJWT}, nil, nil, st, autos, testPresets)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(srv.Stop)
	return connectClient(t, srv, desktopJWT, webJWT), autos, dir
}

// TestStudioAutomationsOldClientCannotEraseUnknownFields simulates the DEPLOYED web app: its typed
// Automation model predates match.minAgeDays + the action loudness keys, so it drops them when it
// decodes a GET and PATCHes the object back without them. minAgeDays gates a DELETE chain - if an
// absent key decoded to 0, an unrelated rename from the web Remote Studio would silently turn
// "delete raw sets older than 30 days" into "delete every matching set, nightly".
func TestStudioAutomationsOldClientCannotEraseUnknownFields(t *testing.T) {
	cl, autos, dir := newAutosServer(t)

	created := cl.call("automations.create", map[string]any{
		"input": map[string]any{
			"label":          "purge old sets",
			"watchDirectory": dir,
			"match":          map[string]any{"extensions": []any{".wav"}, "minAgeDays": 30},
			"actions": []any{
				map[string]any{
					"type": "transcode", "presetId": "p1",
					"loudnessOn": true, "loudnessI": -9, "loudnessTP": -0.5, "loudnessRaiseOnly": true,
				},
				map[string]any{"type": "delete"},
			},
		},
	})
	autoID, _ := created["id"].(string)
	if autoID == "" {
		t.Fatalf("create returned no id: %v", created)
	}

	// GET, then strip every key the old client's model lacks - exactly what its decode does.
	items := cl.callList("automations.list", map[string]any{})
	if len(items) != 1 {
		t.Fatalf("list len=%d", len(items))
	}
	got := items[0]
	match, _ := got["match"].(map[string]any)
	delete(match, "minAgeDays")
	acts, _ := got["actions"].([]any)
	for _, a := range acts {
		am, _ := a.(map[string]any)
		delete(am, "loudnessOn")
		delete(am, "loudnessI")
		delete(am, "loudnessTP")
		delete(am, "loudnessRaiseOnly")
	}

	// ...and PATCH the whole object back with only the label touched (the rename path).
	cl.call("automations.update", map[string]any{"id": autoID, "patch": map[string]any{
		"label": "purge old sets (renamed)", "watchDirectory": got["watchDirectory"],
		"enabled": got["enabled"], "match": match, "actions": acts,
	}})

	after, ok := autos.Get(autoID)
	if !ok {
		t.Fatal("automation gone")
	}
	if after.Label != "purge old sets (renamed)" {
		t.Fatalf("the rename itself must apply: %q", after.Label)
	}
	if after.Match.MinAgeDays != 30 {
		t.Fatalf("THE AGE GATE VANISHED: an old client's rename armed an unconditional nightly purge (match=%+v)", after.Match)
	}
	if len(after.Actions) != 2 {
		t.Fatalf("actions=%+v", after.Actions)
	}
	if a := after.Actions[0]; !a.LoudnessOn || a.LoudnessI != -9 || a.LoudnessTP != -0.5 || !a.LoudnessRaiseOnly {
		t.Fatalf("loudness override erased by an old client's round-trip: %+v", a)
	}
	if after.Actions[1].Type != automation.ActionDelete {
		t.Fatalf("delete action lost: %+v", after.Actions[1])
	}
}

// TestStudioAutomationsExplicitClearStillWorks is the other half of absent≠zero: a client that DOES
// know a field stays in full control of it, including turning it off. Preserve-on-absent must not
// become preserve-on-everything.
func TestStudioAutomationsExplicitClearStillWorks(t *testing.T) {
	cl, autos, dir := newAutosServer(t)

	created := cl.call("automations.create", map[string]any{
		"input": map[string]any{
			"label": "x", "watchDirectory": dir,
			"match": map[string]any{"minAgeDays": 30},
			"actions": []any{
				map[string]any{"type": "transcode", "presetId": "p1", "loudnessOn": true, "loudnessI": -9},
				map[string]any{"type": "delete"},
			},
		},
	})
	autoID, _ := created["id"].(string)

	// An explicit 0 / false is PRESENT ⇒ honored, so the gate and the override can still be cleared.
	cl.call("automations.update", map[string]any{"id": autoID, "patch": map[string]any{
		"match": map[string]any{"minAgeDays": 0},
		"actions": []any{
			map[string]any{"type": "transcode", "presetId": "p1", "loudnessOn": false},
			map[string]any{"type": "delete"},
		},
	}})
	after, _ := autos.Get(autoID)
	if after.Match.MinAgeDays != 0 {
		t.Fatalf("an explicit minAgeDays:0 must clear the gate, got %d", after.Match.MinAgeDays)
	}
	if after.Actions[0].LoudnessOn {
		t.Fatalf("an explicit loudnessOn:false must clear the override: %+v", after.Actions[0])
	}
}

// TestStudioAutomationsEditedChainIsAuthoritative pins the documented limit of the action merge:
// actions carry no stable id, so loudness is only carried across a chain the client provably did
// NOT restructure. Edit the chain and the patch stands as sent - a visible, recoverable loss rather
// than a guess at which action a value belonged to.
func TestStudioAutomationsEditedChainIsAuthoritative(t *testing.T) {
	cl, autos, dir := newAutosServer(t)

	created := cl.call("automations.create", map[string]any{
		"input": map[string]any{
			"label": "x", "watchDirectory": dir,
			"actions": []any{
				map[string]any{"type": "transcode", "presetId": "p1", "loudnessOn": true, "loudnessI": -9},
				map[string]any{"type": "delete"},
			},
		},
	})
	autoID, _ := created["id"].(string)

	// Same length + types, but the preset changed: the client edited a chain it can't fully express.
	cl.call("automations.update", map[string]any{"id": autoID, "patch": map[string]any{
		"actions": []any{
			map[string]any{"type": "transcode", "presetId": "p2"},
			map[string]any{"type": "delete"},
		},
	}})
	after, _ := autos.Get(autoID)
	if after.Actions[0].PresetID != "p2" {
		t.Fatalf("the edit must apply: %+v", after.Actions[0])
	}
	if after.Actions[0].LoudnessOn {
		t.Fatalf("a restructured chain must not inherit loudness by index: %+v", after.Actions[0])
	}
}

// TestStudioAutomationsValidation pins that the wire layer enforces the ENGINE's rule set
// (automation.ValidateActions) rather than a drifting copy: delete-is-terminal is rejected here,
// at save, and every chain an old client could express still validates exactly as before.
func TestStudioAutomationsValidation(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()
	autos := automation.NewManager(st, nil, func(string) (transcode.Preset, bool) { return transcode.Preset{}, false }, logbus.New(50))

	desktopJWT, webJWT := makeJWT("jti-desktop"), makeJWT("jti-web")
	srv := New(logbus.New(100), idResolver{"user-1"}, tokenSrc{desktopJWT}, nil, nil, st, autos, testPresets)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()
	cl := connectClient(t, srv, desktopJWT, webJWT)

	mk := func(acts []any) map[string]any {
		return map[string]any{"input": map[string]any{
			"label": "x", "watchDirectory": dir, "actions": acts,
		}}
	}
	bad := []struct {
		name string
		acts []any
	}{
		{"delete not last", []any{
			map[string]any{"type": "delete"},
			map[string]any{"type": "move-to", "outputDirectory": dir},
		}},
		{"no actions", []any{}},
		{"transcode without preset", []any{map[string]any{"type": "transcode"}}},
		{"move without dir", []any{map[string]any{"type": "move-to"}}},
		{"unknown action", []any{map[string]any{"type": "nope"}}},
		// The wire cannot WARN (the deployed client drops response keys its model lacks), so a
		// setting it would store and then ignore on every run is refused instead of accepted in
		// silence. "remux" copies audio; normalization needs a re-encode.
		{"loudness on a copy-audio preset", []any{
			map[string]any{"type": "transcode", "presetId": "remux", "loudnessOn": true, "loudnessI": -14},
		}},
		{"loudness on trim-silence's remux default", []any{
			map[string]any{"type": "trim-silence", "loudnessOn": true},
		}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := cl.callErr("automations.create", mk(tc.acts)); err == "" {
				t.Fatalf("%s must be rejected", tc.name)
			}
		})
	}
	// A chain an old client could express is still accepted, unchanged.
	if res := cl.call("automations.create", mk([]any{
		map[string]any{"type": "move-to", "outputDirectory": dir},
	})); res["id"] == "" {
		t.Fatal("legacy-shaped chain must still save")
	}
	// The loudness rule is NARROW: on a preset that re-encodes, the same override is legitimate.
	if res := cl.call("automations.create", mk([]any{
		map[string]any{"type": "transcode", "presetId": "audioAac", "loudnessOn": true, "loudnessI": -14},
	})); res["id"] == "" {
		t.Fatal("a LUFS target on a re-encoding preset must save")
	}
}

// TestStudioAutomationsLoudnessCheckSparesLegacyToggles: the check is scoped to a patch that
// CARRIES a chain. Data saved before the rule existed must stay enable/disable-able, or an old
// inert override would strand the automation in whatever state it was left in.
func TestStudioAutomationsLoudnessCheckSparesLegacyToggles(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()
	autos := automation.NewManager(st, nil, testPresets, logbus.New(50))

	// Seeded straight into the engine, as a pre-rule client would have left it.
	seeded, err := autos.Save(automation.Automation{
		Label: "legacy", WatchDir: dir, Enabled: true,
		Actions: []automation.Action{{Type: automation.ActionTranscode, PresetID: "remux",
			LoudnessOn: true, LoudnessI: -14}},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	desktopJWT, webJWT := makeJWT("jti-desktop"), makeJWT("jti-web")
	srv := New(logbus.New(100), idResolver{"user-1"}, tokenSrc{desktopJWT}, nil, nil, st, autos, testPresets)
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()
	cl := connectClient(t, srv, desktopJWT, webJWT)

	// No "actions" key ⇒ the chain is untouched ⇒ the rule must not fire.
	if err := cl.callErr("automations.update", map[string]any{
		"id": seeded.ID, "patch": map[string]any{"enabled": false},
	}); err != "" {
		t.Fatalf("a toggle on pre-rule data was rejected: %s", err)
	}
	got, ok := autos.Get(seeded.ID)
	if !ok || got.Enabled {
		t.Fatal("the toggle did not land")
	}
	if !got.Actions[0].LoudnessOn {
		t.Fatal("the toggle silently stripped the stored override")
	}
}
