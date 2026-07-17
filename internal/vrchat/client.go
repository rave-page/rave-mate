// Package vrchat is a minimal stdlib client for the VRChat API: login + 2FA +
// cookie session, current-user fetch, logout, and the realtime pipeline WS.
// Credentials are used once for the Basic-auth login and never stored or logged;
// only the resulting session cookies persist (sealed, see internal/vrchat store).
package vrchat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
)

const (
	apiBase = "https://api.vrchat.cloud/api/1"
	// VRChat API usage policy requires an identifying User-Agent with contact.
	userAgent = "rave-mate/1.0 (rave.page companion; contact@rave.page)"
	source    = "vrchat"
)

// Err2FARequired: login accepted but a second factor is pending.
var Err2FARequired = errors.New("vrchat: two-factor verification required")

// ErrUnauthorized: session invalid/expired or credentials rejected.
var ErrUnauthorized = errors.New("vrchat: unauthorized")

// User is the slice of the VRChat current-user object we consume.
type User struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"displayName"`
	Status            string   `json:"status"`
	StatusDescription string   `json:"statusDescription"`
	Bio               string   `json:"bio"`
	BioLinks          []string `json:"bioLinks"`
	// read-only profile fields (VRChat raw names; parity with the API DTO so the
	// web Profile-Info card + status-history suggestions don't regress).
	Pronouns      string   `json:"pronouns,omitempty"`
	State         string   `json:"state,omitempty"`
	LastPlatform  string   `json:"last_platform,omitempty"`
	LastLogin     string   `json:"last_login,omitempty"`
	LastActivity  string   `json:"last_activity,omitempty"`
	DateJoined    string   `json:"dateJoined,omitempty"`
	HomeLocation  string   `json:"homeLocation,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	StatusHistory []string `json:"statusHistory,omitempty"`
}

// LoginResult reports the outcome of a credential login.
type LoginResult struct {
	Authenticated bool
	Requires2FA   bool
	Methods       []string // "totp" | "otp" | "emailOtp"
	User          *User
}

// Client talks to api.vrchat.cloud. Session = auth + twoFactorAuth cookies, held
// explicitly (not a jar) so they can be sealed at rest and fed to the pipeline WS.
type Client struct {
	base string
	hc   *http.Client
	log  *logbus.Bus

	mu   sync.RWMutex
	auth string // "auth" cookie value
	tfa  string // "twoFactorAuth" cookie value
}

// New returns a client against the public VRChat API.
func New(log *logbus.Bus) *Client {
	return &Client{
		base: apiBase,
		hc:   &http.Client{Timeout: 20 * time.Second},
		log:  log,
	}
}

// NewWithTransport returns a client whose HTTP transport is rt. The vrchat
// federation runs a REAL Client here: rt tunnels every request through a paired
// instance (which executes it with its own session), so this client's cookie
// jar stays empty and the session never crosses the link.
func NewWithTransport(log *logbus.Bus, rt http.RoundTripper) *Client {
	c := New(log)
	c.hc = &http.Client{Timeout: 25 * time.Second, Transport: rt}
	return c
}

// Raw executes one API call against this client's session and returns status +
// body - the SERVING half of the vrchat federation proxy (remotectl
// vrchat.proxy). pathAndQuery is joined to the API base; the caller validates
// it is host-less. Bodies ride the same 1 MB read cap as every call (do()).
func (c *Client) Raw(ctx context.Context, method, pathAndQuery string, body []byte, contentType string) (int, []byte, error) {
	var rd io.Reader
	if len(body) > 0 {
		rd = bytes.NewReader(body)
	}
	req, err := c.newReq(ctx, method, pathAndQuery, rd)
	if err != nil {
		return 0, nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, respBody, err := c.do(req)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, respBody, nil
}

// Cookies returns the current session cookies (for sealing / pipeline auth).
func (c *Client) Cookies() (auth, twoFactor string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.auth, c.tfa
}

// SetCookies resumes a prior session (loaded from the sealed store).
func (c *Client) SetCookies(auth, twoFactor string) {
	c.mu.Lock()
	c.auth, c.tfa = auth, twoFactor
	c.mu.Unlock()
}

// ClearSession drops the in-memory cookies.
func (c *Client) ClearSession() { c.SetCookies("", "") }

// uriComponent percent-encodes exactly like JS encodeURIComponent, byte-for-byte:
// everything except A-Za-z0-9 and -_.!~*'() is %-escaped (UTF-8 bytes). VRChat's
// Basic-auth login expects this encoding (mirrors the web client). NOT url.QueryEscape -
// that escapes !*'() and encodes space as +, which mangles valid passwords → 401.
func uriComponent(s string) string {
	const safe = "-_.!~*'()"
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			strings.IndexByte(safe, c) >= 0:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0x0f])
		}
	}
	return b.String()
}

const upperHex = "0123456789ABCDEF"

