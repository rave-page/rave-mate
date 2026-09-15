package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"rave.page/mate/internal/github"
	"rave.page/mate/internal/remotectl"
)

// fakeGHSrv is a linked GitHubServer for the peerGistStore round-trip test.
type fakeGHSrv struct {
	login string
	gists map[string]*github.Gist
	seq   int
}

func (f *fakeGHSrv) LocalSignedIn() bool { return true }
func (f *fakeGHSrv) Login() string       { return f.login }
func (f *fakeGHSrv) Create(_ context.Context, _ string, files map[string]string, _ bool) (*github.Gist, error) {
	f.seq++
	id := fmt.Sprintf("g%d", f.seq)
	g := &github.Gist{ID: id, Files: map[string]github.GistFile{}}
	g.Owner.Login = f.login
	for name, content := range files {
		g.Files[name] = github.GistFile{Content: content}
	}
	f.gists[id] = g
	return g, nil
}
func (f *fakeGHSrv) Update(_ context.Context, id, _ string, files map[string]string) (*github.Gist, error) {
	g, ok := f.gists[id]
	if !ok {
		return nil, errors.New("not found")
	}
	for name, content := range files {
		g.Files[name] = github.GistFile{Content: content}
	}
	return g, nil
}
func (f *fakeGHSrv) Get(_ context.Context, id string) (*github.Gist, error) {
	g, ok := f.gists[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return g, nil
}
func (f *fakeGHSrv) Delete(_ context.Context, id string) error { delete(f.gists, id); return nil }

// TestPeerGistStore drives the federated vrcperm.GistStore over an in-process remotectl loopback:
// with no serving node it errors; once armed it creates/updates gists on the serving peer; on
// disarm it errors again.
func TestPeerGistStore(t *testing.T) {
	var srv, cli *remotectl.Endpoint
	srv = remotectl.New(nil, func(_ string, p []byte) error { cli.OnControl("srv", p); return nil })
	cli = remotectl.New(nil, func(_ string, p []byte) error { srv.OnControl("cli", p); return nil })
	f := &fakeGHSrv{login: "peerdj", gists: map[string]*github.Gist{}}
	remotectl.RegisterGitHub(srv, f)

	store := &peerGistStore{endpoint: func() *remotectl.Endpoint { return cli }}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// no serving node armed -> error (never a silent no-op).
	if _, err := store.Create(ctx, "d", map[string]string{"f": "c"}, false); err == nil {
		t.Fatal("Create with no serving peer must error")
	}

	store.setNode("srv")
	g, err := store.Create(ctx, "roster", map[string]string{"perms.json": "[]"}, false)
	if err != nil || g == nil || g.ID == "" || g.Owner.Login != "peerdj" {
		t.Fatalf("Create over peer: g=%+v err=%v", g, err)
	}
	up, err := store.Update(ctx, g.ID, "", map[string]string{"perms.json": "[1]"})
	if err != nil || up.Files["perms.json"].Content != "[1]" {
		t.Fatalf("Update over peer: up=%+v err=%v", up, err)
	}

	// disarm -> the serving node clears, writes error again.
	store.setNode("")
	if _, err := store.Update(ctx, g.ID, "", map[string]string{"perms.json": "[2]"}); err == nil {
		t.Fatal("Update after disarm must error")
	}
}
