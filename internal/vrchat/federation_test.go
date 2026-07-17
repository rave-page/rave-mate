package vrchat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"rave.page/mate/internal/logbus"
)

// Federation state rules: armed federation answers State()/Client()/CurrentUserID
// while there is no local session, never masks a local login OR an in-progress
// local 2FA, and ClearFederated reverts cleanly.
func TestManagerFederation(t *testing.T) {
	log := logbus.New(64)
	m := NewManager(log, func() bool { return false })
	fed := New(log)

	m.SetFederated(fed, State{UserID: "usr_p", DisplayName: "DeskPC", Via: "desk"})
	st := m.State()
	if !st.LoggedIn || st.Via != "desk" || st.DisplayName != "DeskPC" {
		t.Fatalf("federated state drift: %+v", st)
	}
	if m.Client() != fed {
		t.Fatal("Client() must return the federated client without a local session")
	}
	if m.CurrentUserID() != "usr_p" {
		t.Fatalf("CurrentUserID drift: %q", m.CurrentUserID())
	}
	if m.LocalState().LoggedIn {
		t.Fatal("LocalState must stay signed out")
	}

	// in-progress local auth wins over the federation (the 2FA form must show)
	m.setState(State{Awaiting2FA: true, Methods: []string{"totp"}})
	if st := m.State(); st.LoggedIn || !st.Awaiting2FA {
		t.Fatalf("local 2FA masked by federation: %+v", st)
	}

	// local login wins outright
	m.setState(State{LoggedIn: true, UserID: "usr_l", DisplayName: "Local"})
	if st := m.State(); st.Via != "" || st.UserID != "usr_l" {
		t.Fatalf("local session must win: %+v", st)
	}
	if m.Client() == fed {
		t.Fatal("Client() must return the local client when logged in")
	}

	m.setState(State{})
	m.ClearFederated()
	if st := m.State(); st.LoggedIn {
		t.Fatalf("cleared federation still answering: %+v", st)
	}
}

// Raw serves the federation proxy: method/path forwarded against the session,
// status + body returned untouched.
func TestClientRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users/usr_x" && r.Method == http.MethodGet {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":"usr_x"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	c := New(logbus.New(16))
	c.base = srv.URL

	status, body, err := c.Raw(context.Background(), http.MethodGet, "/users/usr_x", nil, "")
	if err != nil || status != 200 || string(body) != `{"id":"usr_x"}` {
		t.Fatalf("raw get: status=%d body=%s err=%v", status, body, err)
	}
	status, _, err = c.Raw(context.Background(), http.MethodGet, "/missing", nil, "")
	if err != nil || status != 404 {
		t.Fatalf("raw 404 passthrough: status=%d err=%v", status, err)
	}
}
