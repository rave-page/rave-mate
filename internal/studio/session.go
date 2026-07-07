package studio

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"rave.page/mate/internal/jobs"
)

// session is one web-client connection through its lifecycle: handshake → open. All
// post-handshake frames carry a monotonic seq + HMAC bound to the token pair.
type session struct {
	srv    *Server
	ws     *websocket.Conn
	origin string

	state string // "await-hello" | "await-auth" | "open"

	sessionID  string
	sub        string
	sessionKey []byte
	jtiBindKey []byte
	expiresAt  int64 // epoch ms

	serverPriv     *ecdh.PrivateKey
	serverNonce    string
	clientHelloRaw []byte
	serverHelloRaw []byte
	clientAuthRaw  []byte
	transcript12   string

	sendMu  sync.Mutex // serialize seq-assign → MAC → write
	sendSeq int64
	recvSeq int64 // last accepted; next must be strictly greater

	lastSeen atomic.Int64 // epoch ms

	subsMu sync.Mutex         // guards subs
	subs   map[string]*subRec // active streaming subscriptions, keyed by originating req id

	closeOnce sync.Once
	expiryT   *time.Timer
}

// subRec ties a streaming req id to its hub subscription so a cancel frame / session
// teardown can detach + cancel the underlying job. For non-job streams (automation run
// events) handle is nil and unsub releases the event-bus subscription.
type subRec struct {
	handle *jobs.Handle
	jobID  string
	unsub  func() // non-job streams (automations.subscribe); nil for transcode subs
}

// ── frame send (serialized) ──────────────────────────────────────────────────

// send assigns seq, MACs (once open), and writes the frame atomically in call order so
// concurrent responses/notifications can't reorder on the wire.
func (s *session) send(frame map[string]any) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	frame["seq"] = s.sendSeq
	s.sendSeq++
	if s.state == "open" {
		frame["mac"] = s.macOf(frame)
	}
	s.writeJSON(frame)
}

// sendRaw writes a handshake frame verbatim (no seq/mac).
func (s *session) sendRaw(frame any) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.writeJSON(frame)
}

func (s *session) writeJSON(v any) {
	raw, err := marshalNoHTMLEscape(v)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.ws.Write(ctx, websocket.MessageText, raw)
}

// macOf computes b64url(HMAC(jtiBindKey, `${seq}.${canonicalJSON(frameNoMac)}`)). The
// frame must already carry seq and must NOT carry mac.
func (s *session) macOf(frameNoMac map[string]any) string {
	canon, err := canonicalJSONValue(frameNoMac)
	if err != nil {
		return ""
	}
	input := numToString(frameNoMac["seq"]) + "." + string(canon)
	return encB64url(hmacSha256(s.jtiBindKey, []byte(input)))
}

func (s *session) verifyFrameMac(frame map[string]any) bool {
	claimed, _ := frame["mac"].(string)
	rest := make(map[string]any, len(frame))
	for k, v := range frame {
		if k != "mac" {
			rest[k] = v
		}
	}
	return constantTimeEqualStr(claimed, s.macOf(rest))
}

func numToString(v any) string {
	switch n := v.(type) {
	case json.Number:
		return n.String()
	case int64:
		return strconv.FormatInt(n, 10)
	case int:
		return strconv.Itoa(n)
	case float64:
		return strconv.FormatFloat(n, 'g', -1, 64)
	default:
		return ""
	}
}

// ── handshake ────────────────────────────────────────────────────────────────

func (s *session) fail(code handshakeFailCode, message string) {
	s.sendRaw(map[string]any{"t": "handshake-fail", "code": string(code), "message": message})
	s.closeWS(closeHandshakeFailed, string(code))
}