// Login authenticates with username/password Basic auth. On Requires2FA the auth
// cookie is already captured - follow with Verify2FA. Credentials are not retained.
func (c *Client) Login(ctx context.Context, username, password string) (*LoginResult, error) {
	basic := base64.StdEncoding.EncodeToString([]byte(uriComponent(username) + ":" + uriComponent(password)))
	req, err := c.newReq(ctx, http.MethodGet, "/auth/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+basic)

	resp, body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, apiMessage(body))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("vrchat: login HTTP %d", resp.StatusCode)
	}

	var out struct {
		RequiresTwoFactorAuth []string `json:"requiresTwoFactorAuth"`
		User
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("vrchat: decode login: %w", err)
	}
	if len(out.RequiresTwoFactorAuth) > 0 {
		c.log.Info(source, "login: 2FA required", map[string]any{"methods": out.RequiresTwoFactorAuth})
		return &LoginResult{Requires2FA: true, Methods: out.RequiresTwoFactorAuth}, nil
	}
	u := out.User
	c.log.Info(source, "login ok", map[string]any{"displayName": u.DisplayName})
	return &LoginResult{Authenticated: true, User: &u}, nil
}

// tfaPaths maps 2FA method → verify endpoint.
var tfaPaths = map[string]string{
	"totp":     "/auth/twofactorauth/totp/verify",
	"otp":      "/auth/twofactorauth/otp/verify",
	"emailOtp": "/auth/twofactorauth/emailotp/verify",
}

// Verify2FA submits a second-factor code ("" method = totp). Captures the
// twoFactorAuth cookie on success.
func (c *Client) Verify2FA(ctx context.Context, method, code string) error {
	if method == "" {
		method = "totp"
	}
	p, ok := tfaPaths[method]
	if !ok {
		return fmt.Errorf("vrchat: unknown 2FA method %q", method)
	}
	payload, _ := json.Marshal(map[string]string{"code": code})
	req, err := c.newReq(ctx, http.MethodPost, p, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, body, err := c.do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s", ErrUnauthorized, apiMessage(body))
	}
	var out struct {
		Verified bool `json:"verified"`
	}
	if err := json.Unmarshal(body, &out); err != nil || !out.Verified {
		return fmt.Errorf("vrchat: 2FA rejected: %s", apiMessage(body))
	}
	c.log.Info(source, "2FA verified", map[string]any{"method": method})
	return nil
}

// CurrentUser fetches /auth/user with the session cookies. Err2FARequired if the
// session still needs a factor; ErrUnauthorized if expired.
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	req, err := c.newReq(ctx, http.MethodGet, "/auth/user", nil)
	if err != nil {
		return nil, err
	}
	resp, body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, apiMessage(body))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("vrchat: user HTTP %d", resp.StatusCode)
	}
	var out struct {
		RequiresTwoFactorAuth []string `json:"requiresTwoFactorAuth"`
		User
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("vrchat: decode user: %w", err)
	}
	if len(out.RequiresTwoFactorAuth) > 0 {
		return nil, Err2FARequired
	}
	u := out.User
	return &u, nil
}

// Logout invalidates the session server-side and drops local cookies.
func (c *Client) Logout(ctx context.Context) error {
	req, err := c.newReq(ctx, http.MethodPut, "/logout", nil)
	if err != nil {
		return err
	}
	_, _, err = c.do(req)
	c.ClearSession()
	return err
}

// newReq builds a request with UA + session cookies attached.
func (c *Client) newReq(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	auth, tfa := c.Cookies()
	if auth != "" {
		req.AddCookie(&http.Cookie{Name: "auth", Value: auth})
	}
	if tfa != "" {
		req.AddCookie(&http.Cookie{Name: "twoFactorAuth", Value: tfa})
	}
	return req, nil
}

// do executes, captures session Set-Cookies, reads the body, and logs redacted
// (method/path/status only - never headers, cookies, or bodies).
func (c *Client) do(req *http.Request) (*http.Response, []byte, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		c.log.Warn(source, "request failed", map[string]any{"method": req.Method, "path": req.URL.Path, "error": err.Error()})
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	c.captureCookies(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp, nil, err
	}
	fields := map[string]any{"method": req.Method, "path": req.URL.Path, "status": resp.StatusCode}
	lvl := logbus.Debug
	if resp.StatusCode >= 400 {
		lvl = logbus.Warn
		// Surface VRChat's own error text (error.message) - diagnostic, not sensitive.
		// Only error bodies are logged; 2xx bodies (which carry session data) never are.
		if msg := apiMessage(body); msg != "" {
			fields["vrcError"] = msg
		}
	}
	c.log.Log(lvl, source, "request", fields)
	return resp, body, nil
}

// captureCookies stores auth/twoFactorAuth Set-Cookies from the response.
func (c *Client) captureCookies(resp *http.Response) {
	for _, ck := range resp.Cookies() {
		switch ck.Name {
		case "auth":
			c.mu.Lock()
			c.auth = ck.Value
			c.mu.Unlock()
		case "twoFactorAuth":
			c.mu.Lock()
			c.tfa = ck.Value
			c.mu.Unlock()
		}
	}
}

// apiMessage extracts {"error":{"message":...}} for terse error text. VRChat
// double-encodes the message (a JSON string inside the JSON string), so the
// surrounding literal quotes are trimmed.
func apiMessage(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return strings.Trim(e.Error.Message, `"`)
	}
	return "request rejected"
}
