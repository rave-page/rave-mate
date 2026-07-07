package vrchat

import (
	"context"
	"errors"
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
}

// NewManager wires client + sealed store. remember gates at-rest persistence.
func NewManager(log *logbus.Bus, remember func() bool) *Manager {
	return &Manager{log: log, cli: New(log), store: &sessionStore{log: log}, remember: remember}
}

// Client exposes the underlying API client (pipeline cookie, future calls).
func (m *Manager) Client() *Client { return m.cli }

// OnChange registers a state hook (appends - app wiring + UI both listen).
func (m *Manager) OnChange(fn func(State)) {
	m.mu.Lock()
	m.onChange = append(m.onChange, fn)
	m.mu.Unlock()
}

// State returns the current snapshot.
func (m *Manager) State() State {
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
