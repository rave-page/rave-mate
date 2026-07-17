package remotectl

import (
	"strings"
	"testing"

	"rave.page/mate/internal/vrchat"
)

// fakeVrcSource is an unlinked-by-default VrcSource; Linked flips the served state.
type fakeVrcSource struct {
	linked bool
}

func (f *fakeVrcSource) State() vrchat.State {
	return vrchat.State{LoggedIn: f.linked, UserID: "usr_x", DisplayName: "DyMattic"}
}
func (f *fakeVrcSource) Client() *vrchat.Client {
	if !f.linked {
		return nil
	}
	return &vrchat.Client{} // never dereferenced in these tests (status only)
}
func (f *fakeVrcSource) CurrentUserID() string { return "usr_x" }

// TestVrcStatusRPC: status answers on every peer - linked=false without a session, and the
// data methods refuse with a clear error instead of touching a nil client.
func TestVrcStatusRPC(t *testing.T) {
	server, client := loopback()
	src := &fakeVrcSource{}
	RegisterVrchat(server, src)
	rc := NewClient(client, "server")

	st, err := rc.VrcStatus(ctx(t))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Linked {
		t.Fatalf("unlinked source must report linked=false: %+v", st)
	}

	if _, err := rc.VrcGroupRoles(ctx(t), "grp_1"); err == nil ||
		!strings.Contains(err.Error(), "not linked") {
		t.Fatalf("data method on unlinked peer must refuse, got err=%v", err)
	}

	src.linked = true
	st, err = rc.VrcStatus(ctx(t))
	if err != nil {
		t.Fatalf("status linked: %v", err)
	}
	if !st.Linked || st.DisplayName != "DyMattic" || st.UserID != "usr_x" {
		t.Fatalf("linked status drift: %+v", st)
	}
}

// vrchat.proxy validation: bad methods, absolute URLs and auth endpoints are
// refused server-side BEFORE any client call (GET /auth/user is the one
// allowed auth read - not asserted here, it would hit the network).
func TestVrcProxyValidation(t *testing.T) {
	server, client := loopback()
	RegisterVrchat(server, &fakeVrcSource{linked: true})
	rc := NewClient(client, "server")

	cases := []struct{ method, pq, wantErr string }{
		{"PATCH", "/users/x", "not allowed"},
		{"GET", "https://evil.example/api", "API-relative"},
		{"GET", "users/x", "API-relative"},
		{"PUT", "/logout", "local-only"},
		{"POST", "/auth/twofactorauth/totp/verify", "local-only"},
		{"GET", "/auth", "local-only"},
	}
	for _, c := range cases {
		_, _, err := rc.VrcProxy(ctx(t), c.method, c.pq, nil, "")
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Fatalf("%s %s: want error containing %q, got %v", c.method, c.pq, c.wantErr, err)
		}
	}
}
