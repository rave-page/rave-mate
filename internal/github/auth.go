// Package github links a GitHub account (Device Code Flow or pasted PAT, `gist`
// scope) and provides a stdlib gist CRUD client. Token sealed at rest via
// shared/secureseal (github.bin), never logged.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	source      = "github"
	oauthBase   = "https://github.com/login"
	apiBase     = "https://api.github.com"
	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	tokenFile   = "github.bin" // sealed token blob (DPAPI on Windows)
	// Scope: gists only - least privilege for publish.
	scope = "gist"
)

// Token is the persisted credential (sealed at rest). GitHub OAuth-app tokens
// don't expire by default - no refresh machinery.
type Token struct {
	Access string `json:"access"`
	Login  string `json:"login"` // GitHub username (gist raw-URL owner segment)
	PAT    bool   `json:"pat"`   // pasted personal access token (vs device flow)
}

func (t Token) valid() bool { return t.Access != "" }

// DeviceAuth is the device-code prompt (open VerificationURI, enter UserCode).
type DeviceAuth struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Interval        int
	ExpiresIn       int
}

// Auth runs the Device Code Flow / holds a pasted PAT.
type Auth struct {
	clientID func() string // config-resolved OAuth app client id ("" = PAT only)
	log      *logbus.Bus
	hc       *http.Client

	oauth string // test override for oauthBase
	api   string // test override for apiBase

	mu  sync.Mutex
	tok Token
}

// NewAuth builds an auth client; clientID resolves the (public) OAuth app id.
func NewAuth(clientID func() string, log *logbus.Bus) *Auth {
	return &Auth{clientID: clientID, log: log, hc: &http.Client{Timeout: 20 * time.Second}, oauth: oauthBase, api: apiBase}
}

// Load reads the sealed token from disk. True if a token was loaded.
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

// SignedIn reports whether a token is held.
func (a *Auth) SignedIn() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tok.valid()
}

// Login returns the linked GitHub username ("" when signed out).
func (a *Auth) Login() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tok.Login
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

// Token returns the access token ("" error when signed out).
func (a *Auth) Token() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.tok.valid() {
		return "", fmt.Errorf("github: not linked")
	}
	return a.tok.Access, nil
}

// SetPAT validates a pasted personal access token (needs gist scope) and stores it.
func (a *Auth) SetPAT(ctx context.Context, pat string) error {
	pat = strings.TrimSpace(pat)
	if pat == "" {
		return fmt.Errorf("github: empty token")
	}
	login, err := a.fetchLogin(ctx, pat)
	if err != nil {
		return err
	}
	a.store(Token{Access: pat, Login: login, PAT: true})
	return nil
}

// StartDevice requests a device + user code. Errors when no client id configured.
func (a *Auth) StartDevice(ctx context.Context) (DeviceAuth, error) {
	cid := a.clientID()
	if cid == "" {
		return DeviceAuth{}, fmt.Errorf("github: no OAuth client id configured - paste a token instead")
	}
	form := url.Values{"client_id": {cid}, "scope": {scope}}
	var r struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
		Error           string `json:"error"`
	}
	if err := a.postForm(ctx, a.oauth+"/device/code", form, &r); err != nil {
		return DeviceAuth{}, err
	}
	if r.DeviceCode == "" {
		return DeviceAuth{}, fmt.Errorf("github device: %s", r.Error)
	}
	if r.Interval < 5 {
		r.Interval = 5
	}
	return DeviceAuth{
		DeviceCode: r.DeviceCode, UserCode: r.UserCode, VerificationURI: r.VerificationURI,
		Interval: r.Interval, ExpiresIn: r.ExpiresIn,
	}, nil
}

// PollDevice polls until approval (stores token + login), expiry, or ctx cancel.
func (a *Auth) PollDevice(ctx context.Context, da DeviceAuth) error {
	interval := time.Duration(da.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(max(da.ExpiresIn, 300)) * time.Second)
	form := url.Values{
		"client_id":   {a.clientID()},
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
			return fmt.Errorf("github device code expired")
		}
		var r struct {
			Access string `json:"access_token"`
			Error  string `json:"error"`
		}
		_ = a.postForm(ctx, a.oauth+"/oauth/access_token", form, &r)
		switch {
		case r.Access != "":
			login, err := a.fetchLogin(ctx, r.Access)
			if err != nil {
				return err
			}
			a.store(Token{Access: r.Access, Login: login})
			a.log.Info(source, "linked", map[string]any{"login": login})
			return nil
		case r.Error == "slow_down":
			interval += 5 * time.Second
		case r.Error == "authorization_pending", r.Error == "":
			// keep waiting
		case r.Error == "expired_token":
			return fmt.Errorf("github device code expired")
		default:
			return fmt.Errorf("github auth: %s", r.Error)
		}
		t.Reset(interval)
	}
}

// fetchLogin resolves the token's GitHub username via GET /user.
func (a *Auth) fetchLogin(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.api+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github: token check HTTP %d", resp.StatusCode)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil || u.Login == "" {
		return "", fmt.Errorf("github: could not resolve user")
	}
	return u.Login, nil
}

// store persists + holds a token (sealed; in-memory only without a secure store).
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

// postForm POSTs form values, decodes the JSON response (GitHub returns JSON
// errors with Accept: application/json).
func (a *Auth) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return json.NewDecoder(resp.Body).Decode(out)
}
