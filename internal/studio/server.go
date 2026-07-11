package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/store"
)

const source = "studio"

// IdentityResolver resolves a token → canonical rave.page user id (the api client).
type IdentityResolver interface {
	WhoAmI(ctx context.Context, token string) (string, error)
}

// TokenSource yields the desktop's current access token (the auth manager).
type TokenSource interface {
	Token() string
}

// Picker shows native desktop file dialogs for the web client's localMedia.pick* /
// chooseSavePath methods. Implemented by the Fyne UI; nil in headless/service mode. Each
// returns "" (no error) when the user cancels.
type Picker interface {
	PickDirectory(ctx context.Context) (string, error)
	PickFile(ctx context.Context) (string, error)
	ChooseSavePath(ctx context.Context, defaultPath, container string) (string, error)
}

// Server is the loopback Local Studio control server (port of studioWsServer.ts).
type Server struct {
	log    *logbus.Bus
	api    IdentityResolver
	tokens TokenSource

	hub    *jobs.Hub          // transcode job fan-out (shared with the desktop UI); nil when no supervisor
	runner jobs.Runner        // worker supervisor for one-shot calls (encoder detect, probe); nil when none
	store  *store.Store       // persistence for localMedia favorites/presets/recents; nil = no persistence
	autos  automation.Manager // media-automation engine facade (automations.* methods); nil = unavailable

	pickerMu sync.Mutex
	picker   Picker // native file dialogs (set by the UI post-construction); nil = headless

	peers  PeerGateway     // LAN peer link surface + remote-context routing; nil = peers disabled
	vrchat VrchatGateway   // local VRChat session surface (vrchat.* methods); nil = VRChat disabled
	obsGw  ObsGateway      // local OBS surface (obs.* + quickAction.*); nil = OBS disabled
	appGrp AppGroupGateway // app-group launch surface (appgroup.*); nil = disabled

	instanceID string
	pid        int
	bootTime   int64
	port       int

	httpSrv *http.Server
	ln      net.Listener

	mu       sync.Mutex
	sessions map[*session]struct{}

	ownIDMu    sync.Mutex
	ownIDToken string
	ownID      string

	ctx    context.Context
	cancel context.CancelFunc
	hbStop chan struct{}
}

// New builds the studio server. api resolves identities; tokens yields the desktop token;
// runner is the worker supervisor (one-shot encoder detect / probe); hub is the shared
// transcode job fan-out (also driven by the desktop UI). runner/hub nil disable transcode.
func New(log *logbus.Bus, api IdentityResolver, tokens TokenSource, runner jobs.Runner, hub *jobs.Hub, st *store.Store, autos automation.Manager) *Server {
	return &Server{
		log:      log,
		api:      api,
		tokens:   tokens,
		runner:   runner,
		hub:      hub,
		store:    st,
		autos:    autos,
		sessions: map[*session]struct{}{},
		hbStop:   make(chan struct{}),
	}
}

func (s *Server) desktopToken() string { return s.tokens.Token() }

// SetPicker wires the native-dialog provider (the UI). Safe to call after Start.
func (s *Server) SetPicker(p Picker) {
	s.pickerMu.Lock()
	s.picker = p
	s.pickerMu.Unlock()
}

// SetPeerGateway wires the LAN peer-link surface (peers.* + remote-context routing). Call
// once at startup, before sessions open. nil leaves peers disabled.
func (s *Server) SetPeerGateway(gw PeerGateway) { s.peers = gw }

// SetVrchatGateway wires the local VRChat session surface (vrchat.* methods). Call once at
// startup, before sessions open. nil leaves VRChat disabled (methods unadvertised).
func (s *Server) SetVrchatGateway(gw VrchatGateway) { s.vrchat = gw }

// SetObsGateway wires the local OBS surface (obs.* + quickAction.* methods). Call once at
// startup, before sessions open. nil leaves OBS disabled (methods unadvertised).
func (s *Server) SetObsGateway(gw ObsGateway) { s.obsGw = gw }

// SetAppGroupGateway wires the app-group launch surface (appgroup.* methods). Call once at
// startup, before sessions open. nil leaves app groups disabled (methods unadvertised).
func (s *Server) SetAppGroupGateway(gw AppGroupGateway) { s.appGrp = gw }

