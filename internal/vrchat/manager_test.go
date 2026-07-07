package vrchat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"rave.page/mate/internal/logbus"
)

// memStore is an in-memory storer for tests (no DPAPI / disk).
type memStore struct {
	ps      persistedSession
	cleared bool
}

func (s *memStore) load() persistedSession   { return s.ps }
func (s *memStore) save(ps persistedSession) { s.ps = ps; s.cleared = false }
func (s *memStore) clear()                   { s.ps = persistedSession{}; s.cleared = true }

func testManager(t *testing.T, h http.Handler, remember bool) (*Manager, *memStore) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	st := &memStore{}
	m := NewManager(logbus.New(64), func() bool { return remember })
	m.store = st
	m.cli.base = srv.URL
	return m, st
}

// vrcMux: 2FA-gated /auth/user + totp verify, the full happy path.
func vrcMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/user", func(w http.ResponseWriter, r *http.Request) {
		auth, _ := r.Cookie("auth")
		if auth == nil {
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "no session"}})
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "auth", Value: "authcookie_x"})
			_ = json.NewEncoder(w).Encode(map[string]any{"requiresTwoFactorAuth": []string{"totp"}})
			return
		}
		if auth.Value != "authcookie_x" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "expired"}})
			return
		}
		if _, err := r.Cookie("twoFactorAuth"); err != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"requiresTwoFactorAuth": []string{"totp"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "usr_1", "displayName": "DJ"})
	})
	mux.HandleFunc("/auth/twofactorauth/totp/verify", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "twoFactorAuth", Value: "tfa_y"})
		_ = json.NewEncoder(w).Encode(map[string]any{"verified": true})
	})
	mux.HandleFunc("/logout", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": map[string]any{"message": "bye"}})
	})
	return mux
}

func TestManagerFullFlow(t *testing.T) {
	m, st := testManager(t, vrcMux(), true)
	ctx := context.Background()

	var transitions []State
	m.OnChange(func(s State) { transitions = append(transitions, s) })

	got, err := m.Login(ctx, "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Awaiting2FA {
		t.Fatalf("want awaiting-2FA, got %+v", got)
	}
	got, err = m.Verify2FA(ctx, "totp", "123456")
	if err != nil {
		t.Fatal(err)
	}
	if !got.LoggedIn || got.DisplayName != "DJ" {
		t.Fatalf("want logged-in DJ, got %+v", got)
	}
	if st.ps.Auth != "authcookie_x" || st.ps.TwoFactor != "tfa_y" {
		t.Fatalf("session not persisted: %+v", st.ps)
	}
	if len(transitions) != 2 {
		t.Errorf("transitions = %d, want 2", len(transitions))
	}

	m.Unlink(ctx)
	if !st.cleared || m.State().LoggedIn {
		t.Fatalf("unlink did not clear: store=%+v state=%+v", st.ps, m.State())
	}
}

func TestManagerNoRememberSkipsPersist(t *testing.T) {
	m, st := testManager(t, vrcMux(), false)
	ctx := context.Background()
	if _, err := m.Login(ctx, "u", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify2FA(ctx, "totp", "123456"); err != nil {
		t.Fatal(err)
	}
	if st.ps.Auth != "" {
		t.Fatalf("session persisted despite remember=false: %+v", st.ps)
	}
}

func TestManagerResume(t *testing.T) {
	m, st := testManager(t, vrcMux(), true)
	st.ps = persistedSession{Auth: "authcookie_x", TwoFactor: "tfa_y"}
	if !m.Resume(context.Background()) {
		t.Fatal("resume failed")
	}
	s := m.State()
	if !s.LoggedIn || s.UserID != "usr_1" {
		t.Fatalf("state = %+v", s)
	}
}

func TestManagerResumeExpiredClears(t *testing.T) {
	m, st := testManager(t, vrcMux(), true)
	st.ps = persistedSession{Auth: "stale", TwoFactor: "tfa_y"}
	if m.Resume(context.Background()) {
		t.Fatal("resume should fail on expired session")
	}
	if !st.cleared {
		t.Fatal("expired session not cleared from store")
	}
	if a, _ := m.cli.Cookies(); a != "" {
		t.Fatalf("client still holds cookie %q", a)
	}
}
