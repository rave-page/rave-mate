package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Gist is the slice of the GitHub gist object we consume.
type Gist struct {
	ID      string                 `json:"id"`
	HTMLURL string                 `json:"html_url"`
	Owner   struct{ Login string } `json:"owner"`
	Files   map[string]GistFile    `json:"files"`
}

// GistFile is one file within a gist.
type GistFile struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	RawURL    string `json:"raw_url"` // revision-pinned - do NOT hand to worlds
}

// RawURL is the latest-revision raw URL for a gist file - the URL worlds poll
// (gist.githubusercontent.com is VRChat string-allowlisted). CDN-cached ~5 min.
func RawURL(owner, gistID, file string) string {
	return fmt.Sprintf("https://gist.githubusercontent.com/%s/%s/raw/%s", owner, gistID, file)
}

// Gists is a minimal stdlib gist CRUD client.
type Gists struct {
	auth *Auth
	hc   *http.Client
	base string // test override for apiBase
}

// NewGists builds a gist client over the linked account.
func NewGists(auth *Auth) *Gists {
	return &Gists{auth: auth, hc: &http.Client{Timeout: 30 * time.Second}, base: auth.api}
}

// Create makes a new gist (secret when public=false: unlisted, readable by URL).
func (g *Gists) Create(ctx context.Context, desc string, files map[string]string, public bool) (*Gist, error) {
	body := map[string]any{"description": desc, "public": public, "files": wrapFiles(files)}
	return g.do(ctx, http.MethodPost, "/gists", body, http.StatusCreated)
}

// Update patches files/description of an existing gist. A nil file content in
// files deletes that file; here all values are set as content.
func (g *Gists) Update(ctx context.Context, id, desc string, files map[string]string) (*Gist, error) {
	body := map[string]any{"files": wrapFiles(files)}
	if desc != "" {
		body["description"] = desc
	}
	return g.do(ctx, http.MethodPatch, "/gists/"+id, body, http.StatusOK)
}

// Get fetches a gist (content included for non-truncated files).
func (g *Gists) Get(ctx context.Context, id string) (*Gist, error) {
	return g.do(ctx, http.MethodGet, "/gists/"+id, nil, http.StatusOK)
}

// Delete removes a gist.
func (g *Gists) Delete(ctx context.Context, id string) error {
	req, err := g.newReq(ctx, http.MethodDelete, "/gists/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := g.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("github: delete gist HTTP %d", resp.StatusCode)
	}
	return nil
}

func wrapFiles(files map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(files))
	for name, content := range files {
		out[name] = map[string]string{"content": content}
	}
	return out
}

func (g *Gists) do(ctx context.Context, method, path string, body any, want int) (*Gist, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := g.newReq(ctx, method, path, rd)
	if err != nil {
		return nil, err
	}
	resp, err := g.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != want {
		return nil, fmt.Errorf("github: %s %s HTTP %d: %s", method, path, resp.StatusCode, apiMessage(respBody))
	}
	var out Gist
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("github: decode gist: %w", err)
	}
	return &out, nil
}

// newReq builds an authed API request (token never logged).
func (g *Gists) newReq(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	tok, err := g.auth.Token()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// apiMessage extracts {"message": ...} from a GitHub error body.
func apiMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return e.Message
	}
	return "request rejected"
}