// capabilities advertises the method surface in handshake-ok: the studio methods, plus peers.*
// when a peer gateway is wired, plus vrchat.* when the VRChat feature is enabled AND a session is
// live (gated per-connection so a mate with no VRChat linked never advertises them), plus obs.* /
// appgroup.* / quickAction.* under the same per-connection gating (obs.* needs a live OBS session;
// quickAction only needs the feature on - its launch step may bring OBS up).
func (s *Server) capabilities() []string {
	caps := append([]string{}, studioMethods...)
	if s.peers != nil {
		caps = append(caps, peerMethods...)
	}
	if s.vrchat != nil && s.vrchat.Enabled() && s.vrchat.LoggedIn() {
		caps = append(caps, vrchatMethods...)
	}
	if s.obsGw != nil && s.obsGw.Enabled() {
		if s.obsGw.Connected() {
			caps = append(caps, obsMethods...)
		}
		caps = append(caps, quickActionMethods...)
	}
	if s.appGrp != nil && s.appGrp.Configured() {
		caps = append(caps, appgroupMethods...)
	}
	return caps
}

func (s *Server) getPicker() Picker {
	s.pickerMu.Lock()
	defer s.pickerMu.Unlock()
	return s.picker
}

// resolveOwnID caches the desktop's own identity per-token (one /auth/me per token).
func (s *Server) resolveOwnID(ctx context.Context, token string) (string, error) {
	s.ownIDMu.Lock()
	if s.ownIDToken == token && s.ownID != "" {
		id := s.ownID
		s.ownIDMu.Unlock()
		return id, nil
	}
	s.ownIDMu.Unlock()
	id, err := s.api.WhoAmI(ctx, token)
	if err == nil && id != "" {
		s.ownIDMu.Lock()
		s.ownIDToken, s.ownID = token, id
		s.ownIDMu.Unlock()
	}
	return id, err
}

// isAllowedOrigin mirrors the Electron allowlist: https rave.page (+subdomains), or
// localhost / 127.0.0.1 for dev.
func isAllowedOrigin(o string) bool {
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if u.Scheme == "https" && (h == "rave.page" || strings.HasSuffix(h, ".rave.page")) {
		return true
	}
	return h == "localhost" || h == "127.0.0.1"
}

// Start binds the first free loopback port in the range and serves until Stop.
func (s *Server) Start() error {
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.instanceID = encB64url(randomBytes(8))
	s.pid = os.Getpid()
	s.bootTime = nowMS()

	ln, port, err := listenRange()
	if err != nil {
		s.log.Warn(source, "no free port in range; studio channel disabled", map[string]any{"range": portRange})
		return err
	}
	s.ln, s.port = ln, port

	s.httpSrv = &http.Server{Handler: http.HandlerFunc(s.handle), ReadHeaderTimeout: 5 * time.Second}
	debuglog.Go(s.log, source, func() {
		if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Warn(source, "studio server stopped", map[string]any{"error": err.Error()})
		}
	})
	debuglog.Go(s.log, source, s.heartbeat)
	s.log.Info(source, "studio channel listening", map[string]any{"addr": "ws://127.0.0.1:" + strconv.Itoa(port)})
	return nil
}

// Stop tears down all sessions and the listener.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	close(s.hbStop)
	if s.hub != nil {
		s.hub.CancelAll()
	}
	s.CloseAll("shutdown")
	if s.httpSrv != nil {
		ctx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		_ = s.httpSrv.Shutdown(ctx)
	}
}

