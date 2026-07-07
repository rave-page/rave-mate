package studio

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"rave.page/mate/internal/vrchat"
)

// VrchatGateway surfaces the desktop's LOCAL VRChat session to the web client (vrchat.* RPCs),
// so the browser queries/updates VRChat THROUGH rave-mate instead of the API vaulting the user's
// VRChat token. nil ⇒ VRChat unavailable: methods aren't advertised. Advertised per-connection
// only when Enabled() (config feature on) AND LoggedIn() (active sealed session). Every op acts on
// the local session's cookies - a caller-supplied token is NEVER accepted. Implemented in the app
// package over vrchat.Manager. All returned types carry VRChat's own JSON field names.
type VrchatGateway interface {
	Enabled() bool  // config feature flag
	LoggedIn() bool // active VRChat session
	CurrentUser(ctx context.Context) (*vrchat.User, error)
	UpdateStatus(ctx context.Context, status, description string) (*vrchat.User, error)
	UpdateBio(ctx context.Context, bio string, links []string) (*vrchat.User, error)
	Friends(ctx context.Context, offset, n int, offline bool) ([]vrchat.Friend, error)
	UserGroups(ctx context.Context, userID string) ([]vrchat.Group, error) // "" userID ⇒ own groups
	GroupRoles(ctx context.Context, groupID string) ([]vrchat.GroupRole, error)
	GroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]vrchat.GroupMember, error)
	SearchGroups(ctx context.Context, query string, offset, n int) ([]vrchat.Group, error)

	// Full group-management surface (ports the API's user-session cluster). Opaque VRChat
	// objects pass through as json.RawMessage; normalized shapes match the API byte-for-byte.
	ProfileLimits() vrchat.ProfileLimits
	ListGroups(ctx context.Context, userID string) ([]map[string]any, error) // "" ⇒ own groups
	GetGroup(ctx context.Context, groupID string) (json.RawMessage, error)
	GroupInstances(ctx context.Context, groupID string) (json.RawMessage, error)
	GroupPermissions(ctx context.Context, groupID string) (json.RawMessage, error)
	GroupMyPermissions(ctx context.Context, groupID string) (map[string]any, error)
	GroupMembersFull(ctx context.Context, groupID string, offset, n int) ([]vrchat.GroupMemberFull, error)
	GroupRequests(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error)
	RespondGroupRequest(ctx context.Context, groupID, userID, action string) (json.RawMessage, error)
	GroupBans(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error)
	BanGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error)
	UnbanGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error)
	KickGroupMember(ctx context.Context, groupID, userID string) (json.RawMessage, error)
	GroupInvites(ctx context.Context, groupID string, offset, n int) ([]json.RawMessage, error)
	InviteToGroup(ctx context.Context, groupID, userID string, confirmOverrideBlock bool) (json.RawMessage, error)
	CancelGroupInvite(ctx context.Context, groupID, userID string) (json.RawMessage, error)
	AddGroupRole(ctx context.Context, groupID, userID, roleID string) (json.RawMessage, error)
	RemoveGroupRole(ctx context.Context, groupID, userID, roleID string) (json.RawMessage, error)
	GroupPosts(ctx context.Context, groupID string, offset, n int, publicOnly bool) ([]json.RawMessage, error)
	GroupAuditLogs(ctx context.Context, groupID string, offset, n int, startDate, endDate string) ([]json.RawMessage, error)
	GroupAnnouncement(ctx context.Context, groupID string, in vrchat.AnnouncementIn) (map[string]any, error)
	GroupCurrentAnnouncement(ctx context.Context, groupID string) (json.RawMessage, error)
	CreateGroupPost(ctx context.Context, groupID string, in vrchat.PostIn) (json.RawMessage, error)
	DeleteGroupPost(ctx context.Context, groupID, postID string) (json.RawMessage, error)
}