func (s *session) onClientHello(raw []byte, hello clientHello) {
	if hello.ProtocolVersion != protocolVersion {
		s.fail(failVersion, "need v"+strconv.Itoa(protocolVersion))
		return
	}
	if !isAllowedOrigin(hello.Origin) || hello.Origin != s.origin {
		s.fail(failOrigin, "")
		return
	}
	s.clientHelloRaw = raw

	priv, pubJwk, err := generateEcdh()
	if err != nil {
		s.fail(failInternal, "ecdh")
		return
	}
	s.serverPriv = priv
	s.serverNonce = encB64url(randomBytes(32))

	serverHello := map[string]any{
		"t":                "server-hello",
		"protocolVersion":  protocolVersion,
		"serverNonce":      s.serverNonce,
		"serverEcdhPubJwk": pubJwk,
		"instanceId":       s.srv.instanceID,
		"pid":              s.srv.pid,
		"bootTime":         s.srv.bootTime,
		"port":             s.srv.port,
		"appVersion":       appVersion,
	}
	serverHelloRaw, err := marshalNoHTMLEscape(serverHello)
	if err != nil {
		s.fail(failInternal, "serialize")
		return
	}
	s.serverHelloRaw = serverHelloRaw

	peerPub, err := publicKeyFromJwk(hello.ClientEcdhPubJwk)
	if err != nil {
		s.fail(failInternal, "peer-pub")
		return
	}
	z, err := deriveSharedSecret(priv, peerPub)
	if err != nil {
		s.fail(failInternal, "ecdh-derive")
		return
	}

	// sessionKey = HKDF(Z, salt=clientNonce||serverNonce, info=transcript(1,2)||HKDF_INFO)
	t12, err := canonicalJSON(wrapArray(raw, serverHelloRaw))
	if err != nil {
		s.fail(failInternal, "transcript")
		return
	}
	s.transcript12 = string(t12)
	cn, err1 := decB64url(hello.ClientNonce)
	sn, err2 := decB64url(s.serverNonce)
	if err1 != nil || err2 != nil {
		s.fail(failInternal, "nonce")
		return
	}
	salt := append(append([]byte{}, cn...), sn...)
	info := append(append([]byte{}, []byte(s.transcript12)...), []byte(hkdfInfo)...)
	key, err := hkdfSha256(z, salt, info, 32)
	if err != nil {
		s.fail(failInternal, "hkdf")
		return
	}
	s.sessionKey = key
	s.state = "await-auth"
	s.sendRaw(json.RawMessage(serverHelloRaw))
}

func (s *session) onClientAuth(ctx context.Context, raw []byte, auth clientAuth) {
	// 1. authTag proves ECDH possession (anti-MITM on a relay).
	expectTag := encB64url(hmacSha256(s.sessionKey, []byte("client-auth"+s.transcript12)))
	if !constantTimeEqualStr(auth.AuthTag, expectTag) {
		s.fail(failMAC, "")
		return
	}

	// 2-3. resolve canonical identity for BOTH tokens via /auth/me; assert same user.
	dToken := s.srv.desktopToken()
	if dToken == "" {
		s.fail(failTokenInvalid, "desktop not signed in")
		return
	}
	peerID, perr := s.srv.api.WhoAmI(ctx, auth.AccessToken)
	ownID, oerr := s.srv.resolveOwnID(ctx, dToken)
	if perr != nil || peerID == "" {
		s.fail(failTokenInvalid, "peer identity unresolved")
		return
	}
	if oerr != nil || ownID == "" {
		s.fail(failInternal, "desktop identity unresolved")
		return
	}
	if peerID != ownID {
		s.fail(failSubMismatch, "")
		return
	}

	// 4. bind the channel key to BOTH tokens (jti when present, else the token).
	bindKey, err := hkdfSha256(
		s.sessionKey,
		[]byte("jti-bind"),
		[]byte(tokenBindId(auth.AccessToken)+"."+tokenBindId(dToken)),
		32,
	)
	if err != nil {
		s.fail(failInternal, "bind")
		return
	}
	s.jtiBindKey = bindKey
	s.sub = ownID
	s.sessionID = encB64url(randomBytes(16))
	s.clientAuthRaw = raw
	s.expiresAt = minExpiry(tokenExpMS(auth.AccessToken), tokenExpMS(dToken))

	// 5. server-auth (own token + tag over transcript(1,2,3)).
	t123, err := canonicalJSON(wrapArray(s.clientHelloRaw, s.serverHelloRaw, raw))
	if err != nil {
		s.fail(failInternal, "transcript")
		return
	}
	serverTag := encB64url(hmacSha256(s.sessionKey, []byte("server-auth"+string(t123))))
	jti := ""
	if c := decodeJwtClaims(dToken); c != nil {
		if v, ok := c["jti"].(string); ok {
			jti = v
		}
	}
	s.sendRaw(map[string]any{"t": "server-auth", "accessToken": dToken, "jti": jti, "authTag": serverTag})

	// 6. file bridge (session-scoped token, derived) + handshake-ok. rave-mate has no
	//    local media HTTP server yet, so baseUrl is empty - control methods work; raw
	//    file streaming is a follow-up (matches the Electron fileBridge shape).
	fileToken, _ := hkdfSha256(s.sessionKey, []byte("file-token"), []byte(s.sessionID), 24)
	s.sendRaw(map[string]any{
		"t":            "handshake-ok",
		"sessionId":    s.sessionID,
		"sub":          s.sub,
		"expiresAt":    s.expiresAt,
		"transport":    "loopback",
		"fileBridge":   map[string]any{"baseUrl": "", "token": encB64url(fileToken)},
		"capabilities": s.srv.capabilities(),
	})
	s.state = "open"
	s.srv.addSession(s)
	s.armExpiry()
}

