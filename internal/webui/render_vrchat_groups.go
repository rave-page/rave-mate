package webui

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/zigui"
)

// VRChat ▸ Groups sub-tab renderer (state + handlers in vrchat_groups_actions.go). Renders from
// vgState snapshots; picker/friend row order is written back into vgState (shown/shownFriends) so
// index-based actions stay pinned to what the user sees. UX reimplements the web app's group-
// management panels (roles/members/moderation) for the native workspace.
//
// Zig-rendered (native/zigui/src/vrcgroups.zig): vrcgBodyState resolves everything impure
// (locks, session, perms, i18n, timestamps); vrcgBodyHTML below stays the Go fallback + golden
// reference (zigui_golden_vrchat_test.go). The dialog surfaces (role body, invite list, the two
// picker shells, kick/ban + post-delete confirms) live in render_vrchat_groups_modals.go.

// ── resolved render state (JSON → Zig) ──

// vgBadgeSt is one rp-badge {text,variant}.
type vgBadgeSt struct {
	Text    string `json:"text"`
	Variant string `json:"variant"`
}

// vgBtnSt is one action button.
type vgBtnSt struct {
	Label   string `json:"label"`
	Variant string `json:"variant"`
	Act     string `json:"act"`
}

// vgKVSt is one kv row; DL = Go-lowered data-label (Unicode lowering stays in Go).
type vgKVSt struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Value string `json:"value"`
}

// vgTabSt is one workspace view tab ([value,label]).
type vgTabSt struct {
	Val   string `json:"val"`
	Label string `json:"label"`
}

// vgPagerSt is the resolved Load-more / cap footer. Mode ∈ {"",loading,more,cap}.
type vgPagerSt struct {
	Mode  string `json:"mode"`
	Msg   string `json:"msg"`
	Label string `json:"label"`
	Act   string `json:"act"`
}

// vgPickerRowSt is one group row in the picker (Meta = "shortCode · N members").
type vgPickerRowSt struct {
	Idx  int    `json:"idx"`
	Name string `json:"name"`
	Meta string `json:"meta"`
}

// vgPickerSt is the "my groups" picker. State ∈ {loading,none,nomatch,rows}.
type vgPickerSt struct {
	Title   string          `json:"title"`
	Refresh string          `json:"refresh"`
	Filter  string          `json:"filter"`
	State   string          `json:"state"`
	Msg     string          `json:"msg"`
	Rows    []vgPickerRowSt `json:"rows,omitempty"`
}

// vgRoleSt is one enriched role row on the overview.
type vgRoleSt struct {
	Name    string      `json:"name"`
	Tags    []vgBadgeSt `json:"tags,omitempty"`
	Order   string      `json:"order"`   // resolved "Order n"
	Desc    string      `json:"desc"`    // "" = no sub-line
	PermSum string      `json:"permSum"` // resolved <summary> count; "" = no details
	Perms   []string    `json:"perms,omitempty"`
}

// vgOverviewSt is the overview view (about + my permissions + roles).
type vgOverviewSt struct {
	CardTitle  string      `json:"cardTitle"` // "Overview" (loading/error card)
	Loading    bool        `json:"loading"`
	LoadingMsg string      `json:"loadingMsg"`
	Missing    bool        `json:"missing"` // group info could not be loaded
	MissingMsg string      `json:"missingMsg"`
	AboutTitle string      `json:"aboutTitle"`
	Desc       string      `json:"desc"` // "" = none
	KVs        []vgKVSt    `json:"kvs,omitempty"`
	RulesTitle string      `json:"rulesTitle"`
	Rules      string      `json:"rules"` // "" = no rules block
	PermsTitle string      `json:"permsTitle"`
	PermsMode  string      `json:"permsMode"` // owner|none|list
	PermsMsg   string      `json:"permsMsg"`  // owner badge text / no-perms hint
	PermBadges []vgBadgeSt `json:"permBadges,omitempty"`
	RolesTitle string      `json:"rolesTitle"`
	RolesEmpty string      `json:"rolesEmpty"` // emptyState msg when no roles
	Roles      []vgRoleSt  `json:"roles,omitempty"`
}

// vgMemberRowSt is one member row.
type vgMemberRowSt struct {
	Name string      `json:"name"`
	Tags []vgBadgeSt `json:"tags,omitempty"`
	Meta string      `json:"meta"`
	Acts []vgBtnSt   `json:"acts,omitempty"`
}

// vgMembersSt is the members view. State ∈ {loading,notloaded,empty,rows}.
type vgMembersSt struct {
	CardTitle string          `json:"cardTitle"`
	State     string          `json:"state"`
	Msg       string          `json:"msg"`
	Rows      []vgMemberRowSt `json:"rows,omitempty"`
	Pager     vgPagerSt       `json:"pager"`
}

// vgUserRowSt is one request/invite/ban row.
type vgUserRowSt struct {
	Name string    `json:"name"`
	Sub  string    `json:"sub"`
	Acts []vgBtnSt `json:"acts,omitempty"`
}

// vgUsersSt is the shared requests/invites/bans list view.
type vgUsersSt struct {
	CardTitle string        `json:"cardTitle"`
	Head      []vgBtnSt     `json:"head,omitempty"` // btnRow above the list (invites only)
	State     string        `json:"state"`
	Msg       string        `json:"msg"`
	Empty     string        `json:"empty"`
	Rows      []vgUserRowSt `json:"rows,omitempty"`
	Pager     vgPagerSt     `json:"pager"`
}

