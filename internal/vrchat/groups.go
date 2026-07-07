package vrchat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Friend is the slice of a friend object we consume.
type Friend struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

// Group is the slice of a group (search result / membership) we consume.
type Group struct {
	ID          string `json:"id"`
	GroupID     string `json:"groupId"` // set on /users/{id}/groups entries
	Name        string `json:"name"`
	ShortCode   string `json:"shortCode"`
	MemberCount int    `json:"memberCount"`
}

// EffectiveID returns the grp_ id regardless of endpoint shape.
func (g Group) EffectiveID() string {
	if g.GroupID != "" {
		return g.GroupID
	}
	return g.ID
}

// GroupRole is one role within a group.
type GroupRole struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	IsManagementRole  bool     `json:"isManagementRole"`
	Order             int      `json:"order,omitempty"`
	IsSelfAssignable  bool     `json:"isSelfAssignable,omitempty"`
	RequiresTwoFactor bool     `json:"requiresTwoFactor,omitempty"`
	Permissions       []string `json:"permissions,omitempty"`
}

// GroupMember is one member row (roleIds included).
type GroupMember struct {
	UserID  string   `json:"userId"`
	RoleIDs []string `json:"roleIds"`
	User    struct {
		DisplayName string `json:"displayName"`
	} `json:"user"`
}

// Friends pages the friends list (n ≤ 100). offline=true lists offline friends.
func (c *Client) Friends(ctx context.Context, offset, n int, offline bool) ([]Friend, error) {
	q := url.Values{"offset": {strconv.Itoa(offset)}, "n": {strconv.Itoa(clampN(n))}, "offline": {strconv.FormatBool(offline)}}
	var out []Friend
	if err := c.getJSON(ctx, "/auth/user/friends?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserGroups lists a user's (public) groups; own id → my groups.
func (c *Client) UserGroups(ctx context.Context, userID string) ([]Group, error) {
	var out []Group
	if err := c.getJSON(ctx, "/users/"+url.PathEscape(userID)+"/groups", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SearchGroups searches groups by name/shortCode.
func (c *Client) SearchGroups(ctx context.Context, query string, offset, n int) ([]Group, error) {
	q := url.Values{"query": {query}, "offset": {strconv.Itoa(offset)}, "n": {strconv.Itoa(clampN(n))}}
	var out []Group
	if err := c.getJSON(ctx, "/groups?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GroupRoles lists a group's roles (readable for any valid group id).
func (c *Client) GroupRoles(ctx context.Context, groupID string) ([]GroupRole, error) {
	var out []GroupRole
	if err := c.getJSON(ctx, "/groups/"+url.PathEscape(groupID)+"/roles", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GroupMembers pages a group's members; roleID "" = all members. Visibility-gated:
// private groups / hidden members may 403/404 or come back partial.
func (c *Client) GroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]GroupMember, error) {
	q := url.Values{"offset": {strconv.Itoa(offset)}, "n": {strconv.Itoa(clampN(n))}}
	if roleID != "" {
		q.Set("roleId", roleID)
	}
	var out []GroupMember
	if err := c.getJSON(ctx, "/groups/"+url.PathEscape(groupID)+"/members?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// getJSON GETs path and decodes a 200 JSON body (401 → ErrUnauthorized).
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.newReq(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, body, err := c.do(req)
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, apiMessage(body))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("vrchat: GET %s HTTP %d: %s", req.URL.Path, resp.StatusCode, apiMessage(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("vrchat: decode %s: %w", req.URL.Path, err)
	}
	return nil
}

// clampN bounds page size to the API's 1..100.
func clampN(n int) int {
	if n < 1 {
		return 60
	}
	if n > 100 {
		return 100
	}
	return n
}
