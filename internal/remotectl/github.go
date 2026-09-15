package remotectl

import (
	"context"
	"encoding/json"
	"fmt"

	"rave.page/mate/internal/github"
)

// ── github (World Sync) federation ─────────────────────────────────────────────
//
// One paired instance holds the GitHub link; a peer with World Sync enabled but no local link
// publishes world feeds through it (gist create/update/get/delete run on the serving box with
// ITS token). Security: state NEVER carries the token, and NO auth verb is registered - linking
// / unlinking stays local-only, so a borrower can never re-auth or read the token. Handlers gate
// on LocalSignedIn so a box that is itself borrowing never serves a third peer.

// GitHubStateResult is the link identity a borrower reads. It carries NO token.
type GitHubStateResult struct {
	SignedIn bool   `json:"signedIn"`
	Login    string `json:"login,omitempty"`
}

// GistCreateParams creates a gist (secret when public=false).
type GistCreateParams struct {
	Desc   string            `json:"desc"`
	Files  map[string]string `json:"files"`
	Public bool              `json:"public"`
}

// GistUpdateParams patches an existing gist's files/description.
type GistUpdateParams struct {
	ID    string            `json:"id"`
	Desc  string            `json:"desc,omitempty"`
	Files map[string]string `json:"files"`
}

// GitHubServer is the narrow view the github.* handlers serve from (satisfied by an app-side
// adapter over *github.Auth + *github.Gists). Auth-free: no token, no device-flow, no logout.
type GitHubServer interface {
	LocalSignedIn() bool
	Login() string
	Create(ctx context.Context, desc string, files map[string]string, public bool) (*github.Gist, error)
	Update(ctx context.Context, id, desc string, files map[string]string) (*github.Gist, error)
	Get(ctx context.Context, id string) (*github.Gist, error)
	Delete(ctx context.Context, id string) error
}

// RegisterGitHub serves this instance's GitHub link to paired peers. state always answers
// (signedIn=false without a link) so a borrower can discover the serving login; the gist verbs
// execute here with this box's token. state NEVER carries the token; no auth verb is exposed.
func RegisterGitHub(e *Endpoint, src GitHubServer) {
	if e == nil || src == nil {
		return
	}
	gate := func() error {
		if !src.LocalSignedIn() {
			return fmt.Errorf("github not linked on this peer")
		}
		return nil
	}
	e.Register(MethodGitHubState, func(context.Context, string, json.RawMessage) (any, error) {
		res := GitHubStateResult{SignedIn: src.LocalSignedIn()}
		if res.SignedIn {
			res.Login = src.Login()
		}
		return res, nil
	})
	e.Register(MethodGitHubGistCreate, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p GistCreateParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return src.Create(ctx, p.Desc, p.Files, p.Public)
	})
	e.Register(MethodGitHubGistUpdate, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p GistUpdateParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return src.Update(ctx, p.ID, p.Desc, p.Files)
	})
	e.Register(MethodGitHubGistGet, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p IDParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return src.Get(ctx, p.ID)
	})
	e.Register(MethodGitHubGistDelete, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		if err := gate(); err != nil {
			return nil, err
		}
		var p IDParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := src.Delete(ctx, p.ID); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
}

// ── github federation client (the borrower's peer-tunneled gist surface) ───────

// GitHubState reads the serving peer's link identity (no token crosses).
func (c *Client) GitHubState(ctx context.Context) (GitHubStateResult, error) {
	return Do[GitHubStateResult](ctx, c.e, c.nodeID, MethodGitHubState, nil)
}

// GhGistCreate creates a gist on the serving peer's account.
func (c *Client) GhGistCreate(ctx context.Context, desc string, files map[string]string, public bool) (*github.Gist, error) {
	g, err := Do[github.Gist](ctx, c.e, c.nodeID, MethodGitHubGistCreate, GistCreateParams{Desc: desc, Files: files, Public: public})
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// GhGistUpdate patches a gist on the serving peer's account.
func (c *Client) GhGistUpdate(ctx context.Context, id, desc string, files map[string]string) (*github.Gist, error) {
	g, err := Do[github.Gist](ctx, c.e, c.nodeID, MethodGitHubGistUpdate, GistUpdateParams{ID: id, Desc: desc, Files: files})
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// GhGistGet fetches a gist from the serving peer's account.
func (c *Client) GhGistGet(ctx context.Context, id string) (*github.Gist, error) {
	g, err := Do[github.Gist](ctx, c.e, c.nodeID, MethodGitHubGistGet, IDParam{ID: id})
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// GhGistDelete deletes a gist on the serving peer's account.
func (c *Client) GhGistDelete(ctx context.Context, id string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodGitHubGistDelete, IDParam{ID: id})
	return err
}