// vgPostRowSt is one group post.
type vgPostRowSt struct {
	Title string    `json:"title"`
	Meta  string    `json:"meta"`
	Text  string    `json:"text"`
	Del   []vgBtnSt `json:"del,omitempty"` // delete action (empty = not permitted)
}

// vgPostsSt is the posts view (announcement + composer forms + post feed).
type vgPostsSt struct {
	AnnTitle     string        `json:"annTitle"`           // "Current announcement"
	AnnTip       string        `json:"annTip"`             // legacy RAW tooltip markup (bridge)
	AnnTipS      *tipSt        `json:"annTipSt,omitempty"` // structured tooltip - wins over AnnTip
	HasAnn       bool          `json:"hasAnn"`
	AnnHead      string        `json:"annHead"`
	AnnWhen      string        `json:"annWhen"`
	AnnText      string        `json:"annText"`
	AnnEmpty     bool          `json:"annEmpty"` // posts loaded, no announcement
	AnnEmptyMsg  string        `json:"annEmptyMsg"`
	CanAnn       bool          `json:"canAnn"`
	NewAnnTitle  string        `json:"newAnnTitle"`
	NewPostTitle string        `json:"newPostTitle"`
	FTitle       string        `json:"fTitle"`  // "Title"
	FText        string        `json:"fText"`   // "Text"
	FImage       string        `json:"fImage"`  // "Image ID (optional)"
	FNotify      string        `json:"fNotify"` // "Send notification to members"
	AnnSubmit    string        `json:"annSubmit"`
	AnnHint      string        `json:"annHint"`
	PostSubmit   string        `json:"postSubmit"`
	PostHint     string        `json:"postHint"`
	CardTitle    string        `json:"cardTitle"` // "Posts - n loaded"
	State        string        `json:"state"`
	Msg          string        `json:"msg"`
	Empty        string        `json:"empty"`
	Rows         []vgPostRowSt `json:"rows,omitempty"`
	Pager        vgPagerSt     `json:"pager"`
}

// vgAuditRowSt is one audit-log entry.
type vgAuditRowSt struct {
	When  string `json:"when"`
	Event string `json:"event"`
	Actor string `json:"actor"`
	Desc  string `json:"desc"`
	Raw   string `json:"raw"`
}

// vgAuditSt is the audit-log view.
type vgAuditSt struct {
	CardTitle  string         `json:"cardTitle"`
	NoPerm     bool           `json:"noPerm"`
	NoPermMsg  string         `json:"noPermMsg"`
	State      string         `json:"state"`
	Msg        string         `json:"msg"`
	Empty      string         `json:"empty"`
	RawSummary string         `json:"rawSummary"` // <summary> label ("raw entry")
	Rows       []vgAuditRowSt `json:"rows,omitempty"`
	Pager      vgPagerSt      `json:"pager"`
}

// vgWorkspaceSt is the opened-group workspace (header + active view).
type vgWorkspaceSt struct {
	Title    string       `json:"title"`
	Refresh  string       `json:"refresh"`
	Back     string       `json:"back"`
	Badges   []vgBadgeSt  `json:"badges,omitempty"`
	View     string       `json:"view"`
	Tabs     []vgTabSt    `json:"tabs,omitempty"`
	Overview vgOverviewSt `json:"overview"`
	Members  vgMembersSt  `json:"members"`
	Users    vgUsersSt    `json:"users"`
	Posts    vgPostsSt    `json:"posts"`
	Audit    vgAuditSt    `json:"audit"`
}

// vrcgState is the resolved render state for the Groups sub-tab. Mode ∈ {picker,workspace}.
type vrcgState struct {
	Available   bool          `json:"available"`
	Unavailable string        `json:"unavailable"`
	SignedIn    bool          `json:"signedIn"`
	SignInTitle string        `json:"signInTitle"`
	SignInHint  string        `json:"signInHint"`
	Mode        string        `json:"mode"`
	Picker      vgPickerSt    `json:"picker"`
	WS          vgWorkspaceSt `json:"ws"`
}

// ── bridge ──

// vrcgBody is the Groups sub-tab root (patched whole via vgPatch).
func (u *UI) vrcgBody() string {
	st := u.vrcgBodyState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVRCGroupsV2", wireVrcg(st), zigui.RenderVRCGroupsV2,
			zigui.RenderVRCGroups, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vrcgBodyHTML(st)
}

// vrcgBodyState resolves session + vgState + i18n into render state (the only impure step:
// lazy group load, lock-held snapshots, picker index write-back).
func (u *UI) vrcgBodyState() vrcgState {
	st := vrcgState{
		Available:   u.svc.Vrchat != nil,
		Unavailable: i18n.T("vrchat.unavailable"),
		SignInTitle: i18n.T("vrchat.subtab.groups"),
		SignInHint:  i18n.T("vrchat.groups.hint.signInToManage"),
		Mode:        "picker",
	}
	if !st.Available {
		return st
	}
	if !u.svc.Vrchat.State().LoggedIn {
		return st
	}
	st.SignedIn = true
	u.vgLoadGroups(false) // lazy first load (single-flight)
	vgState.mu.Lock()
	sel := vgState.selID
	vgState.mu.Unlock()
	if sel == "" {
		st.Picker = u.vgPickerState()
		return st
	}
	st.Mode = "workspace"
	st.WS = u.vgWorkspaceState()
	return st
}

// ── group picker ──

