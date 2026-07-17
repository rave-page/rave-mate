package vrchat

import (
	"context"
	"errors"
	"strings"
	"sync"

	"rave.page/mate/internal/logbus"
)

// State is the account snapshot the UI renders.
type State struct {
	LoggedIn    bool
	Awaiting2FA bool
	Methods     []string // pending 2FA methods
	UserID      string
	DisplayName string
	Message     string // last auth outcome, human-readable
	// Via names the paired instance serving this session (vrchat federation);
	// "" = the session is local. Federated state is read-through: every feature
	// works, but login/2FA/logout stay on the serving instance.
	Via string
}

// storer abstracts sessionStore for tests.
type storer interface {
	load() persistedSession
	save(persistedSession)
	clear()
}

// Manager is the VRChat account state machine: logged-out → awaiting-2FA →
// logged-in. Owns the Client + sealed session persistence. Passwords pass
// through Login once and are never kept.
type Manager struct {
	log      *logbus.Bus
	cli      *Client
	store    storer
	remember func() bool // config: persist session at rest

	mu       sync.RWMutex
	state    State
	onChange []func(State) // hooks (app wiring + UI), called outside locks
	onUnlink []func()      // explicit user unlink (not session expiry)

	// vrchat federation: when THIS instance has no local session but a paired
	// instance does, fedCli (a Client whose transport tunnels through the peer)
	// + fedState make every State()/Client() consumer work as if logged in
	// locally. The local session, once it appears, always wins.
	fedCli   *Client
	fedState State
}

// NewManager wires client + sealed store. remember gates at-rest persistence.
func NewManager(log *logbus.Bus, remember func() bool) *Manager {
	return &Manager{log: log, cli: New(log), store: &sessionStore{log: log}, remember: remember}
}

// Client exposes the API client every feature calls. With no local session but an
// armed federation, this is the peer-tunneled client - callers can't tell the
// difference (same funnel, the peer executes with its own cookies).
func (m *Manager) Client() *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.state.LoggedIn && m.fedCli != nil {
		return m.fedCli
	}
	return m.cli
}

// LocalClient always returns the local-session client (login/2FA/pipeline wiring -
// auth flows must never tunnel through a peer).
func (m *Manager) LocalClient() *Client { return m.cli }

// SetFederated arms read-through federation: cli tunnels through the serving peer,
// st describes that peer's session (Via = peer name). No-op state-wise while a
// LOCAL session is live (local always wins; the federation sits dormant behind it).
func (m *Manager) SetFederated(cli *Client, st State) {
	st.Via, st.LoggedIn = strings.TrimSpace(st.Via), true
	m.mu.Lock()
	m.fedCli = cli
	m.fedState = st
	local := m.state.LoggedIn
	fns := append([]func(State){}, m.onChange...)
	m.mu.Unlock()
	if !local {
		for _, fn := range fns {
			fn(st)
		}
	}
}

// ClearFederated drops the federation (serving peer gone/unlinked).
func (m *Manager) ClearFederated() {
	m.mu.Lock()
	had := m.fedCli != nil && !m.state.LoggedIn
	m.fedCli = nil
	m.fedState = State{}
	st := m.state
	fns := append([]func(State){}, m.onChange...)
	m.mu.Unlock()
	if had {
		for _, fn := range fns {
			fn(st)
		}
	}
}

// OnChange registers a state hook (appends - app wiring + UI both listen).
func (m *Manager) OnChange(fn func(State)) {
	m.mu.Lock()
	m.onChange = append(m.onChange, fn)
	m.mu.Unlock()
}

// State returns the current snapshot. Without a local session an armed federation
// answers instead (LoggedIn=true, Via=<peer>) so every gate lights up. An
// IN-PROGRESS local auth (Awaiting2FA) is never masked - the 2FA form must win.
func (m *Manager) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.state.LoggedIn && !m.state.Awaiting2FA && m.fedCli != nil {
		return m.fedState
	}
	return m.state
}

// LocalState reports only the LOCAL session (federation watcher + auth flows).
func (m *Manager) LocalState() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *Manager) setState(s State) {
	m.mu.Lock()
	m.state = s
	fns := append([]func(State){}, m.onChange...)
	m.mu.Unlock()
	for _, fn := range fns {
		fn(s)
	}
}

// Resume restores a sealed session and validates it against /auth/user.
// Returns true when logged in.
func (m *Manager) Resume(ctx context.Context) bool {
	ps := m.store.load()
	if ps.Auth == "" {
		return false
	}
	m.cli.SetCookies(ps.Auth, ps.TwoFactor)
	u, err := m.cli.CurrentUser(ctx)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, Err2FARequired) {
			// Session is dead - drop it. Network errors keep the blob for retry.
			m.cli.ClearSession()
			m.store.clear()
			m.setState(State{Message: "session expired - sign in again"})
		} else {
			m.setState(State{Message: "VRChat unreachable"})
		}
		return false
	}
	m.persistSession(u)
	m.setState(State{LoggedIn: true, UserID: u.ID, DisplayName: u.DisplayName, Message: "session restored"})
	return true
}

// Login runs the credential flow. State moves to logged-in or awaiting-2FA.
func (m *Manager) Login(ctx context.Context, username, password string) (State, error) {
	res, err := m.cli.Login(ctx, username, password)
	if err != nil {
		st := State{Message: "login failed"}
		if errors.Is(err, ErrUnauthorized) {
			st.Message = "invalid credentials"
		}
		m.setState(st)
		return st, err
	}
	if res.Requires2FA {
		st := State{Awaiting2FA: true, Methods: res.Methods, Message: "enter your 2FA code"}
		m.setState(st)
		return st, nil
	}
	m.persistSession(res.User)
	st := State{LoggedIn: true, UserID: res.User.ID, DisplayName: res.User.DisplayName, Message: "signed in"}
	m.setState(st)
	return st, nil
}

// Verify2FA completes the pending factor, then confirms via /auth/user.
func (m *Manager) Verify2FA(ctx context.Context, method, code string) (State, error) {
	if err := m.cli.Verify2FA(ctx, method, code); err != nil {
		st := m.State()
		st.Message = "2FA code rejected"
		m.setState(st)
		return st, err
	}
	u, err := m.cli.CurrentUser(ctx)
	if err != nil {
		st := State{Message: "2FA ok but session check failed"}
		m.setState(st)
		return st, err
	}
	m.persistSession(u)
	st := State{LoggedIn: true, UserID: u.ID, DisplayName: u.DisplayName, Message: "signed in"}
	m.setState(st)
	return st, nil
}

// OnUnlink registers a hook fired only on explicit user unlink.
func (m *Manager) OnUnlink(fn func()) {
	m.mu.Lock()
	m.onUnlink = append(m.onUnlink, fn)
	m.mu.Unlock()
}

// Unlink logs out server-side (best-effort) and wipes local session state.
func (m *Manager) Unlink(ctx context.Context) {
	_ = m.cli.Logout(ctx)
	m.store.clear()
	m.setState(State{Message: "unlinked"})
	m.mu.RLock()
	fns := append([]func(){}, m.onUnlink...)
	m.mu.RUnlock()
	for _, fn := range fns {
		fn()
	}
}

// persistSession seals the cookies when remember is on.
func (m *Manager) persistSession(u *User) {
	if m.remember != nil && !m.remember() {
		return
	}
	auth, tfa := m.cli.Cookies()
	m.store.save(persistedSession{Auth: auth, TwoFactor: tfa, UserID: u.ID, DisplayName: u.DisplayName})
}
