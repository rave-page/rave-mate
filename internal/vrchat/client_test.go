package vrchat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rave.page/mate/internal/logbus"
)

func testClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(logbus.New(64))
	c.base = srv.URL
	return c
}

func TestURIComponent(t *testing.T) {
	// Exact JS encodeURIComponent parity. The !~*'() set MUST stay literal (the bug
	// that 401'd valid passwords: url.QueryEscape escapes !*'() and space→+).
	for in, want := range map[string]string{
		"user name":     "user%20name",
		"p@ss:w/d+":     "p%40ss%3Aw%2Fd%2B",
		"plain":         "plain",
		"P@ss!w0rd()*'": "P%40ss!w0rd()*'", // !()*' stay literal
		"a~b.c-d_e":     "a~b.c-d_e",       // unreserved stay literal
		"100%done":      "100%25done",
		"smörgås":       "sm%C3%B6rg%C3%A5s", // UTF-8 multibyte
	} {
		if got := uriComponent(in); got != want {
			t.Errorf("uriComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoginSuccess(t *testing.T) {
	var gotAuth, gotUA string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		http.SetCookie(w, &http.Cookie{Name: "auth", Value: "authcookie_x"})
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "usr_1", "displayName": "DJ"})
	}))
	res, err := c.Login(context.Background(), "us er", "p:w")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Authenticated || res.User == nil || res.User.ID != "usr_1" {
		t.Fatalf("unexpected result %+v", res)
	}
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("us%20er:p%3Aw"))
	if gotAuth != wantBasic {
		t.Errorf("basic auth = %q, want %q", gotAuth, wantBasic)
	}
	if !strings.HasPrefix(gotUA, "rave-mate/") {
		t.Errorf("user-agent = %q", gotUA)
	}
	if a, _ := c.Cookies(); a != "authcookie_x" {
		t.Errorf("auth cookie not captured: %q", a)
	}
}

func TestLogin2FAThenVerify(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/user", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("twoFactorAuth"); err == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "usr_1", "displayName": "DJ"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "auth", Value: "authcookie_x"})
		_ = json.NewEncoder(w).Encode(map[string]any{"requiresTwoFactorAuth": []string{"totp", "otp"}})
	})
	mux.HandleFunc("/auth/twofactorauth/totp/verify", func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie("auth"); err != nil || ck.Value != "authcookie_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var in struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Code != "123456" {
			_ = json.NewEncoder(w).Encode(map[string]any{"verified": false})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "twoFactorAuth", Value: "tfa_y"})
		_ = json.NewEncoder(w).Encode(map[string]any{"verified": true})
	})
	c := testClient(t, mux)

	res, err := c.Login(context.Background(), "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Requires2FA || len(res.Methods) != 2 {
		t.Fatalf("expected 2FA result, got %+v", res)
	}
	if err := c.Verify2FA(context.Background(), "", "123456"); err != nil {
		t.Fatal(err)
	}
	if _, tfa := c.Cookies(); tfa != "tfa_y" {
		t.Fatalf("twoFactorAuth cookie not captured: %q", tfa)
	}
	u, err := c.CurrentUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.DisplayName != "DJ" {
		t.Errorf("user = %+v", u)
	}
}

func TestVerify2FABadCode(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"verified": false})
	}))
	if err := c.Verify2FA(context.Background(), "totp", "000000"); err == nil {
		t.Fatal("expected error for rejected code")
	}
}

func TestCurrentUserUnauthorized(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "expired", "status_code": 401}})
	}))
	_, err := c.CurrentUser(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestApiMessageTrimsVrchatDoubleQuotes(t *testing.T) {
	// VRChat double-encodes: message value is itself a quoted JSON string.
	body := []byte(`{"error":{"message":"\"Invalid Username/Email or Password\"","status_code":401}}`)
	if got := apiMessage(body); got != "Invalid Username/Email or Password" {
		t.Errorf("apiMessage = %q, want unquoted message", got)
	}
}

func TestErrorBodyLogged(t *testing.T) {
	bus := logbus.New(64)
	c := New(bus)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"\"Invalid Username/Email or Password\""}}`))
	}))
	defer srv.Close()
	c.base = srv.URL
	_, _ = c.CurrentUser(context.Background())
	found := false
	for _, e := range bus.Snapshot() {
		if v, ok := e.Fields["vrcError"]; ok && v == "Invalid Username/Email or Password" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected vrcError field with VRChat message in the log")
	}
}

func TestResumeSessionCookiesSent(t *testing.T) {
	var sawAuth, sawTFA string
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ck, err := r.Cookie("auth"); err == nil {
			sawAuth = ck.Value
		}
		if ck, err := r.Cookie("twoFactorAuth"); err == nil {
			sawTFA = ck.Value
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "usr_1", "displayName": "DJ"})
	}))
	c.SetCookies("a1", "t1")
	if _, err := c.CurrentUser(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sawAuth != "a1" || sawTFA != "t1" {
		t.Errorf("cookies sent = %q/%q", sawAuth, sawTFA)
	}
}