// vgPickerState snapshots the filtered group list and pins the shown order for index actions.
func (u *UI) vgPickerState() vgPickerSt {
	vgState.mu.Lock()
	defer vgState.mu.Unlock()
	loading := vgState.groupsLoading || !vgState.groupsLoaded
	q := strings.ToLower(strings.TrimSpace(vgState.filter))
	shown := vgState.shown[:0]
	for _, g := range vgState.groups {
		if q == "" || strings.Contains(strings.ToLower(g.Name), q) || strings.Contains(strings.ToLower(g.ShortCode), q) {
			shown = append(shown, g)
		}
	}
	vgState.shown = shown

	p := vgPickerSt{
		Title:   i18n.T("vrchat.groups.myGroupsTitle"),
		Refresh: i18n.T("common.refresh"),
		Filter:  vgState.filter,
		Rows:    []vgPickerRowSt{},
	}
	switch {
	case loading:
		p.State, p.Msg = "loading", i18n.T("vrchat.groups.loadingGroups")
	case len(vgState.groups) == 0:
		p.State, p.Msg = "none", i18n.T("vrchat.groups.noneFound")
	case len(shown) == 0:
		p.State, p.Msg = "nomatch", i18n.T("vrchat.groups.noMatch")
	default:
		p.State = "rows"
		for i, g := range shown {
			p.Rows = append(p.Rows, vgPickerRowSt{
				Idx:  i,
				Name: g.Name,
				Meta: g.ShortCode + " · " + i18n.Tn("vrchat.groups.members", g.MemberCount),
			})
		}
	}
	return p
}

// ── workspace ──

// vgWorkspaceState resolves the opened group's header + the active view's state.
func (u *UI) vgWorkspaceState() vgWorkspaceSt {
	vgState.mu.Lock()
	info := vgState.info
	name := vgState.selName
	view := vgState.view
	owner := vgState.owner
	vgState.mu.Unlock()
	if view == "" {
		view = "overview"
	}
	title := name
	if info != nil && info.Name != "" {
		title = info.Name
	}
	ws := vgWorkspaceSt{
		Title:   title,
		Refresh: i18n.T("common.refresh"),
		Back:    i18n.T("vrchat.groups.backToMyGroups"),
		Badges:  []vgBadgeSt{},
		View:    view,
		Tabs: []vgTabSt{
			{"overview", i18n.T("vrchat.groups.tab.overview")}, {"members", i18n.T("vrchat.groups.tab.members")},
			{"requests", i18n.T("vrchat.groups.tab.requests")}, {"invites", i18n.T("vrchat.groups.tab.invites")},
			{"bans", i18n.T("vrchat.groups.tab.bans")}, {"posts", i18n.T("vrchat.groups.tab.posts")},
			{"audit", i18n.T("vrchat.groups.tab.auditLog")},
		},
	}
	if info != nil {
		if info.ShortCode != "" {
			ws.Badges = append(ws.Badges, vgBadgeSt{info.ShortCode, "outline"})
		}
		ws.Badges = append(ws.Badges, vgBadgeSt{i18n.Tn("vrchat.groups.members", info.MemberCount), "secondary"})
		if info.OnlineCount > 0 {
			ws.Badges = append(ws.Badges, vgBadgeSt{i18n.T("vrchat.groups.onlineCount", i18n.A{"count": fmt.Sprint(info.OnlineCount)}), "success"})
		}
		if info.Privacy != "" {
			ws.Badges = append(ws.Badges, vgBadgeSt{info.Privacy, "info"})
		}
		if info.JoinState != "" {
			ws.Badges = append(ws.Badges, vgBadgeSt{i18n.T("vrchat.groups.joinState", i18n.A{"state": info.JoinState}), "info"})
		}
		if info.IsVerified {
			ws.Badges = append(ws.Badges, vgBadgeSt{i18n.T("vrchat.groups.verified"), "success"})
		}
		if owner {
			ws.Badges = append(ws.Badges, vgBadgeSt{i18n.T("vrchat.groups.youOwnThisGroup"), "success"})
		}
	}
	switch view {
	case "members":
		ws.Members = u.vgMembersState()
	case "requests", "invites", "bans":
		ws.Users = u.vgUsersState(view)
	case "posts":
		ws.Posts = u.vgPostsState()
	case "audit":
		ws.Audit = u.vgAuditState()
	default:
		ws.Overview = u.vgOverviewState()
	}
	return ws
}

// ── overview: about + my permissions + enriched roles ──

