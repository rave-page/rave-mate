package app

import (
	"context"
	"encoding/json"

	"rave.page/mate/internal/studio"
	"rave.page/mate/internal/vrchat"
)

// vrchatGateway adapts the in-proc vrchat.Manager to studio.VrchatGateway so the web Local Studio
// can query/update this desktop's VRChat session through rave-mate. Read/write ops act only on the
// manager's sealed local session - no caller token is ever accepted. enabled reads the live config
// feature flag.
type vrchatGateway struct {
	mgr     *vrchat.Manager
	enabled func() bool
}

func newVrchatGateway(mgr *vrchat.Manager, enabled func() bool) *vrchatGateway {
	return &vrchatGateway{mgr: mgr, enabled: enabled}
}

func (g *vrchatGateway) Enabled() bool  { return g.enabled != nil && g.enabled() }
func (g *vrchatGateway) LoggedIn() bool { return g.mgr.State().LoggedIn }

func (g *vrchatGateway) CurrentUser(ctx context.Context) (*vrchat.User, error) {
	return g.mgr.FetchUser(ctx)
}

func (g *vrchatGateway) UpdateStatus(ctx context.Context, status, description string) (*vrchat.User, error) {
	return g.mgr.UpdateStatus(ctx, status, description)
}

func (g *vrchatGateway) UpdateBio(ctx context.Context, bio string, links []string) (*vrchat.User, error) {
	return g.mgr.UpdateBio(ctx, bio, links)
}

func (g *vrchatGateway) Friends(ctx context.Context, offset, n int, offline bool) ([]vrchat.Friend, error) {
	return g.mgr.Client().Friends(ctx, offset, n, offline)
}

// UserGroups defaults an empty id to the logged-in user's own groups.
func (g *vrchatGateway) UserGroups(ctx context.Context, userID string) ([]vrchat.Group, error) {
	if userID == "" {
		userID = g.mgr.CurrentUserID()
	}
	if userID == "" {
		return nil, vrchat.ErrUnauthorized
	}
	return g.mgr.Client().UserGroups(ctx, userID)
}

func (g *vrchatGateway) GroupRoles(ctx context.Context, groupID string) ([]vrchat.GroupRole, error) {
	return g.mgr.Client().GroupRoles(ctx, groupID)
}

func (g *vrchatGateway) GroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]vrchat.GroupMember, error) {
	return g.mgr.Client().GroupMembers(ctx, groupID, roleID, offset, n)
}

func (g *vrchatGateway) SearchGroups(ctx context.Context, query string, offset, n int) ([]vrchat.Group, error) {
	return g.mgr.Client().SearchGroups(ctx, query, offset, n)
}

// ── full group-management surface (over the local sealed session) ─────────────

func (g *vrchatGateway) ProfileLimits() vrchat.ProfileLimits { return vrchat.DefaultProfileLimits() }

// ListGroups defaults an empty id to the logged-in user's own groups.
func (g *vrchatGateway) ListGroups(ctx context.Context, userID string) ([]map[string]any, error) {
	if userID == "" {
		userID = g.mgr.CurrentUserID()
	}
	if userID == "" {
		return nil, vrchat.ErrUnauthorized
	}
	return g.mgr.Client().ListGroups(ctx, userID)
}

func (g *vrchatGateway) GetGroup(ctx context.Context, groupID string) (json.RawMessage, error) {
	return g.mgr.Client().GetGroup(ctx, groupID)
}

func (g *vrchatGateway) GroupInstances(ctx context.Context, groupID string) (json.RawMessage, error) {
	return g.mgr.Client().GroupInstances(ctx, groupID)
}

func (g *vrchatGateway) GroupPermissions(ctx context.Context, groupID string) (json.RawMessage, error) {
	return g.mgr.Client().GroupPermissions(ctx, groupID)
}

