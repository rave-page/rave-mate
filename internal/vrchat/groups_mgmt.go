package vrchat

// Full user-session group-management surface - a near-verbatim port of the Go API's
// workers/vrchat service cluster (vrchat_groups.go / vrchat_group_mutations.go /
// vrchat_mutations.go), reissued against THIS box's sealed session instead of a bot session.
// VRChat REST paths/verbs/bodies match the API byte-for-byte; opaque VRChat objects pass
// through as json.RawMessage (their own JSON field names preserved). The few shapes the API
// normalizes (groups list id-swap, member flattening, my-permissions flatten, audit-log
// envelope, paginated list-or-single) are replicated so the web contract is unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ── result shapes ────────────────────────────────────────────────────────────

// GroupMemberFull is the flattened member row the API emits (routers/vrchat.py:512-527):
// upstream's nested `user` sub-object hoisted onto the row. VRChat JSON field names kept.
type GroupMemberFull struct {
	ID                    string   `json:"id"`
	UserID                *string  `json:"userId,omitempty"`
	DisplayName           *string  `json:"displayName,omitempty"`
	Username              *string  `json:"username,omitempty"`
	RoleIDs               []string `json:"roleIds"`
	MembershipStatus      *string  `json:"membershipStatus,omitempty"`
	JoinedAt              *string  `json:"joinedAt,omitempty"`
	IsRepresenting        *bool    `json:"isRepresenting,omitempty"`
	ProfilePicOverride    *string  `json:"profilePicOverride,omitempty"`
	CurrentAvatarImageURL *string  `json:"currentAvatarImageUrl,omitempty"`
	Tags                  []string `json:"tags"`
}

// ProfileLimits is the static field-length limits payload (snake_case, API-verbatim).
type ProfileLimits struct {
	StatusMaxLength            int `json:"status_max_length"`
	BioMaxLength               int `json:"bio_max_length"`
	StatusDescriptionMaxLength int `json:"status_description_max_length"`
}

// MaxStatus is the status enum length cap the API reports (status_max_length).
const MaxStatus = 64

// DefaultProfileLimits returns the known VRChat profile field limits. No VRChat call.
func DefaultProfileLimits() ProfileLimits {
	return ProfileLimits{
		StatusMaxLength:            MaxStatus,
		BioMaxLength:               MaxBio,
		StatusDescriptionMaxLength: MaxStatusDescription,
	}
}

// AnnouncementIn is the create-group-announcement body.
type AnnouncementIn struct {
	Title            string
	Text             string
	SendNotification bool
	ImageID          string // "" ⇒ omitted
}

// ── group reads ──────────────────────────────────────────────────────────────

// ListGroups fetches a user's full groups (GET /users/{id}/groups) and applies the API's
// id/groupId/membershipId normalization. Returns the full group objects (not the trimmed
// Group shape) so the web listVrchatGroups contract is preserved.
func (c *Client) ListGroups(ctx context.Context, userID string) ([]map[string]any, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("vrchat: ListGroups: empty user id")
	}
	body, err := c.raw(ctx, http.MethodGet, "/users/"+url.PathEscape(userID)+"/groups", nil)
	if err != nil {
		return nil, err
	}
	var arr []map[string]any // wire-decode boundary: opaque VRChat group objects
	if err := json.Unmarshal(body, &arr); err != nil {
		return nil, fmt.Errorf("vrchat: decode groups: %w", err)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, g := range arr {
		if g == nil {
			continue
		}
		rawID, _ := g["id"].(string)
		groupID, _ := g["groupId"].(string)
		if strings.HasPrefix(rawID, "gmem_") {
			g["membershipId"] = rawID
			if strings.HasPrefix(groupID, "grp_") {
				g["id"] = groupID
			}
		} else if strings.HasPrefix(groupID, "grp_") && rawID != groupID {
			g["membershipId"] = rawID
			g["id"] = groupID
		}
		out = append(out, g)
	}
	return out, nil
}

// GetGroup fetches a group's metadata (GET /groups/{id}).
func (c *Client) GetGroup(ctx context.Context, groupID string) (json.RawMessage, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GetGroup: empty group id")
	}
	return c.raw(ctx, http.MethodGet, "/groups/"+url.PathEscape(groupID), nil)
}

