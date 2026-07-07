// Package twitch is rave-mate's Twitch integration: OAuth (Device Code Flow, no client secret),
// a Helix REST client (stream title/category, send chat, moderation), an EventSub WebSocket
// (chat + follows/subs/cheers over one socket), and a Manager that publishes decoded events onto
// the eventbus so other instances/overlays consume them. Tokens are sealed at rest (twitch.bin),
// never logged.
package twitch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/secureseal"
)

const (
	source      = "twitch"
	idBase      = "https://id.twitch.tv/oauth2"
	helixBase   = "https://api.twitch.tv/helix"
	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	tokenFile   = "twitch.bin" // sealed OAuth token blob (DPAPI on Windows)
	refreshLead = 90 * time.Second
)

// Scopes is the full OAuth scope set rave-mate requests (title control, chat r/w, follows, subs,
// bits, moderation).
var Scopes = []string{
	"channel:manage:broadcast",
	"user:write:chat",
	"user:read:chat",
	"moderator:read:followers",
	"moderator:read:chatters",
	"channel:read:subscriptions",
	"bits:read",
	"moderator:manage:banned_users",
	"moderator:manage:chat_messages",
}

// Token is the persisted OAuth token set (sealed at rest). Refresh tokens rotate - always persist
// the newest from a refresh response.
type Token struct {
	Access    string   `json:"access"`
	Refresh   string   `json:"refresh"`
	ExpiresAt int64    `json:"expiresAt"` // unix sec
	Scopes    []string `json:"scopes"`
}

func (t Token) valid() bool { return t.Access != "" }

// DeviceAuth is the device-code prompt shown to the user (open VerificationURI, enter UserCode).
type DeviceAuth struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Interval        int
	ExpiresIn       int
}

// Auth runs the Device Code Flow + keeps a valid access token (auto-refresh).
type Auth struct {
	clientID string
	log      *logbus.Bus
	hc       *http.Client

	mu  sync.Mutex
	tok Token
}

// NewAuth builds an auth client for the given (public) client id.
func NewAuth(clientID string, log *logbus.Bus) *Auth {
	return &Auth{clientID: clientID, log: log, hc: &http.Client{Timeout: 20 * time.Second}}
}

// Load reads the sealed token from disk (no-op on absence). Returns true if a token was loaded.
func (a *Auth) Load() bool {
	p, err := config.DataPath(tokenFile)
	if err != nil {
		return false
	}
	blob, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	clear, err := secureseal.Unseal(blob)
	if err != nil {
		a.log.Warn(source, "could not decrypt token", map[string]any{"error": err.Error()})
		return false
	}
	var t Token
	if json.Unmarshal(clear, &t) != nil || !t.valid() {
		return false
	}
	a.mu.Lock()
	a.tok = t
	a.mu.Unlock()
	return true
}

// SignedIn reports whether a (possibly expired but refreshable) token is held.
func (a *Auth) SignedIn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tok.valid()
}

// Logout clears the in-memory + on-disk token.
func (a *Auth) Logout() {
	a.mu.Lock()
	a.tok = Token{}
	a.mu.Unlock()
	if p, err := config.DataPath(tokenFile); err == nil {
		_ = os.Remove(p)
	}
}

// ClientID returns the configured client id (needed for the Helix Client-Id header).
func (a *Auth) ClientID() string { return a.clientID }

// StartDevice requests a device code + user code from Twitch.
func (a *Auth) StartDevice(ctx context.Context) (DeviceAuth, error) {
	form := url.Values{"client_id": {a.clientID}, "scopes": {strings.Join(Scopes, " ")}}
	var r struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
		Message         string `json:"message"`
	}
	if err := a.postForm(ctx, idBase+"/device", form, &r); err != nil {
		return DeviceAuth{}, err
	}
	if r.DeviceCode == "" {
		return DeviceAuth{}, fmt.Errorf("twitch device: %s", r.Message)
	}
	if r.Interval == 0 {
		r.Interval = 5
	}
	return DeviceAuth{
		DeviceCode: r.DeviceCode, UserCode: r.UserCode, VerificationURI: r.VerificationURI,
		Interval: r.Interval, ExpiresIn: r.ExpiresIn,
	}, nil
}