func (g *vrchatGateway) GroupMyPermissions(ctx context.Context, groupID string) (map[string]any, error) {
	return g.mgr.Client().GroupMyPermissions(ctx, groupID)
}

func (g *vrchatGateway) GroupMembersFull(ctx context.Context, groupID string, offset, n int) ([]vrchat.GroupMemberFull, error) {
	return g.mgr.Client().GroupMembersFull(ctx, groupID, offset, n)
}

func (g *vrchatGateway) GroupRequests(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error) {
	return g.mgr.Client().GroupRequests(ctx, groupID, offset, n)
}

func (g *vrchatGateway) RespondGroupRequest(ctx context.Context, groupID, userID, action string) (json.RawMessage, error) {
	return g.mgr.Client().RespondGroupRequest(ctx, groupID, userID, action)
}

func (g *vrchatGateway) GroupBans(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error) {
	return g.mgr.Client().GroupBans(ctx, groupID, offset, n)
}

func (g *vrchatGateway) BanGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	return g.mgr.Client().BanGroupMember(ctx, groupID, userID)
}

func (g *vrchatGateway) UnbanGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	return g.mgr.Client().UnbanGroupMember(ctx, groupID, userID)
}

func (g *vrchatGateway) KickGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	return g.mgr.Client().KickGroupMember(ctx, groupID, userID)
}

func (g *vrchatGateway) GroupInvites(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error) {
	return g.mgr.Client().GroupInvites(ctx, groupID, offset, n)
}

func (g *vrchatGateway) InviteToGroup(ctx context.Context, groupID, userID string, confirmOverrideBlock bool) (json.RawMessage, error) {
	return g.mgr.Client().InviteToGroup(ctx, groupID, userID, confirmOverrideBlock)
}

func (g *vrchatGateway) CancelGroupInvite(ctx context.Context, groupID, userID string) (json.RawMessage, error) {
	return g.mgr.Client().CancelGroupInvite(ctx, groupID, userID)
}

func (g *vrchatGateway) AddGroupRole(ctx context.Context, groupID, userID, roleID string) (json.RawMessage, error) {
	return g.mgr.Client().AddGroupRole(ctx, groupID, userID, roleID)
}

func (g *vrchatGateway) RemoveGroupRole(ctx context.Context, groupID, userID, roleID string) (json.RawMessage, error) {
	return g.mgr.Client().RemoveGroupRole(ctx, groupID, userID, roleID)
}

func (g *vrchatGateway) GroupPosts(ctx context.Context, groupID string, offset, n int, publicOnly bool) ([]json.RawMessage, error) {
	return g.mgr.Client().GroupPosts(ctx, groupID, offset, n, publicOnly)
}

func (g *vrchatGateway) GroupAuditLogs(ctx context.Context, groupID string, offset, n int, startDate, endDate string) ([]json.RawMessage, error) {
	return g.mgr.Client().GroupAuditLogs(ctx, groupID, offset, n, startDate, endDate)
}

func (g *vrchatGateway) GroupAnnouncement(ctx context.Context, groupID string, in vrchat.AnnouncementIn) (map[string]any, error) {
	return g.mgr.Client().GroupAnnouncement(ctx, groupID, in)
}

func (g *vrchatGateway) GroupCurrentAnnouncement(ctx context.Context, groupID string) (json.RawMessage, error) {
	return g.mgr.Client().GroupCurrentAnnouncement(ctx, groupID)
}

func (g *vrchatGateway) CreateGroupPost(ctx context.Context, groupID string, in vrchat.PostIn) (json.RawMessage, error) {
	return g.mgr.Client().CreateGroupPost(ctx, groupID, in)
}

func (g *vrchatGateway) DeleteGroupPost(ctx context.Context, groupID, postID string) (json.RawMessage, error) {
	return g.mgr.Client().DeleteGroupPost(ctx, groupID, postID)
}

var _ studio.VrchatGateway = (*vrchatGateway)(nil)