// vrchatMethods are the VRChat RPCs, advertised in capabilities only when the gateway is wired,
// the feature is enabled, and a session is live (see Server.capabilities). Not in studioMethods -
// like peers.*, they're handled locally and never forwarded to a remote context.
//
// TODO(vrchat): worlds/notifications/verify-membership are NOT ported - the client ops
// don't exist in internal/vrchat yet; add thin client methods there first, then expose here.
var vrchatMethods = []string{
	"vrchat.currentUser",
	"vrchat.status",
	"vrchat.setStatus",
	"vrchat.setBio",
	"vrchat.friends",
	"vrchat.userGroups",
	"vrchat.groupRoles",
	"vrchat.groupMembers",
	"vrchat.searchGroups",
	"vrchat.testConnection",
	// Full group-management surface.
	"vrchat.profileLimits",
	"vrchat.groups",
	"vrchat.group",
	"vrchat.groupInstances",
	"vrchat.groupMembersFull",
	"vrchat.groupMyPermissions",
	"vrchat.groupPermissions",
	"vrchat.groupRequests",
	"vrchat.respondGroupRequest",
	"vrchat.groupBans",
	"vrchat.banGroupMember",
	"vrchat.unbanGroupMember",
	"vrchat.kickGroupMember",
	"vrchat.groupInvites",
	"vrchat.inviteToGroup",
	"vrchat.cancelGroupInvite",
	"vrchat.addGroupRole",
	"vrchat.removeGroupRole",
	"vrchat.groupPosts",
	"vrchat.groupAuditLogs",
	"vrchat.groupAnnouncement",
	"vrchat.groupCurrentAnnouncement",
	"vrchat.createGroupPost",
	"vrchat.deleteGroupPost",
}

func isVrchatMethod(m string) bool {
	switch m {
	case "vrchat.currentUser", "vrchat.status", "vrchat.setStatus", "vrchat.setBio",
		"vrchat.friends", "vrchat.userGroups", "vrchat.groupRoles", "vrchat.groupMembers",
		"vrchat.searchGroups", "vrchat.testConnection",
		"vrchat.profileLimits", "vrchat.groups", "vrchat.group", "vrchat.groupInstances",
		"vrchat.groupMembersFull", "vrchat.groupMyPermissions", "vrchat.groupPermissions",
		"vrchat.groupRequests", "vrchat.respondGroupRequest", "vrchat.groupBans",
		"vrchat.banGroupMember", "vrchat.unbanGroupMember", "vrchat.kickGroupMember",
		"vrchat.groupInvites", "vrchat.inviteToGroup", "vrchat.cancelGroupInvite",
		"vrchat.addGroupRole", "vrchat.removeGroupRole", "vrchat.groupPosts",
		"vrchat.groupAuditLogs", "vrchat.groupAnnouncement",
		"vrchat.groupCurrentAnnouncement", "vrchat.createGroupPost", "vrchat.deleteGroupPost":
		return true
	}
	return false
}

// ── result shapes (params reuse the wire map; results are concrete + JSON-tagged) ─────────────

// vrchatStatus is vrchat.status (get): current presence + status text.
type vrchatStatus struct {
	Status            string `json:"status"`
	StatusDescription string `json:"statusDescription"`
}