// PollDevice polls until the user approves (then persists + holds the token), the code expires, or
// ctx is cancelled.
func (a *Auth) PollDevice(ctx context.Context, da DeviceAuth) error {
	interval := time.Duration(da.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(max(da.ExpiresIn, 300)) * time.Second)
	form := url.Values{
		"client_id":   {a.clientID},
		"scopes":      {strings.Join(Scopes, " ")},
		"device_code": {da.DeviceCode},
		"grant_type":  {deviceGrant},
	}
	t := time.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("twitch device code expired")
		}
		var r tokenResp
		_ = a.postForm(ctx, idBase+"/token", form, &r)
		switch {
		case r.Access != "":
			a.store(r.token())
			a.log.Info(source, "signed in", nil)
			return nil
		case strings.Contains(r.errStr(), "slow_down"):
			interval += 5 * time.Second
		case strings.Contains(r.errStr(), "authorization_pending"), r.errStr() == "":
			// keep waiting
		case strings.Contains(r.errStr(), "expired"):
			return fmt.Errorf("twitch device code expired")
		default:
			return fmt.Errorf("twitch auth: %s", r.errStr())
		}
		t.Reset(interval)
	}
}

// Token returns a currently-valid access token, refreshing if it's near expiry.
func (a *Auth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	tok := a.tok
	a.mu.Unlock()
	if !tok.valid() {
		return "", fmt.Errorf("twitch: not signed in")
	}
	if time.Now().Add(refreshLead).Unix() < tok.ExpiresAt {
		return tok.Access, nil
	}
	if err := a.refresh(ctx); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tok.Access, nil
}

func (a *Auth) refresh(ctx context.Context) error {
	a.mu.Lock()
	rt := a.tok.Refresh
	a.mu.Unlock()
	if rt == "" {
		return fmt.Errorf("twitch: no refresh token")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {a.clientID},
	}
	var r tokenResp
	if err := a.postForm(ctx, idBase+"/token", form, &r); err != nil {
		return err
	}
	if r.Access == "" {
		return fmt.Errorf("twitch refresh: %s", r.errStr())
	}
	a.store(r.token())
	return nil
}

// store persists + holds a token.
func (a *Auth) store(t Token) {
	a.mu.Lock()
	a.tok = t
	a.mu.Unlock()
	raw, err := json.Marshal(t)
	if err != nil {
		return
	}
	sealed, err := secureseal.Seal(raw)
	if err != nil {
		a.log.Warn(source, "token not persisted (no secure store)", map[string]any{"error": err.Error()})
		return
	}
	p, err := config.DataPath(tokenFile)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, sealed, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// postForm POSTs url-encoded form values + decodes a JSON response. 2xx-or-not, the body is decoded
// (Twitch returns useful JSON errors), so callers inspect the decoded struct.
func (a *Auth) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return json.NewDecoder(resp.Body).Decode(out)
}

// tokenResp captures both success + error shapes of the /token endpoint.
type tokenResp struct {
	Access  string   `json:"access_token"`
	Refresh string   `json:"refresh_token"`
	Expires int      `json:"expires_in"`
	Scope   []string `json:"scope"`
	Message string   `json:"message"`
	Error   string   `json:"error"`
}

func (r tokenResp) errStr() string {
	if r.Error != "" {
		return r.Error
	}
	return r.Message
}

func (r tokenResp) token() Token {
	return Token{
		Access: r.Access, Refresh: r.Refresh,
		ExpiresAt: time.Now().Add(time.Duration(r.Expires) * time.Second).Unix(),
		Scopes:    r.Scope,
	}
}