func (s *session) armExpiry() {
	dieIn := max(time.Duration(s.expiresAt-nowMS())*time.Millisecond, 0)
	warnIn := dieIn - 60*time.Second
	if warnIn > 0 {
		time.AfterFunc(warnIn, func() {
			if s.srv.hasSession(s) {
				s.notify("session", "session-expiring", map[string]any{"expiresAt": s.expiresAt})
			}
		})
	}
	s.expiryT = time.AfterFunc(dieIn, func() { s.srv.closeSession(s, closeTokenExpired, "expired") })
}

// ── post-handshake dispatch ──────────────────────────────────────────────────

func (s *session) handleData(frame map[string]any) {
	switch frame["t"] {
	case "pong":
		s.lastSeen.Store(nowMS())
	case "cancel":
		if id, ok := frame["id"].(string); ok {
			s.cancelSub(id)
		}
	case "req":
		var req dataReq
		raw, _ := marshalNoHTMLEscape(frame)
		_ = json.Unmarshal(raw, &req)
		s.handleReq(req)
	}
}

func (s *session) handleReq(req dataReq) {
	if isPeerMethod(req.Method) {
		s.handlePeerReq(req) // peer management - always local
		return
	}
	if isVrchatMethod(req.Method) {
		// Local VRChat session only - never forwarded to a remote context. Off the read loop
		// (VRChat HTTP ≤20s can't stall frame intake / heartbeat).
		go s.vrchatCall(req.Method, req.ID, asMap(req.Params))
		return
	}
	if isObsFamilyMethod(req.Method) {
		// Local OBS/app-group surface only - never forwarded. Off the read loop
		// (obs-websocket round-trips + app launches + streamReady's readiness poll).
		go s.obsCall(req.Method, req.ID, asMap(req.Params))
		return
	}
	if !isStudioMethod(req.Method) {
		s.sendErr(req.ID, errUnknownMethod, "unknown "+req.Method)
		return
	}
	if req.Target != "" {
		// Remote context: forward the unary call to the paired peer. Streaming methods can't
		// ride the unary peer-control transport.
		if streamingMethods[req.Method] {
			s.sendErr(req.ID, errBadRequest, "streaming methods aren't available on remote contexts")
			return
		}
		go s.remoteCall(req)
		return
	}
	switch req.Method {
	case "transcode.start":
		s.startTranscode(req.ID, asMap(req.Params))
		return
	case "transcode.attach":
		s.attachTranscode(req.ID, asMap(req.Params))
		return
	case "transcode.cancel":
		if s.srv.hub == nil {
			s.sendErr(req.ID, errInternal, "transcode unavailable (no worker supervisor)")
			return
		}
		s.srv.hub.Cancel(asString(asMap(req.Params)["jobId"]))
		s.send(map[string]any{"t": "res", "id": req.ID, "ok": true, "result": map[string]any{"ok": true}})
		return
	case "transcode.listEncoders", "transcode.encoderCatalog", "transcode.probeDuration":
		// One-shot worker calls (encoder test-encode ~12s, ffprobe). Run off the read loop
		// so a slow probe can't stall frame intake / heartbeat for this connection.
		go s.transcodeUnary(req.Method, req.ID, asMap(req.Params))
		return
	case "localMedia.pickDirectory", "localMedia.pickFile", "localMedia.chooseSavePath":
		// Native file dialogs block on the user - run off the read loop.
		go s.pickerCall(req.Method, req.ID, asMap(req.Params))
		return
	case "localMedia.probe", "localMedia.probeStreams", "localMedia.moveTo",
		"localMedia.rememberRecent", "localMedia.listFavorites", "localMedia.addFavorite",
		"localMedia.removeFavorite", "localMedia.listPresets", "localMedia.savePreset",
		"localMedia.deletePreset":
		// IO/persistence (ffprobe, file move, bbolt) - off the read loop.
		go s.localMediaCall(req.Method, req.ID, asMap(req.Params))
		return
	case "automations.subscribe":
		// Long-lived run-event stream (no terminal res); registered as a sub.
		s.subscribeAutomations(req.ID)
		return
	case "automations.list", "automations.create", "automations.update", "automations.delete",
		"automations.setEnabled", "automations.setBackgroundCredentials", "automations.runOnce",
		"automations.runManual", "automations.commitStep", "automations.skipStep",
		"automations.abortRun", "automations.probeSilence", "automations.listEvents":
		// Engine calls (CRUD, run control, API events) - off the read loop.
		go s.automationsCall(req.Method, req.ID, asMap(req.Params))
		return
	}
	if streamingMethods[req.Method] {
		// Any remaining streaming method we don't route above.
		s.sendErr(req.ID, errInternal, "streaming method not implemented: "+req.Method)
		return
	}
	result, code, err := dispatchUnary(req.Method, req.Params)
	if err != nil {
		s.sendErr(req.ID, code, err.Error())
		return
	}
	s.send(map[string]any{"t": "res", "id": req.ID, "ok": true, "result": result})
}

