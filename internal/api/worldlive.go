package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// worldlive: rave.page's server-side gist publish API (the hosted WorldSync path). rave-mate PUTs
// the RAW module payload; the server envelopes it, owns the seq, and writes the gist under
// rave.page's OWN service account - rave-mate needs no gist token in this mode. Hand-written (the
// generated client doesn't cover these ops), routed through the redacted-logging doer (token never
// logged), Bearer = the user access token. The gateway forwards the principal, so user_id defaults
// to the caller (omitted). Shapes mirror workers/worldlive dto.WorldLiveModuleOut (snake_case);
// non-2xx bodies are RFC 7807 problem+json, mapped to *WorldLiveError - never a panic.

// WorldLiveModule mirrors dto.WorldLiveModuleOut. RawURL is STABLE across republishes (bake it
// into the world); Seq is the server-assigned monotonic SEQ-GATE value.
type WorldLiveModule struct {
	WorldID   string `json:"world_id"`
	UserID    string `json:"user_id"`
	Module    string `json:"module"`
	Schema    string `json:"schema"`
	GistID    string `json:"gist_id"`
	RawURL    string `json:"raw_url"`
	Seq       int64  `json:"seq"`
	UpdatedAt string `json:"updated_at"`
}

// WorldLiveError is a worldlive API failure carrying the HTTP status + parsed RFC-7807 fields
// (details.code / message / trace id). Surfaced as a Go error; rave-mate never throws.
type WorldLiveError struct {
	Status  int
	Code    string // problem details["code"], e.g. VALIDATION_FAILED / NOT_FOUND
	Message string // user-safe problem message
	TraceID string
}

func (e *WorldLiveError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("worldlive %d %s: %s", e.Status, e.Code, msg)
	}
	return fmt.Sprintf("worldlive %d: %s", e.Status, msg)
}

// NotFound reports a BOLA-safe 404 (not published / not the caller's).
func (e *WorldLiveError) NotFound() bool { return e.Status == http.StatusNotFound }

// PublishWorldLive PUTs a raw module payload; the server envelopes it + owns the seq. Returns the
// stable raw URL + seq. token = the rave.page user access token (Bearer).
func (c *Client) PublishWorldLive(ctx context.Context, token, worldID, module string, payload []byte) (WorldLiveModule, error) {
	resp, err := c.doWorldLive(ctx, http.MethodPut, token, c.worldLivePath(worldID, module), payload)
	if err != nil {
		return WorldLiveModule{}, err
	}
	var out WorldLiveModule
	err = decode(resp, &out)
	return out, err
}

// GetWorldLive reads one published module (BOLA-safe 404 when absent).
func (c *Client) GetWorldLive(ctx context.Context, token, worldID, module string) (WorldLiveModule, error) {
	resp, err := c.doWorldLive(ctx, http.MethodGet, token, c.worldLivePath(worldID, module), nil)
	if err != nil {
		return WorldLiveModule{}, err
	}
	var out WorldLiveModule
	err = decode(resp, &out)
	return out, err
}

// ListWorldLive lists every published module for the caller's (world, user).
func (c *Client) ListWorldLive(ctx context.Context, token, worldID string) ([]WorldLiveModule, error) {
	resp, err := c.doWorldLive(ctx, http.MethodGet, token, c.base+"/worlds/"+url.PathEscape(worldID)+"/live", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []WorldLiveModule `json:"items"`
	}
	if err := decode(resp, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// DeleteWorldLive removes a published module (gist + mapping).
func (c *Client) DeleteWorldLive(ctx context.Context, token, worldID, module string) (deleted bool, gistID string, err error) {
	resp, err := c.doWorldLive(ctx, http.MethodDelete, token, c.worldLivePath(worldID, module), nil)
	if err != nil {
		return false, "", err
	}
	var out struct {
		Deleted bool   `json:"deleted"`
		GistID  string `json:"gist_id"`
	}
	if err := decode(resp, &out); err != nil {
		return false, "", err
	}
	return out.Deleted, out.GistID, nil
}

func (c *Client) worldLivePath(worldID, module string) string {
	return c.base + "/worlds/" + url.PathEscape(worldID) + "/live/" + url.PathEscape(module)
}

// doWorldLive issues an authed worldlive request through the redacted-logging doer. On non-2xx it
// returns *WorldLiveError from the problem+json body (and closes the body); on 2xx it hands the
// live response to the caller's decode.
func (c *Client) doWorldLive(ctx context.Context, method, token, reqURL string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, worldLiveError(resp)
	}
	return resp, nil
}

// worldLiveError maps a non-2xx worldlive response to *WorldLiveError from its RFC-7807
// problem+json body (message/trace_id + details.code), falling back to the raw snippet when the
// body isn't problem-shaped.
func worldLiveError(resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	e := &WorldLiveError{Status: resp.StatusCode}
	var p struct {
		Message string         `json:"message"`
		TraceID string         `json:"trace_id"`
		Details map[string]any `json:"details"`
	}
	if json.Unmarshal(raw, &p) == nil {
		e.Message, e.TraceID = p.Message, p.TraceID
		if code, ok := p.Details["code"].(string); ok {
			e.Code = code
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(raw))
	}
	return e
}