// GroupInstances lists a group's active instances (GET /groups/{id}/instances).
func (c *Client) GroupInstances(ctx context.Context, groupID string) (json.RawMessage, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GroupInstances: empty group id")
	}
	return c.raw(ctx, http.MethodGet, "/groups/"+url.PathEscape(groupID)+"/instances", nil)
}

// GroupPermissions lists the group's permission catalog (GET /groups/{id}/permissions).
func (c *Client) GroupPermissions(ctx context.Context, groupID string) (json.RawMessage, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GroupPermissions: empty group id")
	}
	return c.raw(ctx, http.MethodGet, "/groups/"+url.PathEscape(groupID)+"/permissions", nil)
}

// GroupMyPermissions returns the caller's membership + roles for a group, flattened to
// {group_id, membership, roles} from GET /groups/{id}?includeRoles=true (API parity).
func (c *Client) GroupMyPermissions(ctx context.Context, groupID string) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GroupMyPermissions: empty group id")
	}
	body, err := c.raw(ctx, http.MethodGet, "/groups/"+url.PathEscape(groupID)+"?includeRoles=true", nil)
	if err != nil {
		return nil, err
	}
	var group map[string]any // wire-decode boundary
	if err := json.Unmarshal(body, &group); err != nil {
		return nil, fmt.Errorf("vrchat: decode group: %w", err)
	}
	myMember, _ := group["myMember"].(map[string]any)
	if myMember == nil {
		myMember = map[string]any{}
	}
	rolesRaw, _ := group["roles"].([]any)
	roles := make([]map[string]any, 0, len(rolesRaw))
	for _, r := range rolesRaw {
		if m, ok := r.(map[string]any); ok {
			roles = append(roles, m)
		}
	}
	return map[string]any{"group_id": groupID, "membership": myMember, "roles": roles}, nil
}

// GroupMembersFull pages a group's members (GET /groups/{id}/members) and flattens each
// row's nested `user` object per API parity. roleID unused - full member list.
func (c *Client) GroupMembersFull(ctx context.Context, groupID string, offset, n int) ([]GroupMemberFull, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GroupMembersFull: empty group id")
	}
	q := url.Values{"n": {strconv.Itoa(clampN(n))}, "offset": {strconv.Itoa(offset)}}
	body, err := c.raw(ctx, http.MethodGet, "/groups/"+url.PathEscape(groupID)+"/members?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var raw []map[string]any // wire-decode boundary
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("vrchat: decode members: %w", err)
	}
	out := make([]GroupMemberFull, 0, len(raw))
	for _, m := range raw {
		if m == nil {
			continue
		}
		user, _ := m["user"].(map[string]any)
		id, _ := m["id"].(string)
		mem := GroupMemberFull{
			ID:                 id,
			UserID:             strPtr(m, "userId"),
			DisplayName:        coalesceStr(user, m, "displayName"),
			Username:           coalesceStr(user, m, "username"),
			RoleIDs:            stringArr(m, "roleIds"),
			MembershipStatus:   strPtr(m, "membershipStatus"),
			JoinedAt:           strPtr(m, "joinedAt"),
			IsRepresenting:     boolPtr(m, "isRepresenting"),
			ProfilePicOverride: coalesceStr(user, m, "profilePicOverride"),
			Tags:               coalesceTags(user, m),
		}
		// Prefer user.currentAvatarThumbnailImageUrl over member.currentAvatarImageUrl.
		if v, ok := user["currentAvatarThumbnailImageUrl"].(string); ok && v != "" {
			mem.CurrentAvatarImageURL = &v
		} else if v, ok := m["currentAvatarImageUrl"].(string); ok && v != "" {
			mem.CurrentAvatarImageURL = &v
		}
		out = append(out, mem)
	}
	return out, nil
}

// GroupRequests lists pending join requests (GET /groups/{id}/requests).
func (c *Client) GroupRequests(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error) {
	return c.pagedGroupList(ctx, groupID, "requests", offset, n, nil)
}

// GroupBans lists banned members (GET /groups/{id}/bans).
func (c *Client) GroupBans(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error) {
	return c.pagedGroupList(ctx, groupID, "bans", offset, n, nil)
}

