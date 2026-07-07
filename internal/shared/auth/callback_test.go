package auth

import "testing"

func TestParseCallback(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantCode string
		wantAPI  string
		wantErr  bool
	}{
		{"ravepage grant", "ravepage://auth/callback?grant=abc123&api=https://development.api.rave.page", "abc123", "https://development.api.rave.page", false},
		{"rave grant", "rave://auth/callback?grant=r7&api=https://development.api.rave.page", "r7", "https://development.api.rave.page", false},
		{"rave code", "rave://auth/callback?code=r8", "r8", "", false},
		{"dymattic legacy code", "dymattic://auth/callback?code=xyz", "xyz", "", false},
		{"short callback path", "ravepage://callback?grant=g1", "g1", "", false},
		{"single slash normalized", "rave:/auth/callback?code=zz", "zz", "", false},
		{"oauth error", "ravepage://auth/callback?error=access_denied", "", "", false},
		{"wrong scheme", "https://rave.page/auth/callback?code=q", "", "", true},
		{"wrong route", "ravepage://auth/somewhere?code=q", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cb, err := parseCallback(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", cb)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cb.Code != c.wantCode {
				t.Errorf("code = %q, want %q", cb.Code, c.wantCode)
			}
			if cb.API != c.wantAPI {
				t.Errorf("api = %q, want %q", cb.API, c.wantAPI)
			}
		})
	}
}

func TestIsDeepLink(t *testing.T) {
	yes := []string{"rave://auth/callback?grant=x", "ravepage://auth/callback?grant=x", "DYMATTIC://auth/callback", "ravepage:/auth/callback"}
	no := []string{"https://rave.page", "", "C:\\path\\to\\file", "--service"}
	for _, s := range yes {
		if !IsDeepLink(s) {
			t.Errorf("IsDeepLink(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if IsDeepLink(s) {
			t.Errorf("IsDeepLink(%q) = true, want false", s)
		}
	}
}

func TestWebsiteBase(t *testing.T) {
	cases := map[string]string{
		"https://development.api.rave.page": "https://development.rave.page",
		"https://api.rave.page":             "https://rave.page",
		"https://testing.api.rave.page":     "https://testing.rave.page",
	}
	for in, want := range cases {
		if got := websiteBase(in); got != want {
			t.Errorf("websiteBase(%q) = %q, want %q", in, got, want)
		}
	}
}