func (u *UI) vgOverviewState() vgOverviewSt {
	vgState.mu.Lock()
	info := vgState.info
	roles := vgState.roles
	owner := vgState.owner
	perms := make([]string, 0, len(vgState.perms))
	for p := range vgState.perms {
		perms = append(perms, p)
	}
	loading := vgState.ovLoading
	vgState.mu.Unlock()
	sort.Strings(perms)

	ov := vgOverviewSt{
		CardTitle:  i18n.T("vrchat.groups.tab.overview"),
		LoadingMsg: i18n.T("vrchat.groups.loadingGroup"),
		MissingMsg: i18n.T("vrchat.groups.couldNotLoad"),
		KVs:        []vgKVSt{},
		PermBadges: []vgBadgeSt{},
		Roles:      []vgRoleSt{},
	}
	if loading && info == nil {
		ov.Loading = true
		return ov
	}
	if info == nil {
		ov.Missing = true
		return ov
	}
	ov.AboutTitle = i18n.T("vrchat.groups.about")
	ov.Desc = info.Description
	ov.KVs = append(ov.KVs,
		vgKV(i18n.T("vrchat.groups.kv.shortCode"), orDash(info.ShortCode)),
		vgKV(i18n.T("vrchat.groups.tab.members"), i18n.T("vrchat.groups.memberCountOnline", i18n.A{"count": fmt.Sprint(info.MemberCount), "online": fmt.Sprint(info.OnlineCount)})),
		vgKV(i18n.T("vrchat.groups.kv.privacy"), orDash(info.Privacy)),
		vgKV(i18n.T("vrchat.groups.kv.joinState"), orDash(info.JoinState)))
	if owner {
		ov.KVs = append(ov.KVs, vgKV(i18n.T("vrchat.groups.kv.owner"), i18n.T("vrchat.groups.you")))
	} else {
		ov.KVs = append(ov.KVs, vgKV(i18n.T("vrchat.groups.kv.owner"), orDash(info.OwnerID)))
	}
	ov.RulesTitle, ov.Rules = i18n.T("vrchat.groups.groupRules"), info.Rules

	ov.PermsTitle = i18n.T("vrchat.groups.yourPermissions")
	switch {
	case owner:
		ov.PermsMode, ov.PermsMsg = "owner", i18n.T("vrchat.groups.ownerFullPermissions")
	case len(perms) == 0:
		ov.PermsMode, ov.PermsMsg = "none", i18n.T("vrchat.groups.noManagementPerms")
	default:
		ov.PermsMode = "list"
		for _, p := range perms {
			v := "secondary"
			if p == "*" {
				v = "success"
			}
			ov.PermBadges = append(ov.PermBadges, vgBadgeSt{p, v})
		}
	}

	ov.RolesTitle = i18n.T("vrchat.groups.rolesTitle", i18n.A{"count": fmt.Sprint(len(roles))})
	if len(roles) == 0 {
		ov.RolesEmpty = i18n.T("vrchat.groups.noRolesVisible")
	}
	for _, r := range roles {
		rs := vgRoleSt{
			Name:  r.Name,
			Tags:  []vgBadgeSt{},
			Order: i18n.T("vrchat.groups.order", i18n.A{"n": strconv.Itoa(r.Order)}),
			Desc:  r.Description,
			Perms: []string{},
		}
		if r.IsManagementRole {
			rs.Tags = append(rs.Tags, vgBadgeSt{i18n.T("vrchat.groups.managementBadge"), "warning"})
		}
		if r.IsSelfAssignable {
			rs.Tags = append(rs.Tags, vgBadgeSt{i18n.T("vrchat.groups.selfAssignBadge"), "info"})
		}
		if r.RequiresTwoFactor {
			rs.Tags = append(rs.Tags, vgBadgeSt{i18n.T("vrchat.groups.twoFARequired"), "error"})
		}
		if len(r.Permissions) > 0 {
			rs.PermSum = i18n.Tn("vrchat.groups.permissionsCount", len(r.Permissions))
			rs.Perms = append(rs.Perms, r.Permissions...)
		}
		ov.Roles = append(ov.Roles, rs)
	}
	return ov
}

func vgKV(label, value string) vgKVSt {
	return vgKVSt{Label: label, DL: strings.ToLower(label), Value: value}
}

// ── members ──

func (u *UI) vgMembersState() vgMembersSt {
	vgState.mu.Lock()
	pg := vgState.members
	roleName := vgRoleNames(vgState.roles)
	canRoles := vgHasPerm(vgState.owner, vgState.perms, "group-roles-assign")
	canKick := vgHasPerm(vgState.owner, vgState.perms, "group-members-remove")
	canBan := vgHasPerm(vgState.owner, vgState.perms, "group-bans-manage")
	vgState.mu.Unlock()

	ms := vgMembersSt{
		CardTitle: i18n.T("vrchat.groups.membersLoadedTitle", i18n.A{"count": fmt.Sprint(len(pg.rows))}),
		Rows:      []vgMemberRowSt{},
		Pager:     vgPagerState(pg.loaded, pg.loading, pg.end, len(pg.rows), "members", vgMaxMembers),
	}
	switch {
	case !pg.loaded && pg.loading:
		ms.State, ms.Msg = "loading", i18n.T("vrchat.groups.loadingMembers")
	case !pg.loaded:
		ms.State, ms.Msg = "notloaded", i18n.T("vrchat.groups.notLoaded")
	case len(pg.rows) == 0:
		ms.State, ms.Msg = "empty", i18n.T("vrchat.groups.noMembersVisible")
	default:
		ms.State = "rows"
		for i, m := range pg.rows {
			row := vgMemberRowSt{Name: vgMemberName(m), Tags: []vgBadgeSt{}, Acts: []vgBtnSt{}}
			for _, rid := range m.RoleIDs {
				n := roleName[rid]
				if n == "" {
					n = vgShortID(rid)
				}
				row.Tags = append(row.Tags, vgBadgeSt{n, "secondary"})
			}
			if m.IsRepresenting != nil && *m.IsRepresenting {
				row.Tags = append(row.Tags, vgBadgeSt{i18n.T("vrchat.groups.representing"), "success"})
			}
			var meta []string
			if m.JoinedAt != nil {
				meta = append(meta, i18n.T("vrchat.groups.joined", i18n.A{"when": vgWhen(*m.JoinedAt)}))
			}
			if m.MembershipStatus != nil && *m.MembershipStatus != "" && *m.MembershipStatus != "member" {
				meta = append(meta, *m.MembershipStatus)
			}
			row.Meta = strings.Join(meta, " · ")
			if canRoles {
				row.Acts = append(row.Acts, vgBtnSt{i18n.T("vrchat.groups.action.roles"), "ghost", fmt.Sprintf("vrcg-roles:%d", i)})
			}
			if canKick {
				row.Acts = append(row.Acts, vgBtnSt{i18n.T("vrchat.groups.action.kick"), "warn", fmt.Sprintf("vrcg-kick:%d", i)})
			}
			if canBan {
				row.Acts = append(row.Acts, vgBtnSt{i18n.T("vrchat.groups.action.ban"), "destructive", fmt.Sprintf("vrcg-ban:%d", i)})
			}
			ms.Rows = append(ms.Rows, row)
		}
	}
	return ms
}

