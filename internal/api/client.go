// Package api wraps the generated apiclient (internal/apiclient, oapi-codegen) with
// rave-mate ergonomics: a redacted-logging HTTP Doer (logs method/path/status, never
// tokens or bodies) and correctly-shaped request bodies for the few endpoints whose
// OpenAPI schema mis-types freeform fields (streams ingest/create - see SUPPLY_CHAIN /
// openapi2_codegen_gap). Generated transport, hand-shaped bodies where the spec lies.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/apiclient"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/netstats"
)

const source = "api"

// Client targets one rave.page API base. Safe for concurrent use.
type Client struct {
	base          string
	gen           *apiclient.Client
	doer          *loggingDoer
	bulkDoer      *loggingDoer // longer timeout for bulk endpoints (library upload)
	log           *logbus.Bus
	netIn, netOut atomic.Uint64 // HTTP body bytes across both doers (dashboard NETWORK graph)
}

// New returns a client for base (trailing slash trimmed).
func New(base string, log *logbus.Bus) *Client {
	base = strings.TrimRight(base, "/")
	c := &Client{base: base, log: log}
	c.doer = &loggingDoer{inner: &http.Client{Timeout: 15 * time.Second}, log: log, in: &c.netIn, out: &c.netOut}
	c.bulkDoer = &loggingDoer{inner: &http.Client{Timeout: 60 * time.Second}, log: log, in: &c.netIn, out: &c.netOut}
	c.gen, _ = apiclient.NewClient(base, apiclient.WithHTTPClient(c.doer))
	return c
}

// NetTotals returns cumulative HTTP body bytes (response in, request out).
func (c *Client) NetTotals() (in, out uint64) { return c.netIn.Load(), c.netOut.Load() }

// WhoAmI resolves the canonical rave.page user id for a token via GET /auth/me. This is
// the authoritative, token-format-agnostic identity and doubles as token validation (the
// API rejects invalid/expired/revoked tokens). Used by the studio channel's mutual
// identity check. TLS is always verified.
func (c *Client) WhoAmI(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/auth/me", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := decode(resp, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// BaseURL returns the configured API base.
func (c *Client) BaseURL() string { return c.base }

// ── events (rave.page event windows; for organizing VRChat photos by event) ──

// Event is a rave.page event reduced to what photo-organizing needs: id, title, and the
// time window (parsed from EventOut.starts_at/ends_at, RFC3339).
type Event struct {
	ID    string
	Title string
	Start time.Time
	End   time.Time
}

// ListEvents fetches events from GET /events (EventOut: id/title/starts_at/ends_at). token
// may be empty (endpoint is anonymous-public → public events). organizerType+organizerID
// (optional) scope to one organizer; limit caps the page (server max 200). Hand-written GET
// like WhoAmI - the generated client doesn't include listEvents. Events with no parseable
// window are dropped (can't match a photo time).
func (c *Client) ListEvents(ctx context.Context, token, organizerType, organizerID string, limit int) ([]Event, error) {
	u := c.base + "/events"
	q := url.Values{}
	if organizerType != "" {
		q.Set("organizer_type", organizerType)
	}
	if organizerID != "" {
		q.Set("organizer_id", organizerID)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		StartsAt string `json:"starts_at"`
		EndsAt   string `json:"ends_at"`
	}
	if err := decode(resp, &raw); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(raw))
	for _, e := range raw {
		ev := Event{ID: e.ID, Title: e.Title}
		ev.Start, _ = time.Parse(time.RFC3339, e.StartsAt)
		ev.End, _ = time.Parse(time.RFC3339, e.EndsAt)
		if ev.Start.IsZero() || ev.End.IsZero() {
			continue // no usable window
		}
		out = append(out, ev)
	}
	return out, nil
}

// loggingDoer logs a redacted summary of every request (method, path, status). It never
// logs headers (Authorization), query, or body, so tokens/codes never reach the log.
// Request + response body bytes are counted into in/out for the dashboard NETWORK graph.
type loggingDoer struct {
	inner   *http.Client
	log     *logbus.Bus
	in, out *atomic.Uint64
}

func (d *loggingDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body = netstats.CountBody(req.Body, d.out)
	}
	resp, err := d.inner.Do(req)
	if err == nil && resp.Body != nil {
		resp.Body = netstats.CountBody(resp.Body, d.in)
	}
	fields := map[string]any{"method": req.Method, "path": req.URL.Path}
	if err != nil {
		fields["error"] = err.Error()
		d.log.Error(source, "request failed", fields)
		return resp, err
	}
	fields["status"] = resp.StatusCode
	if resp.StatusCode >= 400 {
		d.log.Warn(source, "non-2xx response", fields)
	} else {
		d.log.Info(source, "request ok", fields)
	}
	return resp, err
}

// bearer returns a RequestEditorFn that attaches a Bearer token; label is for logging only.
func bearer(token string) apiclient.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	}
}

// decode reads a 2xx JSON body into out; on non-2xx returns an error with a short snippet.
func decode(resp *http.Response, out any) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s -> %d: %s", resp.Request.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return fmt.Errorf("decode %s: %w", resp.Request.URL.Path, err)
	}
	return nil
}

// ── auth (desktop-grant exchange) ────────────────────────────────────────────