// GroupInvites lists outstanding invites (GET /groups/{id}/invites).
func (c *Client) GroupInvites(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error) {
	return c.pagedGroupList(ctx, groupID, "invites", offset, n, nil)
}

// GroupPosts lists group posts (GET /groups/{id}/posts). publicOnly adds ?publicOnly=true.
func (c *Client) GroupPosts(ctx context.Context, groupID string, offset, n int, publicOnly bool) ([]json.RawMessage, error) {
	var extra url.Values
	if publicOnly {
		extra = url.Values{"publicOnly": {"true"}}
	}
	return c.pagedGroupList(ctx, groupID, "posts", offset, n, extra)
}

// GroupAuditLogs lists moderation audit logs (GET /groups/{id}/auditLogs). Unwraps the
// {results,totalCount} envelope OR a bare array (API handles both). startDate/endDate optional.
func (c *Client) GroupAuditLogs(ctx context.Context, groupID string, offset, n int, startDate, endDate string) ([]json.RawMessage, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GroupAuditLogs: empty group id")
	}
	q := pageValues(offset, n)
	if startDate != "" {
		q.Set("startDate", startDate)
	}
	if endDate != "" {
		q.Set("endDate", endDate)
	}
	path := "/groups/" + url.PathEscape(groupID) + "/auditLogs"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	body, err := c.raw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Results []json.RawMessage `json:"results"`
	}
	if json.Unmarshal(body, &env) == nil && env.Results != nil {
		return env.Results, nil
	}
	return decodeList(body), nil
}

// ── group mutations ──────────────────────────────────────────────────────────

// RespondGroupRequest accepts/rejects a join request (PUT /groups/{id}/requests/{userId},
// body {action}). action ∈ accept|reject.
func (c *Client) RespondGroupRequest(ctx context.Context, groupID, userID, action string) (json.RawMessage, error) {
	if err := reqGroupUser(groupID, userID); err != nil {
		return nil, err
	}
	if action != "accept" && action != "reject" {
		return nil, fmt.Errorf("vrchat: action must be 'accept' or 'reject'")
	}
	return c.raw(ctx, http.MethodPut, "/groups/"+url.PathEscape(groupID)+"/requests/"+url.PathEscape(userID),
		map[string]any{"action": action})
}

// BanGroupMember bans a user (POST /groups/{id}/bans, body {userId}).
func (c *Client) BanGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	if err := reqGroupUser(groupID, userID); err != nil {
		return nil, err
	}
	return c.raw(ctx, http.MethodPost, "/groups/"+url.PathEscape(groupID)+"/bans",
		map[string]any{"userId": userID})
}

// UnbanGroupMember lifts a ban (DELETE /groups/{id}/bans/{userId}).
func (c *Client) UnbanGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	if err := reqGroupUser(groupID, userID); err != nil {
		return nil, err
	}
	return c.raw(ctx, http.MethodDelete, "/groups/"+url.PathEscape(groupID)+"/bans/"+url.PathEscape(userID), nil)
}

// KickGroupMember removes a member (DELETE /groups/{id}/members/{userId}).
func (c *Client) KickGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	if err := reqGroupUser(groupID, userID); err != nil {
		return nil, err
	}
	return c.raw(ctx, http.MethodDelete, "/groups/"+url.PathEscape(groupID)+"/members/"+url.PathEscape(userID), nil)
}

// InviteToGroup invites a user (POST /groups/{id}/invites, body {userId, confirmOverrideBlock?}).
func (c *Client) InviteToGroup(ctx context.Context, groupID, userID string, confirmOverrideBlock bool) (json.RawMessage, error) {
	if err := reqGroupUser(groupID, userID); err != nil {
		return nil, err
	}
	payload := map[string]any{"userId": userID}
	if confirmOverrideBlock {
		payload["confirmOverrideBlock"] = true
	}
	return c.raw(ctx, http.MethodPost, "/groups/"+url.PathEscape(groupID)+"/invites", payload)
}

// CancelGroupInvite rescinds an invite (DELETE /groups/{id}/invites/{userId}).
func (c *Client) CancelGroupInvite(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	if err := reqGroupUser(groupID, userID); err != nil {
		return nil, err
	}
	return c.raw(ctx, http.MethodDelete, "/groups/"+url.PathEscape(groupID)+"/invites/"+url.PathEscape(userID), nil)
}