// vrchatConn is vrchat.testConnection: session health. status ∈ ok|expired|invalid.
type vrchatConn struct {
	Status      string `json:"status"`
	UserID      string `json:"userId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Message     string `json:"message,omitempty"`
}

// vrchatCall runs one vrchat.* method off the read loop (VRChat HTTP ≤20s) and replies. Only the
// local session is touched; no wire token is read. testConnection works signed-out (reports it);
// every other method requires a live session.
func (s *session) vrchatCall(method, id string, p map[string]any) {
	gw := s.srv.vrchat
	if gw == nil || !gw.Enabled() {
		s.sendErr(id, errUnknownMethod, "vrchat unavailable")
		return
	}
	if method != "vrchat.testConnection" && !gw.LoggedIn() {
		s.sendErr(id, errUnauthorized, "vrchat not signed in")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	result, code, err := dispatchVrchat(ctx, gw, method, p)
	if err != nil {
		s.sendErr(id, code, err.Error())
		return
	}
	s.send(map[string]any{"t": "res", "id": id, "ok": true, "result": result})
}

// dispatchVrchat maps a method to the gateway. Returns (result, errCode, err).
func dispatchVrchat(ctx context.Context, gw VrchatGateway, method string, p map[string]any) (any, errorCode, error) {
	switch method {
	case "vrchat.currentUser":
		return vrchatErr(gw.CurrentUser(ctx))
	case "vrchat.status":
		u, err := gw.CurrentUser(ctx)
		if err != nil {
			return nil, vrchatCode(err), err
		}
		return vrchatStatus{Status: u.Status, StatusDescription: u.StatusDescription}, "", nil
	case "vrchat.setStatus":
		return vrchatErr(gw.UpdateStatus(ctx, asString(p["status"]), asString(p["statusDescription"])))
	case "vrchat.setBio":
		// nil links ⇒ leave bioLinks untouched; a present (possibly empty) array rewrites them.
		var links []string
		if _, ok := p["bioLinks"]; ok {
			links = asStringSlice(p["bioLinks"])
		}
		return vrchatErr(gw.UpdateBio(ctx, asString(p["bio"]), links))
	case "vrchat.friends":
		out, err := gw.Friends(ctx, intOf(p["offset"]), intOf(p["n"]), asBool(p["offline"]))
		if err != nil {
			return nil, vrchatCode(err), err
		}
		return out, "", nil
	case "vrchat.userGroups":
		out, err := gw.UserGroups(ctx, asString(p["userId"]))
		if err != nil {
			return nil, vrchatCode(err), err
		}
		return out, "", nil
	case "vrchat.groupRoles":
		out, err := gw.GroupRoles(ctx, asString(p["groupId"]))
		if err != nil {
			return nil, vrchatCode(err), err
		}
		return out, "", nil
	case "vrchat.groupMembers":
		out, err := gw.GroupMembers(ctx, asString(p["groupId"]), asString(p["roleId"]), intOf(p["offset"]), intOf(p["n"]))
		if err != nil {
			return nil, vrchatCode(err), err
		}
		return out, "", nil
	case "vrchat.searchGroups":
		out, err := gw.SearchGroups(ctx, asString(p["query"]), intOf(p["offset"]), intOf(p["n"]))
		if err != nil {
			return nil, vrchatCode(err), err
		}
		return out, "", nil
	case "vrchat.testConnection":
		if !gw.LoggedIn() {
			return vrchatConn{Status: "invalid", Message: "no vrchat session"}, "", nil
		}
		u, err := gw.CurrentUser(ctx)
		switch {
		case err == nil:
			return vrchatConn{Status: "ok", UserID: u.ID, DisplayName: u.DisplayName}, "", nil
		case errors.Is(err, vrchat.ErrUnauthorized), errors.Is(err, vrchat.Err2FARequired):
			return vrchatConn{Status: "expired", Message: err.Error()}, "", nil
		default:
			return vrchatConn{Status: "invalid", Message: err.Error()}, "", nil
		}

	// ── full group-management surface ────────────────────────────────────────
	case "vrchat.profileLimits":
		return gw.ProfileLimits(), "", nil
	case "vrchat.groups":
		return vrchatOut(gw.ListGroups(ctx, asString(p["userId"])))
	case "vrchat.group":
		return vrchatOut(gw.GetGroup(ctx, asString(p["groupId"])))
	case "vrchat.groupInstances":
		return vrchatOut(gw.GroupInstances(ctx, asString(p["groupId"])))
	case "vrchat.groupPermissions":
		return vrchatOut(gw.GroupPermissions(ctx, asString(p["groupId"])))
	case "vrchat.groupMyPermissions":
		return vrchatOut(gw.GroupMyPermissions(ctx, asString(p["groupId"])))
	case "vrchat.groupMembersFull":
		return vrchatOut(gw.GroupMembersFull(ctx, asString(p["groupId"]), intOf(p["offset"]), intOf(p["n"])))
	case "vrchat.groupRequests":
		return vrchatOut(gw.GroupRequests(ctx, asString(p["groupId"]), intOf(p["offset"]), intOf(p["n"])))
	case "vrchat.respondGroupRequest":
		return vrchatOut(gw.RespondGroupRequest(ctx, asString(p["groupId"]), asString(p["userId"]), asString(p["action"])))
	case "vrchat.groupBans":
		return vrchatOut(gw.GroupBans(ctx, asString(p["groupId"]), intOf(p["offset"]), intOf(p["n"])))
	case "vrchat.banGroupMember":
		return vrchatOut(gw.BanGroupMember(ctx, asString(p["groupId"]), asString(p["userId"])))
	case "vrchat.unbanGroupMember":
		return vrchatOut(gw.UnbanGroupMember(ctx, asString(p["groupId"]), asString(p["userId"])))
	case "vrchat.kickGroupMember":
		return vrchatOut(gw.KickGroupMember(ctx, asString(p["groupId"]), asString(p["userId"])))
	case "vrchat.groupInvites":
		return vrchatOut(gw.GroupInvites(ctx, asString(p["groupId"]), intOf(p["offset"]), intOf(p["n"])))
	case "vrchat.inviteToGroup":
		return vrchatOut(gw.InviteToGroup(ctx, asString(p["groupId"]), asString(p["userId"]), asBool(p["confirmOverrideBlock"])))
	case "vrchat.cancelGroupInvite":
		return vrchatOut(gw.CancelGroupInvite(ctx, asString(p["groupId"]), asString(p["userId"])))
	case "vrchat.addGroupRole":
		return vrchatOut(gw.AddGroupRole(ctx, asString(p["groupId"]), asString(p["userId"]), asString(p["roleId"])))
	case "vrchat.removeGroupRole":
		return vrchatOut(gw.RemoveGroupRole(ctx, asString(p["groupId"]), asString(p["userId"]), asString(p["roleId"])))
	case "vrchat.groupPosts":
		return vrchatOut(gw.GroupPosts(ctx, asString(p["groupId"]), intOf(p["offset"]), intOf(p["n"]), asBool(p["publicOnly"])))
	case "vrchat.groupAuditLogs":
		return vrchatOut(gw.GroupAuditLogs(ctx, asString(p["groupId"]), intOf(p["offset"]), intOf(p["n"]), asString(p["startDate"]), asString(p["endDate"])))
	case "vrchat.groupCurrentAnnouncement":
		return vrchatOut(gw.GroupCurrentAnnouncement(ctx, asString(p["groupId"])))
	case "vrchat.createGroupPost":
		return vrchatOut(gw.CreateGroupPost(ctx, asString(p["groupId"]), vrchat.PostIn{
			Title:            asString(p["title"]),
			Text:             asString(p["text"]),
			SendNotification: asBool(p["sendNotification"]),
			ImageID:          asString(p["imageId"]),
			Visibility:       asString(p["visibility"]),
		}))
	case "vrchat.deleteGroupPost":
		return vrchatOut(gw.DeleteGroupPost(ctx, asString(p["groupId"]), asString(p["postId"])))
	case "vrchat.groupAnnouncement":
		return vrchatOut(gw.GroupAnnouncement(ctx, asString(p["groupId"]), vrchat.AnnouncementIn{
			Title:            asString(p["title"]),
			Text:             asString(p["text"]),
			SendNotification: asBool(p["sendNotification"]),
			ImageID:          asString(p["imageId"]),
		}))
	}
	return nil, errUnknownMethod, errors.New("unknown vrchat method " + method)
}

// vrchatOut wraps a (result, error) return into the dispatch triple, mapping the error to a
// wire code. Used by the group-management cases.
func vrchatOut[T any](v T, err error) (any, errorCode, error) {
	if err != nil {
		return nil, vrchatCode(err), err
	}
	return v, "", nil
}

// vrchatErr wraps a (*User, error) return into the dispatch triple.
func vrchatErr(u *vrchat.User, err error) (any, errorCode, error) {
	if err != nil {
		return nil, vrchatCode(err), err
	}
	return u, "", nil
}

// vrchatCode maps a VRChat client error to a wire error code.
func vrchatCode(err error) errorCode {
	if errors.Is(err, vrchat.ErrUnauthorized) || errors.Is(err, vrchat.Err2FARequired) {
		return errUnauthorized
	}
	return errInternal
}