// ── requests / invites / bans (shared row list) ──

func (u *UI) vgUsersState(kind string) vgUsersSt {
	vgState.mu.Lock()
	var pg vgPage[vgUserRow]
	switch kind {
	case "requests":
		pg = vgState.reqs
	case "invites":
		pg = vgState.invs
	default:
		pg = vgState.bans
	}
	canInv := vgHasPerm(vgState.owner, vgState.perms, "group-invites-manage")
	canBan := vgHasPerm(vgState.owner, vgState.perms, "group-bans-manage")
	vgState.mu.Unlock()

	us := vgUsersSt{
		Head:  []vgBtnSt{},
		Rows:  []vgUserRowSt{},
		Msg:   "Loading…",
		Pager: vgPagerState(pg.loaded, pg.loading, pg.end, len(pg.rows), kind, vgMaxRows),
	}
	rowBtns := func(int) []vgBtnSt { return []vgBtnSt{} }
	switch kind {
	case "requests":
		us.CardTitle, us.Empty = "Join requests", "No pending join requests."
		if canInv {
			rowBtns = func(i int) []vgBtnSt {
				return []vgBtnSt{{"Accept", "go", fmt.Sprintf("vrcg-req-a:%d", i)}, {"Reject", "warn", fmt.Sprintf("vrcg-req-r:%d", i)}}
			}
		}
	case "invites":
		us.CardTitle, us.Empty = "Invites", "No outstanding invites."
		if canInv {
			us.Head = append(us.Head, vgBtnSt{"Invite user…", "primary", "vrcg-invite"})
			rowBtns = func(i int) []vgBtnSt {
				return []vgBtnSt{{"Cancel", "warn", fmt.Sprintf("vrcg-inv-cancel:%d", i)}}
			}
		}
	default:
		us.CardTitle, us.Empty = "Bans", "No banned users."
		if canBan {
			rowBtns = func(i int) []vgBtnSt {
				return []vgBtnSt{{"Unban", "outline", fmt.Sprintf("vrcg-unban:%d", i)}}
			}
		}
	}
	switch {
	case !pg.loaded && pg.loading:
		us.State = "loading"
	case !pg.loaded:
		us.State, us.Msg = "notloaded", "Not loaded - Refresh to retry."
	case len(pg.rows) == 0:
		us.State = "empty"
	default:
		us.State = "rows"
		for i, r := range pg.rows {
			us.Rows = append(us.Rows, vgUserRowSt{
				Name: r.Name,
				Sub:  strings.Join(vgNonEmpty(vgWhen(r.When), r.Sub), " · "),
				Acts: rowBtns(i),
			})
		}
	}
	return us
}

// ── posts + announcement ──

func (u *UI) vgPostsState() vgPostsSt {
	vgState.mu.Lock()
	pg := vgState.posts
	ann := vgState.ann
	canAnn := vgHasPerm(vgState.owner, vgState.perms, "group-announcement-manage")
	vgState.mu.Unlock()

	ps := vgPostsSt{
		AnnTitle: "Current announcement", AnnTipS: tipTopicSt("vrchat-announcement"),
		AnnEmptyMsg: "No announcement set.",
		CanAnn:      canAnn,
		NewAnnTitle: "New announcement", NewPostTitle: "New post",
		FTitle: "Title", FText: "Text", FImage: "Image ID (optional)", FNotify: "Send notification to members",
		AnnSubmit:  "Post announcement",
		AnnHint:    "Replaces the group's current announcement; VRChat enforces its own length limits.",
		PostSubmit: "Create post",
		PostHint:   "Adds to the group's post feed (doesn't replace the announcement).",
		CardTitle:  fmt.Sprintf("Posts - %d loaded", len(pg.rows)),
		Msg:        "Loading posts…",
		Empty:      "No posts yet.",
		Rows:       []vgPostRowSt{},
		Pager:      vgPagerState(pg.loaded, pg.loading, pg.end, len(pg.rows), "posts", vgMaxPosts),
	}
	if ann != nil {
		ps.HasAnn, ps.AnnHead, ps.AnnWhen, ps.AnnText = true, ann.Title, vgWhen(ann.CreatedAt), ann.Text
	} else if pg.loaded {
		ps.AnnEmpty = true
	}
	switch {
	case !pg.loaded && pg.loading:
		ps.State = "loading"
	case !pg.loaded:
		ps.State, ps.Msg = "notloaded", "Not loaded - Refresh to retry."
	case len(pg.rows) == 0:
		ps.State = "empty"
	default:
		ps.State = "rows"
		for i, p := range pg.rows {
			row := vgPostRowSt{
				Title: p.Title,
				Meta:  strings.Join(vgNonEmpty(vgWhen(p.CreatedAt), p.Visibility, p.AuthorID), " · "),
				Text:  p.Text,
				Del:   []vgBtnSt{},
			}
			if canAnn && p.ID != "" {
				row.Del = append(row.Del, vgBtnSt{"Delete", "destructive", fmt.Sprintf("vrcg-post-del:%d", i)})
			}
			ps.Rows = append(ps.Rows, row)
		}
	}
	return ps
}