// AddGroupRole grants a role (PUT /groups/{id}/members/{userId}/roles/{roleId}).
func (c *Client) AddGroupRole(ctx context.Context, groupID, userID, roleID string) (json.RawMessage, error) {
	if err := reqGroupUserRole(groupID, userID, roleID); err != nil {
		return nil, err
	}
	return c.raw(ctx, http.MethodPut,
		"/groups/"+url.PathEscape(groupID)+"/members/"+url.PathEscape(userID)+"/roles/"+url.PathEscape(roleID), nil)
}

// RemoveGroupRole revokes a role (DELETE /groups/{id}/members/{userId}/roles/{roleId}).
func (c *Client) RemoveGroupRole(ctx context.Context, groupID, userID, roleID string) (json.RawMessage, error) {
	if err := reqGroupUserRole(groupID, userID, roleID); err != nil {
		return nil, err
	}
	return c.raw(ctx, http.MethodDelete,
		"/groups/"+url.PathEscape(groupID)+"/members/"+url.PathEscape(userID)+"/roles/"+url.PathEscape(roleID), nil)
}

// GroupAnnouncement posts a group announcement (POST /groups/{id}/announcement). Projects the
// upstream response onto {id, title, text, createdAt, updatedAt} per API parity.
func (c *Client) GroupAnnouncement(ctx context.Context, groupID string, in AnnouncementIn) (map[string]any, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GroupAnnouncement: empty group id")
	}
	if in.Title == "" {
		return nil, fmt.Errorf("vrchat: announcement title required")
	}
	if in.Text == "" {
		return nil, fmt.Errorf("vrchat: announcement text required")
	}
	payload := map[string]any{"title": in.Title, "text": in.Text, "sendNotification": in.SendNotification}
	if in.ImageID != "" {
		payload["imageId"] = in.ImageID
	}
	body, err := c.raw(ctx, http.MethodPost, "/groups/"+url.PathEscape(groupID)+"/announcement", payload)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw) // tolerate empty/odd bodies; projection falls back to inputs
	return map[string]any{
		"id":        raw["id"],
		"title":     pickString(raw, "title", in.Title),
		"text":      pickString(raw, "text", in.Text),
		"createdAt": raw["createdAt"],
		"updatedAt": raw["updatedAt"],
	}, nil
}

// GroupCurrentAnnouncement fetches the group's current announcement
// (GET /groups/{id}/announcement). Raw passthrough; VRChat returns {} when none is set.
func (c *Client) GroupCurrentAnnouncement(ctx context.Context, groupID string) (json.RawMessage, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: GroupCurrentAnnouncement: empty group id")
	}
	return c.raw(ctx, http.MethodGet, "/groups/"+url.PathEscape(groupID)+"/announcement", nil)
}

// PostIn is the create-group-post body.
type PostIn struct {
	Title            string
	Text             string
	SendNotification bool
	ImageID          string // "" ⇒ omitted
	Visibility       string // "group"|"public"; "" ⇒ group
}

// CreateGroupPost creates a group post (POST /groups/{id}/posts). Raw passthrough.
func (c *Client) CreateGroupPost(ctx context.Context, groupID string, in PostIn) (json.RawMessage, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: CreateGroupPost: empty group id")
	}
	if in.Title == "" {
		return nil, fmt.Errorf("vrchat: post title required")
	}
	if in.Text == "" {
		return nil, fmt.Errorf("vrchat: post text required")
	}
	vis := in.Visibility
	if vis == "" {
		vis = "group"
	}
	payload := map[string]any{"title": in.Title, "text": in.Text, "sendNotification": in.SendNotification, "visibility": vis}
	if in.ImageID != "" {
		payload["imageId"] = in.ImageID
	}
	return c.raw(ctx, http.MethodPost, "/groups/"+url.PathEscape(groupID)+"/posts", payload)
}

// DeleteGroupPost deletes a group post (DELETE /groups/{id}/posts/{postId}). Raw passthrough.
func (c *Client) DeleteGroupPost(ctx context.Context, groupID, postID string) (json.RawMessage, error) {
	if groupID == "" || postID == "" {
		return nil, fmt.Errorf("vrchat: DeleteGroupPost: empty group/post id")
	}
	return c.raw(ctx, http.MethodDelete,
		"/groups/"+url.PathEscape(groupID)+"/posts/"+url.PathEscape(postID), nil)
}