func listenRange() (net.Listener, int, error) {
	var lastErr error
	for _, p := range portRange {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	cors := func() {
		ao := "null"
		if isAllowedOrigin(origin) {
			ao = origin
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", ao)
		h.Set("Access-Control-Allow-Private-Network", "true") // Chrome PNA preflight
		h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "*")
		h.Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		cors()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !isAllowedOrigin(origin) {
		cors()
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	// InsecureSkipVerify here = skip coder/websocket's built-in *Origin* same-origin
	// check (NOT TLS - this is a plaintext loopback ws:// server). Safe: isAllowedOrigin
	// above is our strict allowlist, vetted before every upgrade (parity w/ Electron).
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	s.ServeConn(s.ctx, newWSConn(c), origin, TransportLoopback)
}

// ServeConn runs one studio session over any Conn until it closes. The loopback listener wraps
// its websocket; the account bridge hands us a relay-backed Conn (already E2E-encrypted and
// bound to a gate-authorized peer). The protocol is identical either way - do not fork it.
//
// origin is the claimed web origin; it is still checked against the allowlist in the
// client-hello (isAllowedOrigin). Over the bridge there is no HTTP Origin header, so the caller
// supplies the origin the peer claims - which is advisory there: the real guarantees are the
// ECDH+HMAC channel, the mutual /auth/me identity match, and the authz gate that let the peer
// reach us at all.
//
// Blocking: call it on your own goroutine.
func (s *Server) ServeConn(ctx context.Context, c Conn, origin, transport string) {
	sess := &session{
		srv: s, conn: c, origin: origin, transport: transport,
		state: "await-hello", recvSeq: -1,
	}
	sess.lastSeen.Store(nowMS())
	for {
		data, err := c.Recv(ctx)
		if err != nil {
			break
		}
		s.route(ctx, sess, data)
	}
	s.removeSession(sess)
	sess.detachAllSubs()
	if sess.expiryT != nil {
		sess.expiryT.Stop()
	}
}

func (s *Server) route(ctx context.Context, sess *session, data []byte) {
	var tag frameTag
	if json.Unmarshal(data, &tag) != nil {
		return
	}
	switch sess.state {
	case "await-hello":
		if tag.T == "client-hello" {
			var h clientHello
			if json.Unmarshal(data, &h) == nil {
				sess.onClientHello(data, h)
			}
		}
	case "await-auth":
		if tag.T == "client-auth" {
			var a clientAuth
			if json.Unmarshal(data, &a) == nil {
				sess.onClientAuth(ctx, data, a)
			}
		}
	case "open":
		m, err := parseMapNum(data)
		if err != nil {
			return
		}
		if tag.Seq == nil || *tag.Seq <= sess.recvSeq {
			s.closeSession(sess, closeProtocolError, "seq")
			return
		}
		if !sess.verifyFrameMac(m) {
			s.closeSession(sess, closeProtocolError, "mac")
			return
		}
		sess.recvSeq = *tag.Seq
		sess.lastSeen.Store(nowMS())
		sess.handleData(m)
	}
}

func parseMapNum(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	err := dec.Decode(&m)
	return m, err
}

// ── session set ──────────────────────────────────────────────────────────────

func (s *Server) addSession(sess *session) {
	s.mu.Lock()
	s.sessions[sess] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) removeSession(sess *session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}

func (s *Server) hasSession(sess *session) bool {
	s.mu.Lock()
	_, ok := s.sessions[sess]
	s.mu.Unlock()
	return ok
}

func (s *Server) closeSession(sess *session, code int, reason string) {
	s.removeSession(sess)
	if sess.expiryT != nil {
		sess.expiryT.Stop()
	}
	sess.closeWS(code, reason)
}

func (s *Server) snapshotSessions() []*session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}

// CloseAll closes every open session (logout / shutdown).
func (s *Server) CloseAll(reason string) {
	for _, sess := range s.snapshotSessions() {
		s.closeSession(sess, closeLoggedOut, reason)
	}
}

// OnDesktopTokenChanged re-resolves the desktop identity and tears down sessions that no
// longer match (logout / account switch). Wire to auth.Manager.OnChange.
func (s *Server) OnDesktopTokenChanged() {
	s.ownIDMu.Lock()
	s.ownIDToken, s.ownID = "", "" // force re-resolve
	s.ownIDMu.Unlock()

	token := s.desktopToken()
	if token == "" {
		if s.autos != nil {
			s.autos.SetBackgroundCredentials("", "") // don't leak creds across logout
		}
		s.CloseAll("desktop-logged-out")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	newID, err := s.resolveOwnID(ctx, token)
	if err != nil || newID == "" {
		s.CloseAll("desktop-identity-unresolved")
		return
	}
	for _, sess := range s.snapshotSessions() {
		if sess.sub != newID {
			s.closeSession(sess, closeSubMismatch, "account-changed")
		}
	}
}

func (s *Server) heartbeat() {
	t := time.NewTicker(time.Duration(heartbeatMS) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.hbStop:
			return
		case <-t.C:
			now := nowMS()
			for _, sess := range s.snapshotSessions() {
				if now-sess.lastSeen.Load() > deadMS {
					s.closeSession(sess, closeGoingAway, "timeout")
					continue
				}
				sess.send(map[string]any{"t": "ping", "ts": now})
			}
		}
	}
}