// ── audit log ──

func (u *UI) vgAuditState() vgAuditSt {
	vgState.mu.Lock()
	pg := vgState.audit
	canAudit := vgHasPerm(vgState.owner, vgState.perms, "group-audit-view")
	vgState.mu.Unlock()

	as := vgAuditSt{
		CardTitle:  fmt.Sprintf("Audit log - %d loaded", len(pg.rows)),
		NoPerm:     !canAudit,
		NoPermMsg:  "Viewing usually requires the group-audit-view permission.",
		Msg:        "Loading audit log…",
		Empty:      "No audit entries.",
		RawSummary: "raw entry",
		Rows:       []vgAuditRowSt{},
		Pager:      vgPagerState(pg.loaded, pg.loading, pg.end, len(pg.rows), "audit", vgMaxAudit),
	}
	switch {
	case !pg.loaded && pg.loading:
		as.State = "loading"
	case !pg.loaded:
		as.State, as.Msg = "notloaded", "Not loaded - Refresh to retry."
	case len(pg.rows) == 0:
		as.State = "empty"
	default:
		as.State = "rows"
		for _, a := range pg.rows {
			as.Rows = append(as.Rows, vgAuditRowSt{
				When: vgWhen(a.When), Event: orDash(a.Event), Actor: a.Actor, Desc: a.Desc, Raw: a.Raw,
			})
		}
	}
	return as
}

// ── pure renderers (golden reference; byte-identical to native/zigui/src/vrcgroups.zig) ──

func vrcgBodyHTML(st vrcgState) string {
	if !st.Available {
		return emptyState(st.Unavailable)
	}
	if !st.SignedIn {
		return card(st.SignInTitle, "", hint("info", st.SignInHint))
	}
	if st.Mode == "picker" {
		return vgPickerHTML(st.Picker)
	}
	return vgWorkspaceHTML(st.WS)
}

func vgPickerHTML(p vgPickerSt) string {
	var list strings.Builder
	switch p.State {
	case "loading":
		list.WriteString(hint("info", p.Msg))
	case "none", "nomatch":
		list.WriteString(emptyState(p.Msg))
	default:
		list.WriteString(`<div class=vrc-glist>`)
		for _, g := range p.Rows {
			fmt.Fprintf(&list, `<button class=vrc-glist-item data-act="vrcg-open:%d"><span>%s</span><span class=vrc-gcount>%s</span></button>`,
				g.Idx, html.EscapeString(g.Name), html.EscapeString(g.Meta))
		}
		list.WriteString(`</div>`)
	}
	body := `<form data-act=vrcg-filter><input class=field-input name=q value="` + html.EscapeString(p.Filter) +
		`" placeholder="Filter my groups… (Enter)"></form>` + list.String()
	return card(p.Title, btn(p.Refresh, "ghost", "vrcg-refresh-groups", ""), body)
}

func vgWorkspaceHTML(ws vgWorkspaceSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card vrcg-head">`)
	b.WriteString(`<div class=vrcg-head-top><div class=vrcg-title>` + html.EscapeString(ws.Title) + `</div>` +
		btnRow(btn(ws.Refresh, "ghost", "vrcg-reload", ""), btn(ws.Back, "outline", "vrcg-back", "")) + `</div>`)
	if len(ws.Badges) > 0 {
		var bd strings.Builder
		for _, x := range ws.Badges {
			bd.WriteString(badge(x.Text, x.Variant))
		}
		b.WriteString(`<div class=vrcg-badges>` + bd.String() + `</div>`)
	}
	items := make([][2]string, 0, len(ws.Tabs))
	for _, t := range ws.Tabs {
		items = append(items, [2]string{t.Val, t.Label})
	}
	b.WriteString(subTabs("vrcg-view:", ws.View, items...))
	b.WriteString(`</div>`)

	switch ws.View {
	case "members":
		b.WriteString(vgMembersHTML(ws.Members))
	case "requests", "invites", "bans":
		b.WriteString(vgUsersHTML(ws.Users))
	case "posts":
		b.WriteString(vgPostsHTML(ws.Posts))
	case "audit":
		b.WriteString(vgAuditHTML(ws.Audit))
	default:
		b.WriteString(vgOverviewHTML(ws.Overview))
	}
	return b.String()
}

