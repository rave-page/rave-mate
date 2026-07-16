package vrchat

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// UserDisplayName resolves a user id to its CURRENT display name (GET /users/{id}) - ids are
// stable, names drift, so the editor re-resolves ids to names at author time. The caller treats a
// per-id error as unresolvable (keep the id, retry later); it must not fail a whole resolve batch.
func (c *Client) UserDisplayName(ctx context.Context, userID string) (string, error) {
	if strings.TrimSpace(userID) == "" {
		return "", fmt.Errorf("vrchat: UserDisplayName: empty user id")
	}
	var out struct {
		DisplayName string `json:"displayName"`
	}
	if err := c.getJSON(ctx, "/users/"+url.PathEscape(userID), &out); err != nil {
		return "", err
	}
	return out.DisplayName, nil
}

// GroupName resolves a group id to its current name (GET /groups/{id}). Same per-id best-effort
// contract as UserDisplayName.
func (c *Client) GroupName(ctx context.Context, groupID string) (string, error) {
	if strings.TrimSpace(groupID) == "" {
		return "", fmt.Errorf("vrchat: GroupName: empty group id")
	}
	var out struct {
		Name string `json:"name"`
	}
	if err := c.getJSON(ctx, "/groups/"+url.PathEscape(groupID), &out); err != nil {
		return "", err
	}
	return out.Name, nil
}
