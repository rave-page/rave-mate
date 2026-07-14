package matebridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ── gateway seams (the app implements these over vrchat.Manager / vrcperm.Service / config; NOT
//    wired yet - see the package doc). A nil gateway makes its route family return 501 and drops its
//    capability from /v1/health. Every method acts ONLY on rave-mate's local sanctioned surface; no
//    caller-supplied credential is ever accepted. ────────────────────────────────────────────────

// Directory is the VRChat friends/groups surface (privileged enumeration stays in rave-mate).
type Directory interface {
	Friends(ctx context.Context, offset, n int, offline bool) ([]Friend, error)
	Groups(ctx context.Context) ([]Group, error)
	GroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]GroupMember, bool, error) // (members, partial, err)
	Resolve(ctx context.Context, ids []string) ([]Resolved, error)
}

// Presets is the preset round-trip store.
type Presets interface {
	List(ctx context.Context, kind string, sinceSeq int64) ([]PresetEnvelope, error)
	Get(ctx context.Context, kind, id string) (*PresetEnvelope, error)
	Put(ctx context.Context, kind, id string, p PresetEnvelope) (int64, error) // returns assigned seq
}

// SettingsStore is the project-settings + rebuild-signal surface (edited while Unity is closed).
type SettingsStore interface {
	Settings(ctx context.Context, projectID string) (*Settings, error)
	RebuildSignals(ctx context.Context, sinceSeq int64) (int64, []RebuildSignal, error) // (highWater, signals, err)
}

// RosterPublisher hands a resolved display-name roster to rave-mate to publish as a gist (rave-mate
// owns the GitHub token). Returns (gistID, rawURL, jsonURL, seq).
type RosterPublisher interface {
	PublishRoster(ctx context.Context, kind, name string, names []string) (gistID, rawURL, jsonURL string, seq int64, err error)
}

// Options wires the server. Any nil gateway disables its routes gracefully. Version feeds /v1/health.
// DiscoveryPath is where the {port,token} handshake file is written (caller passes
// config.DataPath(DiscoveryFile)); "" skips the write (tests). Token overrides the auto-minted bearer.
type Options struct {
	Directory     Directory
	Presets       Presets
	Settings      SettingsStore
	Roster        RosterPublisher
	Version       string
	DiscoveryPath string
	Token         string
}

// Server is the edit-time loopback RPC server. Loopback-bound; a rave-mate-minted bearer token is the
// app-layer guard. Stateless request/response (NOT the account-bound Local Studio WS channel).
type Server struct {
	opt   Options
	token string
	mux   *http.ServeMux

	ln   net.Listener
	srv  *http.Server
	port int
}

// New builds the server. It does not bind until Start.
func New(opt Options) *Server {
	s := &Server{opt: opt, token: opt.Token}
	s.mux = s.routes()
	return s
}

// Start mints a token (if none supplied), binds the first free loopback port, writes the discovery
// file, and serves until Stop. Non-blocking.
func (s *Server) Start() error {
	if s.token == "" {
		s.token = mintToken()
	}
	ln, port, err := listenRange(PortRange)
	if err != nil {
		return err
	}
	s.ln, s.port = ln, port
	s.srv = &http.Server{Handler: s.auth(s.mux), ReadHeaderTimeout: 5 * time.Second}
	if err := s.writeDiscovery(); err != nil {
		_ = ln.Close()
		return err
	}
	go func() { _ = s.srv.Serve(ln) }() // TODO(matebridge): route Serve error to logbus once wired
	return nil
}

// Stop tears down the listener and removes the discovery file.
func (s *Server) Stop() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	}
	if s.opt.DiscoveryPath != "" {
		_ = os.Remove(s.opt.DiscoveryPath)
	}
}

// Port reports the bound port (0 before Start).
func (s *Server) Port() int { return s.port }

// Handler exposes the authenticated handler (for httptest without binding a real port).
func (s *Server) Handler() http.Handler { return s.auth(s.mux) }

// ── routing ──────────────────────────────────────────────────────────────────────────────────────

