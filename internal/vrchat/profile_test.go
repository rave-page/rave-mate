package vrchat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestValidStatus(t *testing.T) {
	for _, s := range []string{"join me", "active", "ask me", "busy"} {
		if !ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = false", s)
		}
	}
	for _, s := range []string{"offline", "online", "", "Active"} {
		if ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = true", s)
		}
	}
}

func TestUpdateStatus(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "usr_1", "displayName": "DJ", "status": "active", "statusDescription": "live",
		})
	}))
	u, err := c.UpdateStatus(context.Background(), "usr_1", "active", "live")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/users/usr_1" {
		t.Errorf("request = %s %s; want PUT /users/usr_1", gotMethod, gotPath)
	}
	if gotBody["status"] != "active" || gotBody["statusDescription"] != "live" {
		t.Errorf("body = %v", gotBody)
	}
	if _, ok := gotBody["bio"]; ok {
		t.Errorf("status update must not send bio: %v", gotBody)
	}
	if u.Status != "active" {
		t.Errorf("returned user status = %q", u.Status)
	}
}

func TestUpdateStatusValidation(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be hit on local validation failure")
	}))
	if _, err := c.UpdateStatus(context.Background(), "usr_1", "offline", ""); err == nil {
		t.Error("expected invalid-status error")
	}
	if _, err := c.UpdateStatus(context.Background(), "usr_1", "active", strings.Repeat("x", 33)); err == nil {
		t.Error("expected statusDescription-too-long error")
	}
}

func TestUpdateBio(t *testing.T) {
	var gotBody map[string]any
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "usr_1", "bio": "hi"})
	}))
	// links nil → bioLinks omitted.
	if _, err := c.UpdateBio(context.Background(), "usr_1", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["bioLinks"]; ok {
		t.Errorf("nil links must omit bioLinks: %v", gotBody)
	}
	if gotBody["bio"] != "hi" {
		t.Errorf("body bio = %v", gotBody["bio"])
	}
	// links provided → bioLinks present.
	if _, err := c.UpdateBio(context.Background(), "usr_1", "hi", []string{"https://rave.page"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := gotBody["bioLinks"].([]any); len(got) != 1 {
		t.Errorf("bioLinks = %v", gotBody["bioLinks"])
	}
}

func TestUpdateBioValidation(t *testing.T) {
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("server must not be hit on local validation failure")
	}))
	if _, err := c.UpdateBio(context.Background(), "usr_1", strings.Repeat("x", MaxBio+1), nil); err == nil {
		t.Error("expected bio-too-long error")
	}
	if _, err := c.UpdateBio(context.Background(), "usr_1", "ok", []string{"a", "b", "c", "d"}); err == nil {
		t.Error("expected too-many-bioLinks error")
	}
}

func TestManagerUpdateRequiresLogin(t *testing.T) {
	m := NewManager(nil, func() bool { return false })
	if _, err := m.UpdateStatus(context.Background(), "active", ""); err == nil {
		t.Error("expected ErrUnauthorized when signed out")
	}
	if _, err := m.UpdateBio(context.Background(), "hi", nil); err == nil {
		t.Error("expected ErrUnauthorized when signed out")
	}
	if id := m.CurrentUserID(); id != "" {
		t.Errorf("CurrentUserID signed out = %q", id)
	}
}