func vgOverviewHTML(ov vgOverviewSt) string {
	if ov.Loading {
		return card(ov.CardTitle, "", hint("info", ov.LoadingMsg))
	}
	if ov.Missing {
		return card(ov.CardTitle, "", hint("warn", ov.MissingMsg))
	}
	var b strings.Builder

	var about strings.Builder
	if ov.Desc != "" {
		about.WriteString(`<div class=vrcg-desc>` + html.EscapeString(ov.Desc) + `</div>`)
	}
	for _, r := range ov.KVs {
		about.WriteString(kvDL(r.Label, r.DL, r.Value))
	}
	if ov.Rules != "" {
		about.WriteString(`<details class=vrcg-det><summary>` + html.EscapeString(ov.RulesTitle) + `</summary><div class=vrcg-desc>` +
			html.EscapeString(ov.Rules) + `</div></details>`)
	}
	b.WriteString(card(ov.AboutTitle, "", about.String()))

	var pb strings.Builder
	switch ov.PermsMode {
	case "owner":
		pb.WriteString(badge(ov.PermsMsg, "success"))
	case "none":
		pb.WriteString(hint("info", ov.PermsMsg))
	default:
		var pv strings.Builder
		for _, x := range ov.PermBadges {
			pv.WriteString(badge(x.Text, x.Variant))
		}
		pb.WriteString(`<div class=vrcg-badges>` + pv.String() + `</div>`)
	}
	b.WriteString(card(ov.PermsTitle, "", pb.String()))

	var rb strings.Builder
	if len(ov.Roles) == 0 {
		rb.WriteString(emptyState(ov.RolesEmpty))
	}
	for _, r := range ov.Roles {
		var tags strings.Builder
		for _, t := range r.Tags {
			tags.WriteString(badge(t.Text, t.Variant))
		}
		head := `<div class=vrcg-mname><b>` + html.EscapeString(r.Name) + `</b>` + tags.String() +
			`<span class=vrcg-count>` + html.EscapeString(r.Order) + `</span></div>`
		sub := ""
		if r.Desc != "" {
			sub = `<div class=vrcg-mmeta>` + html.EscapeString(r.Desc) + `</div>`
		}
		det := ""
		if r.PermSum != "" {
			var pv strings.Builder
			for _, p := range r.Perms {
				pv.WriteString(badge(p, "secondary"))
			}
			det = `<details class=vrcg-det><summary>` + html.EscapeString(r.PermSum) +
				`</summary><div class=vrcg-badges>` + pv.String() + `</div></details>`
		}
		rb.WriteString(`<div class=vrcg-rolerow>` + head + sub + det + `</div>`)
	}
	b.WriteString(card(ov.RolesTitle, "", rb.String()))
	return b.String()
}

func vgMembersHTML(ms vgMembersSt) string {
	var b strings.Builder
	switch ms.State {
	case "loading":
		b.WriteString(hint("info", ms.Msg))
	case "notloaded":
		b.WriteString(hint("warn", ms.Msg))
	case "empty":
		b.WriteString(emptyState(ms.Msg))
	default:
		for _, m := range ms.Rows {
			var tags, acts strings.Builder
			for _, t := range m.Tags {
				tags.WriteString(badge(t.Text, t.Variant))
			}
			for _, a := range m.Acts {
				acts.WriteString(btn(a.Label, a.Variant, a.Act, ""))
			}
			fmt.Fprintf(&b, `<div class=vrcg-mrow><div class=vrcg-mmain><div class=vrcg-mname>%s%s</div><div class=vrcg-mmeta>%s</div></div><div class=vrcg-macts>%s</div></div>`,
				html.EscapeString(m.Name), tags.String(), html.EscapeString(m.Meta), acts.String())
		}
	}
	b.WriteString(vgPagerHTML(ms.Pager))
	return card(ms.CardTitle, "", b.String())
}

func vgUsersHTML(us vgUsersSt) string {
	var b strings.Builder
	if len(us.Head) > 0 {
		hb := make([]string, 0, len(us.Head))
		for _, x := range us.Head {
			hb = append(hb, btn(x.Label, x.Variant, x.Act, ""))
		}
		b.WriteString(btnRow(hb...))
	}
	switch us.State {
	case "loading":
		b.WriteString(hint("info", us.Msg))
	case "notloaded":
		b.WriteString(hint("warn", us.Msg))
	case "empty":
		b.WriteString(emptyState(us.Empty))
	default:
		for _, r := range us.Rows {
			acts := make([]string, 0, len(r.Acts))
			for _, a := range r.Acts {
				acts = append(acts, btn(a.Label, a.Variant, a.Act, ""))
			}
			b.WriteString(itemRow(r.Name, r.Sub, acts...))
		}
	}
	b.WriteString(vgPagerHTML(us.Pager))
	return card(us.CardTitle, "", b.String())
}