func (s *Server) routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET "+PathPrefix+"/health", s.health)
	m.HandleFunc("GET "+PathPrefix+"/vrchat/friends", s.friends)
	m.HandleFunc("GET "+PathPrefix+"/vrchat/groups", s.groups)
	m.HandleFunc("GET "+PathPrefix+"/vrchat/groups/{groupId}/members", s.groupMembers)
	m.HandleFunc("POST "+PathPrefix+"/vrchat/resolve", s.resolve)
	m.HandleFunc("GET "+PathPrefix+"/presets", s.presetList)
	m.HandleFunc("GET "+PathPrefix+"/presets/{kind}/{id}", s.presetGet)
	m.HandleFunc("PUT "+PathPrefix+"/presets/{kind}/{id}", s.presetPut)
	m.HandleFunc("GET "+PathPrefix+"/settings/{projectId}", s.settings)
	m.HandleFunc("GET "+PathPrefix+"/rebuild-signals", s.rebuildSignals)
	m.HandleFunc("POST "+PathPrefix+"/worldsync/gist", s.publishRoster)
	return m
}

// auth enforces the loopback bind + bearer token on every request except CORS preflight. /health is
// still token-gated: the editor always has the token from the discovery file, and an unauthenticated
// health probe would leak liveness to any local process.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ContractHeader, strconv.Itoa(ContractVersion))
		got := strings.TrimPrefix(r.Header.Get(AuthHeader), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeProblem(w, http.StatusUnauthorized, ProblemUnauthorized, "unauthorized",
				"missing or invalid bearer token - reopen the rave-mate editor bridge")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── handlers (plumbing is real; the gateways behind them are wired app-side later) ────────────────

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Health{
		OK:              true,
		RaveMateVersion: s.opt.Version,
		ContractVersion: ContractVersion,
		Capabilities:    s.capabilities(),
	})
}

func (s *Server) capabilities() []string {
	caps := []string{}
	if s.opt.Directory != nil {
		caps = append(caps, CapVRChat)
	}
	if s.opt.Roster != nil {
		caps = append(caps, CapWorldSync)
	}
	if s.opt.Presets != nil {
		caps = append(caps, CapPresets)
	}
	if s.opt.Settings != nil {
		caps = append(caps, CapSettings)
	}
	return caps
}

func (s *Server) friends(w http.ResponseWriter, r *http.Request) {
	if s.opt.Directory == nil {
		unavailable(w, "vrchat directory not available")
		return
	}
	out, err := s.opt.Directory.Friends(r.Context(), qi(r, "offset", 0), qi(r, "n", 60), qb(r, "offline"))
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, FriendsResponse{ContractVersion: ContractVersion, Friends: out})
}

func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	if s.opt.Directory == nil {
		unavailable(w, "vrchat directory not available")
		return
	}
	out, err := s.opt.Directory.Groups(r.Context())
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, GroupsResponse{ContractVersion: ContractVersion, Groups: out})
}

func (s *Server) groupMembers(w http.ResponseWriter, r *http.Request) {
	if s.opt.Directory == nil {
		unavailable(w, "vrchat directory not available")
		return
	}
	out, partial, err := s.opt.Directory.GroupMembers(r.Context(), r.PathValue("groupId"), r.URL.Query().Get("roleId"), qi(r, "offset", 0), qi(r, "n", 100))
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, GroupMembersResponse{ContractVersion: ContractVersion, Members: out, Partial: partial})
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	if s.opt.Directory == nil {
		unavailable(w, "vrchat directory not available")
		return
	}
	var req ResolveRequest
	if !decode(w, r, &req) {
		return
	}
	out, err := s.opt.Directory.Resolve(r.Context(), req.IDs)
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ResolveResponse{ContractVersion: ContractVersion, Resolved: out})
}

func (s *Server) presetList(w http.ResponseWriter, r *http.Request) {
	if s.opt.Presets == nil {
		unavailable(w, "presets not available")
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		badRequest(w, "kind query param required")
		return
	}
	out, err := s.opt.Presets.List(r.Context(), kind, qi64(r, "sinceSeq", 0))
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, PresetListResponse{ContractVersion: ContractVersion, Presets: out})
}

