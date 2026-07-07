package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"rave.page/mate/internal/logbus"
)

// isolateDataDir keeps sealed-token writes out of the real user config dir.
func isolateDataDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)         // windows
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	t.Setenv("HOME", dir)            // darwin
}

func newTestAuth(t *testing.T, oauth, api string) *Auth {
	t.Helper()
	a := NewAuth(func() string { return "cid123" }, logbus.New(16))
	a.oauth, a.api = oauth, api
	return a
}

func TestDeviceFlow(t *testing.T) {
	isolateDataDir(t)
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_id") != "cid123" || r.FormValue("scope") != "gist" {
			t.Errorf("bad device request: %v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev40", "user_code": "ABCD-1234",
			"verification_uri": "https://github.com/login/device", "interval": 0, "expires_in": 900,
		})
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") != deviceGrant {
			t.Errorf("bad grant type %q", r.FormValue("grant_type"))
		}
		if polls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "dymattic"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := newTestAuth(t, srv.URL+"/login", srv.URL)
	da, err := a.StartDevice(context.Background())
	if err != nil {
		t.Fatalf("StartDevice: %v", err)
	}
	if da.UserCode != "ABCD-1234" || da.Interval != 5 {
		t.Fatalf("bad DeviceAuth %+v (interval floor = 5)", da)
	}
	da.Interval = 0 // fast poll in test
	if err := a.PollDevice(context.Background(), da); err != nil {
		t.Fatalf("PollDevice: %v", err)
	}
	if !a.SignedIn() || a.Login() != "dymattic" {
		t.Fatalf("not signed in after poll: login=%q", a.Login())
	}
	if tok, _ := a.Token(); tok != "tok-1" {
		t.Fatalf("token = %q", tok)
	}
}

func TestPollDeviceDenied(t *testing.T) {
	isolateDataDir(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
	}))
	defer srv.Close()
	a := newTestAuth(t, srv.URL, srv.URL)
	err := a.PollDevice(context.Background(), DeviceAuth{DeviceCode: "d", ExpiresIn: 900})
	if err == nil {
		t.Fatal("want error on access_denied")
	}
}

func TestStartDeviceNoClientID(t *testing.T) {
	a := NewAuth(func() string { return "" }, logbus.New(16))
	if _, err := a.StartDevice(context.Background()); err == nil {
		t.Fatal("want error without client id")
	}
}

func TestSetPAT(t *testing.T) {
	isolateDataDir(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer pat-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "someone"})
	}))
	defer srv.Close()
	a := newTestAuth(t, srv.URL, srv.URL)
	if err := a.SetPAT(context.Background(), "bad"); err == nil {
		t.Fatal("want error for rejected PAT")
	}
	if err := a.SetPAT(context.Background(), " pat-1 "); err != nil {
		t.Fatalf("SetPAT: %v", err)
	}
	if a.Login() != "someone" {
		t.Fatalf("login = %q", a.Login())
	}
	a.Logout()
	if a.SignedIn() {
		t.Fatal("still signed in after Logout")
	}
}

func TestGistCRUD(t *testing.T) {
	isolateDataDir(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "o"})
	})
	mux.HandleFunc("/gists", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Public bool                         `json:"public"`
			Files  map[string]map[string]string `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Public || body.Files["a.txt"]["content"] != "hello" {
			t.Errorf("bad create body: %+v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"g1","html_url":"https://gist.github.com/o/g1","owner":{"login":"o"},"files":{"a.txt":{"content":"hello"}}}`))
	})
	mux.HandleFunc("/gists/g1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			_, _ = w.Write([]byte(`{"id":"g1","owner":{"login":"o"},"files":{"a.txt":{"content":"v2"}}}`))
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":"g1","owner":{"login":"o"},"files":{"a.txt":{"content":"v2"}}}`))
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := newTestAuth(t, srv.URL, srv.URL)
	if err := a.SetPAT(context.Background(), "p"); err != nil {
		t.Fatalf("SetPAT: %v", err)
	}
	g := NewGists(a)
	created, err := g.Create(context.Background(), "d", map[string]string{"a.txt": "hello"}, false)
	if err != nil || created.ID != "g1" {
		t.Fatalf("Create: %v %+v", err, created)
	}
	up, err := g.Update(context.Background(), "g1", "", map[string]string{"a.txt": "v2"})
	if err != nil || up.Files["a.txt"].Content != "v2" {
		t.Fatalf("Update: %v %+v", err, up)
	}
	got, err := g.Get(context.Background(), "g1")
	if err != nil || got.Files["a.txt"].Content != "v2" {
		t.Fatalf("Get: %v %+v", err, got)
	}
	if err := g.Delete(context.Background(), "g1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestRawURL(t *testing.T) {
	want := "https://gist.githubusercontent.com/o/g1/raw/perms.txt"
	if got := RawURL("o", "g1", "perms.txt"); got != want {
		t.Fatalf("RawURL = %q", got)
	}
}
