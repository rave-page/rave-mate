// Package auth is the shared browser-deeplink login flow for the native rave.page apps,
// ported from the upstream web client. The browser holds the Zitadel session; the
// desktop app never sees a password.
//
// Flow: open {website}/desktop/bridge in the real browser → user signs in there → the
// bridge mints a one-time grant (POST /auth/grant, browser session) and redirects to
// rave://auth/callback?code=…&api=… → the OS hands that deeplink to the app →
// we exchange the code (POST /auth/exchange, anonymous) for access + refresh tokens.
//
// Host apps inject their own token exchange (Exchanger), logging (Logger), and config-dir
// resolver (DataPath) so this package drags in no rave-mate/rave-app internals.
package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const source = "auth"

// Logger is the host app's log sink (satisfied by rave-mate's *logbus.Bus).
type Logger interface {
	Info(source, msg string, f map[string]any)
	Warn(source, msg string, f map[string]any)
	Error(source, msg string, f map[string]any)
}

// Exchanger trades a one-time grant code for access + refresh tokens (satisfied by
// rave-mate's *api.Client). The implementation owns the HTTP call to POST /auth/exchange.
type Exchanger interface {
	ExchangeDesktopGrant(ctx context.Context, code string) (token, refresh string, err error)
}

// DataPath resolves an absolute path under the app's config dir (e.g. rave-mate's
// config.DataPath). Used to persist the sealed token blob.
type DataPath func(name string) (string, error)

// State is the current sign-in state broadcast to subscribers.
type State struct {
	SignedIn bool
	Token    string // access token (empty when signed out)
}

// Manager owns the login flow, token store, and sign-in state fan-out.
type Manager struct {
	exch    Exchanger
	log     Logger
	apiBase string
	store   *tokenStore

	mu       sync.Mutex
	onChange []func(State)
}

// NewManager builds the auth manager. apiBase is the resolved rave.page API base; dataPath
// resolves the config dir for the sealed token blob (pass nil to keep tokens in-memory only).
func NewManager(exch Exchanger, log Logger, apiBase string, dataPath DataPath) *Manager {
	return &Manager{
		exch:    exch,
		log:     log,
		apiBase: strings.TrimRight(apiBase, "/"),
		store:   newTokenStore(log, dataPath),
	}
}

// Restore loads a persisted token (encrypted at rest) so a restart stays signed in
// until the access token expires. Emits the restored state. No-op if nothing stored.
func (m *Manager) Restore() {
	if tok := m.store.load(); tok != "" {
		m.log.Info(source, "restored persisted session", nil)
		m.emit(State{SignedIn: true, Token: tok})
	}
}

// Login opens the browser bridge page. The callback returns via the deeplink handler.
func (m *Manager) Login() error {
	url := websiteBase(m.apiBase) + "/desktop/bridge"
	m.log.Info(source, "opening browser for sign-in", map[string]any{"url": url})
	return openBrowser(url)
}

// SignOut clears the in-memory + persisted token and broadcasts signed-out state.
func (m *Manager) SignOut() {
	m.store.clear()
	m.log.Info(source, "signed out", nil)
	m.emit(State{SignedIn: false})
}

// Token returns the current access token (empty if signed out).
func (m *Manager) Token() string { return m.store.token() }

// Refresh returns the current refresh token (empty if signed out / none held).
func (m *Manager) Refresh() string { return m.store.refresh() }

// SignedIn reports whether an access token is held.
func (m *Manager) SignedIn() bool { return m.store.token() != "" }

// SetTokens injects an access + refresh pair directly, bypassing the browser grant flow:
// persists them sealed and broadcasts signed-in state. Used for co-located handoff (rave-app
// signs a sibling rave-mate in with the same account's tokens). No-op if access is empty.
func (m *Manager) SetTokens(access, refresh string) {
	if access == "" {
		return
	}
	m.store.set(access, refresh)
	m.log.Info(source, "session adopted from handoff", nil)
	m.emit(State{SignedIn: true, Token: access})
}

// OnChange registers a sign-in state listener (fired on every state change).
func (m *Manager) OnChange(fn func(State)) {
	m.mu.Lock()
	m.onChange = append(m.onChange, fn)
	m.mu.Unlock()
}

func (m *Manager) emit(s State) {
	m.mu.Lock()
	fns := append([]func(State){}, m.onChange...)
	m.mu.Unlock()
	for _, fn := range fns {
		fn(s)
	}
}

// HandleDeepLink parses an auth callback deeplink, exchanges the grant code for tokens,
// persists them, and broadcasts the signed-in state. Safe to call from any goroutine.
func (m *Manager) HandleDeepLink(raw string) {
	cb, err := parseCallback(raw)
	if err != nil {
		m.log.Warn(source, "ignoring deeplink", map[string]any{"error": err.Error()})
		return
	}
	if cb.Err != "" {
		m.log.Error(source, "auth callback returned error", map[string]any{"error": cb.Err, "desc": cb.ErrDesc})
		return
	}
	if cb.Code == "" {
		m.log.Warn(source, "auth callback missing code", nil)
		return
	}
	if cb.API != "" && strings.TrimRight(cb.API, "/") != m.apiBase {
		// Browser targeted a different API host than we're configured for. Trust the
		// configured base (same env); a mismatch usually means a stale bridge link.
		m.log.Warn(source, "callback api differs from configured base", map[string]any{"callbackAPI": cb.API, "configured": m.apiBase})
	}

	m.goSafe(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		token, refresh, err := m.exch.ExchangeDesktopGrant(ctx, cb.Code)
		if err != nil {
			m.log.Error(source, "grant exchange failed", map[string]any{"error": err.Error()})
			return
		}
		m.store.set(token, refresh)
		m.log.Info(source, "signed in via browser bridge", nil)
		m.emit(State{SignedIn: true, Token: token})
	})
}

// goSafe runs fn in a goroutine with a panic guard (inlined debuglog.Go): a panic in the
// async exchange logs to the bus instead of taking the host process down.
func (m *Manager) goSafe(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.log.Error(source, "panic in auth goroutine", map[string]any{"panic": fmt.Sprint(r)})
			}
		}()
		fn()
	}()
}

// websiteBase derives the public website base from the API base (api host → web host),
// mirroring the web repo's getWebsiteBaseUrl (e.g. development.api.rave.page →
// development.rave.page, api.rave.page → rave.page).
func websiteBase(apiBase string) string { return WebsiteBase(apiBase) }

// WebsiteBase maps an API base onto its website origin (api.rave.page → rave.page,
// {branch}.api.rave.page → {branch}.rave.page). Exported so UI surfaces can link to the
// site (terms, a published recording) without re-deriving the mapping.
func WebsiteBase(apiBase string) string {
	s := apiBase
	s = strings.Replace(s, "://api.rave.page", "://rave.page", 1)
	s = strings.Replace(s, ".api.rave.page", ".rave.page", 1)
	return strings.TrimRight(s, "/")
}