func (s *Server) presetGet(w http.ResponseWriter, r *http.Request) {
	if s.opt.Presets == nil {
		unavailable(w, "presets not available")
		return
	}
	p, err := s.opt.Presets.Get(r.Context(), r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		upstream(w, err)
		return
	}
	if p == nil {
		writeProblem(w, http.StatusNotFound, ProblemNotFound, "not found", "no such preset")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) presetPut(w http.ResponseWriter, r *http.Request) {
	if s.opt.Presets == nil {
		unavailable(w, "presets not available")
		return
	}
	var p PresetEnvelope
	if !decode(w, r, &p) {
		return
	}
	seq, err := s.opt.Presets.Put(r.Context(), r.PathValue("kind"), r.PathValue("id"), p)
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, PresetPutResponse{ContractVersion: ContractVersion, OK: true, Seq: seq})
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if s.opt.Settings == nil {
		unavailable(w, "settings not available")
		return
	}
	out, err := s.opt.Settings.Settings(r.Context(), r.PathValue("projectId"))
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) rebuildSignals(w http.ResponseWriter, r *http.Request) {
	if s.opt.Settings == nil {
		unavailable(w, "settings not available")
		return
	}
	hw, sigs, err := s.opt.Settings.RebuildSignals(r.Context(), qi64(r, "sinceSeq", 0))
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RebuildSignalsResponse{ContractVersion: ContractVersion, Seq: hw, Signals: sigs})
}

func (s *Server) publishRoster(w http.ResponseWriter, r *http.Request) {
	if s.opt.Roster == nil {
		unavailable(w, "worldsync gist publishing not available")
		return
	}
	var req PublishRosterRequest
	if !decode(w, r, &req) {
		return
	}
	gistID, rawURL, jsonURL, seq, err := s.opt.Roster.PublishRoster(r.Context(), req.Kind, req.Name, req.Names)
	if err != nil {
		upstream(w, err)
		return
	}
	writeJSON(w, http.StatusOK, PublishRosterResponse{
		ContractVersion: ContractVersion, GistID: gistID, RawURL: rawURL, JSONURL: jsonURL, Seq: seq,
	})
}

// ── discovery + transport helpers ─────────────────────────────────────────────────────────────────

// writeDiscovery writes the {port,token} handshake file 0600 (best-effort perms; the parent dir is
// already per-user). "" path => skip (tests / headless without a config dir).
func (s *Server) writeDiscovery() error {
	if s.opt.DiscoveryPath == "" {
		return nil
	}
	raw, err := json.MarshalIndent(Discovery{
		Schema:          DiscoverySchema,
		Port:            s.port,
		Token:           s.token,
		ContractVersion: ContractVersion,
		PID:             os.Getpid(),
		RaveMateVersion: s.opt.Version,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.opt.DiscoveryPath, append(raw, '\n'), 0o600)
}

func listenRange(ports []int) (net.Listener, int, error) {
	var lastErr error
	for _, p := range ports {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

func mintToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand; a short read is not expected on supported platforms
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(ContractHeader, strconv.Itoa(ContractVersion))
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, typ, title, detail string) {
	w.Header().Set("Content-Type", ProblemContentType)
	w.Header().Set(ContractHeader, strconv.Itoa(ContractVersion))
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: typ, Title: title, Status: status, Detail: detail, ContractVersion: ContractVersion})
}

// decode reads a JSON body into v; on failure it writes a 400 and returns false.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		badRequest(w, "malformed JSON body")
		return false
	}
	return true
}

// unavailable => 501: the feature family isn't wired/enabled on this rave-mate. The editor treats it
// as a soft-degraded state (grey the tool), never fatal.
func unavailable(w http.ResponseWriter, detail string) {
	writeProblem(w, http.StatusNotImplemented, ProblemNotImplemented, "not available", detail)
}

func badRequest(w http.ResponseWriter, detail string) {
	writeProblem(w, http.StatusBadRequest, ProblemBadRequest, "bad request", detail)
}

// upstream => 502: a downstream VRChat/GitHub call failed. Kept generic on purpose - no upstream error
// detail that could carry a token/cookie leaks to the editor.
func upstream(w http.ResponseWriter, _ error) {
	writeProblem(w, http.StatusBadGateway, ProblemUpstream, "upstream error", "a VRChat/GitHub call failed; retry later")
}

func qi(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func qi64(r *http.Request, key string, def int64) int64 {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func qb(r *http.Request, key string) bool {
	b, _ := strconv.ParseBool(r.URL.Query().Get(key))
	return b
}