func (s *session) sendErr(id string, code errorCode, message string) {
	s.send(map[string]any{"t": "res", "id": id, "ok": false,
		"error": map[string]any{"code": string(code), "message": message}})
}

func (s *session) notify(kind, event string, payload any) {
	s.send(map[string]any{"t": "notify", "kind": kind, "event": event, "payload": payload})
}

// notifyStream sends a kind:"stream" notification tagged with the originating sub req id
// (the web client routes stream frames by `for`).
func (s *session) notifyStream(forID, event string, payload any) {
	s.send(map[string]any{"t": "notify", "kind": "stream", "for": forID, "event": event, "payload": payload})
}

func (s *session) closeWS(code int, reason string) {
	s.closeOnce.Do(func() {
		_ = s.ws.Close(websocket.StatusCode(code), reason)
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

func nowMS() int64 { return time.Now().UnixMilli() }

// wrapArray builds the bytes of a JSON array from raw element bytes: [e0,e1,...].
func wrapArray(elems ...[]byte) []byte {
	out := []byte{'['}
	for i, e := range elems {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, e...)
	}
	return append(out, ']')
}

// tokenExpMS returns the JWT exp in epoch ms, or 0 if absent.
func tokenExpMS(token string) int64 {
	c := decodeJwtClaims(token)
	if c == nil {
		return 0
	}
	switch e := c["exp"].(type) {
	case float64:
		return int64(e * 1000)
	case json.Number:
		f, _ := e.Float64()
		return int64(f * 1000)
	}
	return 0
}

// minExpiry = min of two exp's (0 = no expiry); falls back to +12h if both absent.
func minExpiry(a, b int64) int64 {
	ia, ib := a, b
	if ia == 0 {
		ia = math.MaxInt64
	}
	if ib == 0 {
		ib = math.MaxInt64
	}
	m := min(ia, ib)
	if m == math.MaxInt64 {
		return nowMS() + int64(defaultExpHr)*3600_000
	}
	return m
}