// ExchangeDesktopGrant trades a one-time grant code (minted in the browser by
// POST /auth/grant) for an access + refresh token pair. Anonymous-public.
func (c *Client) ExchangeDesktopGrant(ctx context.Context, code string) (token, refresh string, err error) {
	resp, err := c.gen.ExchangeDesktopGrant(ctx, apiclient.DesktopExchangeIn{Code: &code})
	if err != nil {
		return "", "", err
	}
	var out apiclient.DesktopExchangeOut
	if err := decode(resp, &out); err != nil {
		return "", "", err
	}
	if out.Token == nil || *out.Token == "" {
		return "", "", fmt.Errorf("exchange: no token in response")
	}
	c.log.Info(source, "desktop grant exchanged", map[string]any{"hasRefresh": out.Refresh != nil})
	if out.Refresh != nil {
		refresh = *out.Refresh
	}
	return *out.Token, refresh, nil
}

// ── streams (parity with electron streamPublisher.ts) ────────────────────────
// Bodies are hand-marshaled: the spec types `metadata`/`events` as []int, so the
// generated typed bodies are unusable; the WithBody methods take raw JSON instead.

// CreateStreamReq starts a live stream.
type CreateStreamReq struct {
	Title    string         `json:"title"`
	Kind     string         `json:"kind"`     // e.g. "dj_set"
	Source   string         `json:"source"`   // e.g. "traktor_pro_4"
	Metadata map[string]any `json:"metadata"` // {software: ...}
}

// CreateStreamResp is the publish grant.
type CreateStreamResp struct {
	StreamID              string `json:"stream_id"`
	PublishToken          string `json:"publish_token"`
	PublishTokenExpiresAt string `json:"publish_token_expires_at"`
	StartedAt             string `json:"started_at"`
}

// IngestEvent is one batched live-set event (deck/channel/master/heartbeat).
type IngestEvent struct {
	Type    string         `json:"type"`
	Deck    string         `json:"deck,omitempty"`
	Channel string         `json:"channel,omitempty"`
	State   map[string]any `json:"state,omitempty"`
	Seq     uint64         `json:"seq"`
	TS      int64          `json:"ts,omitempty"`
}

// CreateStream creates a stream with the user access token; returns the publish grant.
func (c *Client) CreateStream(ctx context.Context, userToken string, req CreateStreamReq) (CreateStreamResp, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return CreateStreamResp{}, err
	}
	resp, err := c.gen.CreateLiveStreamWithBody(ctx, "application/json", bytes.NewReader(body), bearer(userToken))
	if err != nil {
		return CreateStreamResp{}, err
	}
	var out CreateStreamResp
	err = decode(resp, &out)
	return out, err
}

// Ingest posts a batch of events with the publish token.
func (c *Client) Ingest(ctx context.Context, streamID, publishToken string, events []IngestEvent) error {
	body, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		return err
	}
	resp, err := c.gen.IngestLiveStreamEventsWithBody(ctx, streamID, "application/json", bytes.NewReader(body), bearer(publishToken))
	if err != nil {
		return err
	}
	return decode(resp, nil)
}

// Heartbeat keeps the stream alive (server reaper resets on this).
func (c *Client) Heartbeat(ctx context.Context, streamID, publishToken string) error {
	resp, err := c.gen.HeartbeatLiveStream(ctx, streamID, bearer(publishToken))
	if err != nil {
		return err
	}
	return decode(resp, nil)
}

// ── VRChat uplink (opt-in: share the VRChat session with rave.page) ──────────

// VrchatToken is the session material pushed to rave.page (server vaults it).
type VrchatToken struct {
	AuthToken   string
	TwoFactor   string
	UserID      string
	DisplayName string
}

// StoreVrchatToken vaults the VRChat session token server-side (opt-in uplink).
func (c *Client) StoreVrchatToken(ctx context.Context, userToken string, t VrchatToken) error {
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	resp, err := c.gen.StoreVrchatToken(ctx, apiclient.VRChatStoreTokenIn{
		AuthToken:          opt(t.AuthToken),
		TwoFactorAuthToken: opt(t.TwoFactor),
		VrchatUserId:       opt(t.UserID),
		VrchatDisplayName:  opt(t.DisplayName),
	}, bearer(userToken))
	if err != nil {
		return err
	}
	var out apiclient.VRChatStoreTokenOut
	if err := decode(resp, &out); err != nil {
		return err
	}
	if out.Stored == nil || !*out.Stored {
		return fmt.Errorf("vrchat token not stored by server")
	}
	return nil
}

// TestVrchatConnection asks rave.page whether its vaulted VRChat session works.
func (c *Client) TestVrchatConnection(ctx context.Context, userToken string) (bool, error) {
	resp, err := c.gen.TestVrchatConnection(ctx, bearer(userToken))
	if err != nil {
		return false, err
	}
	var out apiclient.VrchatTestResp
	if err := decode(resp, &out); err != nil {
		return false, err
	}
	return out.Connected != nil && *out.Connected, nil
}

// DeleteVrchatCredentials removes the vaulted VRChat session server-side.
func (c *Client) DeleteVrchatCredentials(ctx context.Context, userToken string) error {
	resp, err := c.gen.DeleteVrchatCredentials(ctx, bearer(userToken))
	if err != nil {
		return err
	}
	return decode(resp, nil)
}

// EndStream ends the stream.
func (c *Client) EndStream(ctx context.Context, streamID, publishToken string) error {
	resp, err := c.gen.EndLiveStream(ctx, streamID, bearer(publishToken))
	if err != nil {
		return err
	}
	return decode(resp, nil)
}