// ── internals ────────────────────────────────────────────────────────────────

// raw runs an authed request (optional JSON body) and returns the raw 2xx body. 401 ⇒
// ErrUnauthorized; other non-2xx ⇒ error carrying VRChat's message. Session cookies via newReq.
func (c *Client) raw(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("vrchat: marshal body: %w", err)
		}
		r = strings.NewReader(string(b))
	}
	req, err := c.newReq(ctx, method, path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, respBody, err := c.do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, apiMessage(respBody))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("vrchat: %s %s HTTP %d: %s", method, req.URL.Path, resp.StatusCode, apiMessage(respBody))
	}
	return json.RawMessage(respBody), nil
}

// pagedGroupList GETs /groups/{id}/{sub}?n&offset(+extra) and returns list-or-single (API parity).
func (c *Client) pagedGroupList(ctx context.Context, groupID, sub string, offset, n int, extra url.Values) ([]json.RawMessage, error) {
	if groupID == "" {
		return nil, fmt.Errorf("vrchat: %s: empty group id", sub)
	}
	q := pageValues(offset, n)
	for k, vv := range extra {
		for _, v := range vv {
			q.Add(k, v)
		}
	}
	path := "/groups/" + url.PathEscape(groupID) + "/" + sub
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	body, err := c.raw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return decodeList(body), nil
}

// pageValues builds {n,offset} only for positive values (API buildPagination parity).
func pageValues(offset, n int) url.Values {
	q := url.Values{}
	if n > 0 {
		q.Set("n", strconv.Itoa(n))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return q
}

// decodeList mirrors Python's `result if isinstance(result, list) else [result]`.
// decodeList returns body as a list: bare array, a {posts|results|…:[…]} envelope
// (VRChat wraps some group lists, e.g. GET /groups/{id}/posts → {"posts":[…]}),
// or single object (API list-or-single parity).
func decodeList(body json.RawMessage) []json.RawMessage {
	var arr []json.RawMessage
	if json.Unmarshal(body, &arr) == nil {
		return arr
	}
	var env map[string]json.RawMessage
	if json.Unmarshal(body, &env) == nil {
		for _, k := range []string{"posts", "results", "requests", "invites", "bans", "instances"} {
			if v, ok := env[k]; ok && json.Unmarshal(v, &arr) == nil {
				return arr
			}
		}
	}
	if len(body) > 0 && string(body) != "null" {
		return []json.RawMessage{body}
	}
	return []json.RawMessage{}
}

func reqGroupUser(groupID, userID string) error {
	if groupID == "" {
		return fmt.Errorf("vrchat: group id required")
	}
	if userID == "" {
		return fmt.Errorf("vrchat: user id required")
	}
	return nil
}

func reqGroupUserRole(groupID, userID, roleID string) error {
	if err := reqGroupUser(groupID, userID); err != nil {
		return err
	}
	if roleID == "" {
		return fmt.Errorf("vrchat: role id required")
	}
	return nil
}

// ── member-normalization helpers (API routers/vrchat.py parity) ───────────────

func strPtr(m map[string]any, key string) *string {
	if v, ok := m[key].(string); ok && v != "" {
		return &v
	}
	return nil
}

func boolPtr(m map[string]any, key string) *bool {
	if v, ok := m[key].(bool); ok {
		return &v
	}
	return nil
}

func coalesceStr(primary, fallback map[string]any, key string) *string {
	if primary != nil {
		if v, ok := primary[key].(string); ok && v != "" {
			return &v
		}
	}
	if fallback != nil {
		if v, ok := fallback[key].(string); ok && v != "" {
			return &v
		}
	}
	return nil
}

func stringArr(m map[string]any, key string) []string {
	if v, ok := m[key].([]any); ok {
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{}
}

// coalesceTags returns user.currentAvatarTags || member.tags (API parity).
func coalesceTags(user, member map[string]any) []string {
	if user != nil {
		if v := stringArr(user, "currentAvatarTags"); len(v) > 0 {
			return v
		}
	}
	if member != nil {
		return stringArr(member, "tags")
	}
	return []string{}
}

func pickString(m map[string]any, key, fallback string) any {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}
