package remotectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"rave.page/mate/internal/github"
)

// fakeGH is a minimal GitHubServer for the github.* RPC tests.
type fakeGH struct {
	linked bool
	login  string
	gists  map[string]*github.Gist
	seq    int
}

func (f *fakeGH) LocalSignedIn() bool { return f.linked }
func (f *fakeGH) Login() string       { return f.login }
func (f *fakeGH) Create(_ context.Context, _ string, files map[string]string, _ bool) (*github.Gist, error) {
	f.seq++
	id := fmt.Sprintf("g%d", f.seq)
	g := &github.Gist{ID: id, HTMLURL: "https://gist.github.com/" + id, Files: map[string]github.GistFile{}}
	g.Owner.Login = f.login
	for name, content := range files {
		g.Files[name] = github.GistFile{Content: content}
	}
	f.gists[id] = g
	return g, nil
}
func (f *fakeGH) Update(_ context.Context, id, _ string, files map[string]string) (*github.Gist, error) {
	g, ok := f.gists[id]
	if !ok {
		return nil, errors.New("not found")
	}
	for name, content := range files {
		g.Files[name] = github.GistFile{Content: content}
	}
	return g, nil
}
func (f *fakeGH) Get(_ context.Context, id string) (*github.Gist, error) {
	g, ok := f.gists[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return g, nil
}
func (f *fakeGH) Delete(_ context.Context, id string) error { delete(f.gists, id); return nil }

// TestGitHubRPC drives state + gist create/update/get/delete through the full path.
func TestGitHubRPC(t *testing.T) {
	server, client := loopback()
	f := &fakeGH{linked: true, login: "raverdj", gists: map[string]*github.Gist{}}
	RegisterGitHub(server, f)
	rc := NewClient(client, "server")

	st, err := rc.GitHubState(ctx(t))
	if err != nil || !st.SignedIn || st.Login != "raverdj" {
		t.Fatalf("state=%+v err=%v", st, err)
	}

	g, err := rc.GhGistCreate(ctx(t), "roster", map[string]string{"perms.json": "[]"}, false)
	if err != nil || g == nil || g.ID == "" || g.Owner.Login != "raverdj" || g.Files["perms.json"].Content != "[]" {
		t.Fatalf("create=%+v err=%v", g, err)
	}
	up, err := rc.GhGistUpdate(ctx(t), g.ID, "", map[string]string{"perms.json": "[1]"})
	if err != nil || up.Files["perms.json"].Content != "[1]" {
		t.Fatalf("update=%+v err=%v", up, err)
	}
	got, err := rc.GhGistGet(ctx(t), g.ID)
	if err != nil || got.ID != g.ID {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	if err := rc.GhGistDelete(ctx(t), g.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := rc.GhGistGet(ctx(t), g.ID); err == nil {
		t.Fatal("get after delete must error")
	}
}

// TestGitHubStateNoToken: the state wire result carries identity only - never a token/secret.
func TestGitHubStateNoToken(t *testing.T) {
	server, client := loopback()
	RegisterGitHub(server, &fakeGH{linked: true, login: "raverdj", gists: map[string]*github.Gist{}})
	raw, err := client.Call(ctx(t), "server", MethodGitHubState, nil)
	if err != nil {
		t.Fatalf("state call: %v", err)
	}
	low := strings.ToLower(string(raw))
	for _, banned := range []string{"token", "access", "secret", "bearer", "pat"} {
		if strings.Contains(low, banned) {
			t.Fatalf("github.state result must not carry credentials, found %q in %s", banned, raw)
		}
	}
	// and it decodes to exactly the identity shape.
	var res GitHubStateResult
	if json.Unmarshal(raw, &res) != nil || !res.SignedIn || res.Login != "raverdj" {
		t.Fatalf("state shape drift: %s", raw)
	}
}

// TestGitHubGateWhenUnlinked: an unlinked box answers state (signedIn=false) but refuses writes.
func TestGitHubGateWhenUnlinked(t *testing.T) {
	server, client := loopback()
	f := &fakeGH{linked: false, gists: map[string]*github.Gist{}}
	RegisterGitHub(server, f)
	rc := NewClient(client, "server")

	if st, err := rc.GitHubState(ctx(t)); err != nil || st.SignedIn {
		t.Fatalf("unlinked state=%+v err=%v", st, err)
	}
	if _, err := rc.GhGistCreate(ctx(t), "x", map[string]string{"a": "b"}, false); err == nil {
		t.Fatal("create must be refused when the serving box is not linked")
	}
	if len(f.gists) != 0 {
		t.Fatalf("refused create must not touch the serving box: %v", f.gists)
	}
}

// TestGitHubNoAuthSurface: NO verb that can re-auth, unlink, or return the token is registered.
func TestGitHubNoAuthSurface(t *testing.T) {
	server, client := loopback()
	RegisterGitHub(server, &fakeGH{linked: true, login: "x", gists: map[string]*github.Gist{}})
	for _, m := range []string{
		"github.token", "github.auth", "github.auth.start", "github.startDevice", "github.pollDevice",
		"github.logout", "github.unlink", "github.setPat", "github.pat",
	} {
		if _, err := client.Call(ctx(t), "server", m, nil); err == nil || !strings.Contains(err.Error(), "unknown method") {
			t.Fatalf("auth-shaped method %q must be unregistered (got err=%v)", m, err)
		}
	}
}
