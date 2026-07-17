package vrchat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Profile field limits + status enum (wiki.vrchat.com / VRChat API UpdateUserRequest).
const (
	MaxStatusDescription = 32  // statusDescription character cap
	MaxBio               = 512 // bio character cap
	MaxBioLinks          = 3   // bioLinks array cap
)

// Statuses are the settable presence values (offline is not user-settable).
var Statuses = []string{"join me", "active", "ask me", "busy"}

// ValidStatus reports whether s is a settable presence value.
func ValidStatus(s string) bool {
	for _, v := range Statuses {
		if v == s {
			return true
		}
	}
	return false
}

// updateUser PUTs a partial user patch (only the given fields) to /users/{id} and decodes the
// updated user. The session cookies authenticate it (see newReq). Mirrors the web client's
// VrchatService.updateUser; VRChat accepts a sparse body.
func (c *Client) updateUser(ctx context.Context, userID string, fields map[string]any) (*User, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("vrchat: updateUser: empty user id")
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	req, err := c.newReq(ctx, http.MethodPut, "/users/"+userID, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, respBody, err := c.do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, apiMessage(respBody))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("vrchat: updateUser HTTP %d: %s", resp.StatusCode, apiMessage(respBody))
	}
	var u User
	if err := json.Unmarshal(respBody, &u); err != nil {
		return nil, fmt.Errorf("vrchat: decode updateUser: %w", err)
	}
	return &u, nil
}

// UpdateStatus sets presence (status) + status text (statusDescription, ≤32 chars). status must
// be one of Statuses.
func (c *Client) UpdateStatus(ctx context.Context, userID, status, description string) (*User, error) {
	if !ValidStatus(status) {
		return nil, fmt.Errorf("vrchat: invalid status %q (want one of %s)", status, strings.Join(Statuses, ", "))
	}
	if len(description) > MaxStatusDescription {
		return nil, fmt.Errorf("vrchat: statusDescription exceeds %d chars", MaxStatusDescription)
	}
	return c.updateUser(ctx, userID, map[string]any{"status": status, "statusDescription": description})
}

// UpdateBio sets the bio (≤512 chars) and optional bioLinks (≤3 URLs). Pass a non-nil links
// slice (possibly empty) to also rewrite the links; nil leaves links untouched.
func (c *Client) UpdateBio(ctx context.Context, userID, bio string, links []string) (*User, error) {
	if len(bio) > MaxBio {
		return nil, fmt.Errorf("vrchat: bio exceeds %d chars", MaxBio)
	}
	fields := map[string]any{"bio": bio}
	if links != nil {
		if len(links) > MaxBioLinks {
			return nil, fmt.Errorf("vrchat: at most %d bioLinks", MaxBioLinks)
		}
		fields["bioLinks"] = links
	}
	return c.updateUser(ctx, userID, fields)
}

// ── Manager wrappers (use the logged-in current user) ─────────────────────────

// CurrentUserID returns the logged-in VRChat user id ("" when signed out).
// Federation-aware: with no local session, the serving peer's user id answers.
func (m *Manager) CurrentUserID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.state.LoggedIn && m.fedCli != nil {
		return m.fedState.UserID
	}
	return m.state.UserID
}

// UpdateStatus sets the current user's presence + status text. Errors if not logged in.
func (m *Manager) UpdateStatus(ctx context.Context, status, description string) (*User, error) {
	id := m.CurrentUserID()
	if id == "" {
		return nil, ErrUnauthorized
	}
	return m.Client().UpdateStatus(ctx, id, status, description)
}

// UpdateBio sets the current user's bio (+ optional bioLinks). Errors if not logged in.
func (m *Manager) UpdateBio(ctx context.Context, bio string, links []string) (*User, error) {
	id := m.CurrentUserID()
	if id == "" {
		return nil, ErrUnauthorized
	}
	return m.Client().UpdateBio(ctx, id, bio, links)
}

// FetchUser re-fetches the current user (to seed the editor with live status/bio). Errors if
// not logged in.
func (m *Manager) FetchUser(ctx context.Context) (*User, error) {
	if m.CurrentUserID() == "" {
		return nil, ErrUnauthorized
	}
	return m.Client().CurrentUser(ctx)
}