func vgPostsHTML(ps vgPostsSt) string {
	var b strings.Builder

	if ps.HasAnn {
		cur := `<div class=vrcg-post><div class=vrcg-mname><b>` + html.EscapeString(ps.AnnHead) + `</b></div>` +
			`<div class=vrcg-mmeta>` + html.EscapeString(ps.AnnWhen) + `</div>` +
			`<div class=vrcg-post-text>` + html.EscapeString(ps.AnnText) + `</div></div>`
		b.WriteString(card(ps.AnnTitle, tipOr(ps.AnnTipS, ps.AnnTip), cur))
	} else if ps.AnnEmpty {
		b.WriteString(card(ps.AnnTitle, tipOr(ps.AnnTipS, ps.AnnTip), emptyState(ps.AnnEmptyMsg)))
	}

	if ps.CanAnn {
		form := `<form data-act=vrcg-ann>` +
			`<label class=field data-label=ann-title><span class=field-label>` + html.EscapeString(ps.FTitle) + `</span><input class=field-input name=title maxlength=100></label>` +
			`<label class=field data-label=ann-text><span class=field-label>` + html.EscapeString(ps.FText) + `</span><textarea class=field-input name=text rows=3 maxlength=5000></textarea></label>` +
			`<label class=field data-label=ann-imageid><span class=field-label>` + html.EscapeString(ps.FImage) + `</span><input class=field-input name=imageid placeholder="file_…"></label>` +
			`<label class=row><span class=row-label>` + html.EscapeString(ps.FNotify) + `</span>` +
			`<span class=switch><input type=checkbox name=notify value=1><span class=switch-track></span></span></label>` +
			`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(ps.AnnSubmit) + `</button></form>` +
			hint("info", ps.AnnHint)
		b.WriteString(card(ps.NewAnnTitle, "", form))

		postForm := `<form data-act=vrcg-post>` +
			`<label class=field data-label=post-title><span class=field-label>` + html.EscapeString(ps.FTitle) + `</span><input class=field-input name=title maxlength=100></label>` +
			`<label class=field data-label=post-text><span class=field-label>` + html.EscapeString(ps.FText) + `</span><textarea class=field-input name=text rows=3 maxlength=5000></textarea></label>` +
			`<label class=row><span class=row-label>` + html.EscapeString(ps.FNotify) + `</span>` +
			`<span class=switch><input type=checkbox name=notify value=1><span class=switch-track></span></span></label>` +
			`<button class="rp-btn rp-btn--go" type=submit>` + html.EscapeString(ps.PostSubmit) + `</button></form>` +
			hint("info", ps.PostHint)
		b.WriteString(card(ps.NewPostTitle, "", postForm))
	}

	var pb strings.Builder
	switch ps.State {
	case "loading":
		pb.WriteString(hint("info", ps.Msg))
	case "notloaded":
		pb.WriteString(hint("warn", ps.Msg))
	case "empty":
		pb.WriteString(emptyState(ps.Empty))
	default:
		for _, p := range ps.Rows {
			del := ""
			if len(p.Del) > 0 {
				var db strings.Builder
				for _, d := range p.Del {
					db.WriteString(btn(d.Label, d.Variant, d.Act, ""))
				}
				del = `<div class=vrcg-post-actions>` + db.String() + `</div>`
			}
			pb.WriteString(`<div class=vrcg-post><div class=vrcg-mname><b>` + html.EscapeString(p.Title) + `</b></div>` +
				`<div class=vrcg-mmeta>` + html.EscapeString(p.Meta) + `</div>` +
				`<div class=vrcg-post-text>` + html.EscapeString(p.Text) + `</div>` + del + `</div>`)
		}
	}
	pb.WriteString(vgPagerHTML(ps.Pager))
	b.WriteString(card(ps.CardTitle, "", pb.String()))
	return b.String()
}

func vgAuditHTML(as vgAuditSt) string {
	var b strings.Builder
	if as.NoPerm {
		b.WriteString(hint("info", as.NoPermMsg))
	}
	switch as.State {
	case "loading":
		b.WriteString(hint("info", as.Msg))
	case "notloaded":
		b.WriteString(hint("warn", as.Msg))
	case "empty":
		b.WriteString(emptyState(as.Empty))
	default:
		for _, a := range as.Rows {
			head := `<span class=vrcg-atime>` + html.EscapeString(a.When) + `</span>` +
				badge(a.Event, "info") + `<span>` + html.EscapeString(a.Actor) + `</span>`
			desc := ""
			if a.Desc != "" {
				desc = `<div class=vrcg-mmeta>` + html.EscapeString(a.Desc) + `</div>`
			}
			det := `<details class=vrcg-det><summary>` + html.EscapeString(as.RawSummary) + `</summary><pre class=vrcg-json>` +
				html.EscapeString(a.Raw) + `</pre></details>`
			b.WriteString(`<div class=vrcg-arow><div class=vrcg-mname>` + head + `</div>` + desc + det + `</div>`)
		}
	}
	b.WriteString(vgPagerHTML(as.Pager))
	return card(as.CardTitle, "", b.String())
}

// ── small render helpers ──

// vgPagerState resolves the Load-more / cap footer for a paged list.
func vgPagerState(loaded, loading, end bool, n int, section string, max int) vgPagerSt {
	switch {
	case loading && loaded:
		return vgPagerSt{Mode: "loading", Msg: "Loading…"}
	case loaded && !end && n > 0:
		return vgPagerSt{Mode: "more", Label: "Load more", Act: "vrcg-more:" + section}
	case loaded && n >= max:
		return vgPagerSt{Mode: "cap", Msg: fmt.Sprintf("Showing first %d.", max)}
	}
	return vgPagerSt{}
}

// vgPagerHTML renders a resolved pager footer.
func vgPagerHTML(p vgPagerSt) string {
	switch p.Mode {
	case "loading":
		return `<div class=btn-row>` + hint("info", p.Msg) + `</div>`
	case "more":
		return btnRow(btn(p.Label, "outline", p.Act, ""))
	case "cap":
		return `<div class=btn-row>` + hint("warn", p.Msg) + `</div>`
	}
	return ""
}

func vgHasPerm(owner bool, perms map[string]bool, p string) bool {
	return owner || perms["*"] || perms[p]
}

func vgRoleNames(roles []vrchat.GroupRole) map[string]string {
	m := make(map[string]string, len(roles))
	for _, r := range roles {
		m[r.ID] = r.Name
	}
	return m
}

func vgMemberName(m vrchat.GroupMemberFull) string {
	if m.DisplayName != nil && *m.DisplayName != "" {
		return *m.DisplayName
	}
	if m.UserID != nil && *m.UserID != "" {
		return *m.UserID
	}
	return m.ID
}

func vgShortID(id string) string {
	if len(id) > 13 {
		return id[:13] + "…"
	}
	return id
}

// vgWhen renders a VRChat RFC3339 timestamp as local "2006-01-02 15:04".
func vgWhen(s string) string {
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	if len(s) >= 16 {
		return s[:16]
	}
	return s
}

// vgNonEmpty filters out blank strings.
func vgNonEmpty(parts ...string) []string {
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}
