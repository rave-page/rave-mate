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
)

// VRChat ▸ Groups sub-tab renderer (state + handlers in vrchat_groups_actions.go). Renders from
// vgState snapshots; picker/friend row order is written back into vgState (shown/shownFriends) so
// index-based actions stay pinned to what the user sees. UX reimplements the web app's group-
// management panels (roles/members/moderation) for the native workspace.

// vrcgBody is the Groups sub-tab root (patched whole via vgPatch).
func (u *UI) vrcgBody() string {
	if u.svc.Vrchat == nil {
		return emptyState(i18n.T("vrchat.unavailable"))
	}
	if !u.svc.Vrchat.State().LoggedIn {
		return card(i18n.T("vrchat.subtab.groups"), "", hint("info", i18n.T("vrchat.groups.hint.signInToManage")))
	}
	u.vgLoadGroups(false) // lazy first load (single-flight)
	vgState.mu.Lock()
	sel := vgState.selID
	vgState.mu.Unlock()
	if sel == "" {
		return u.vgPickerHTML()
	}
	return u.vgWorkspaceHTML()
}

// ── group picker ──

func (u *UI) vgPickerHTML() string {
	vgState.mu.Lock()
	loading := vgState.groupsLoading || !vgState.groupsLoaded
	q := strings.ToLower(strings.TrimSpace(vgState.filter))
	shown := vgState.shown[:0]
	for _, g := range vgState.groups {
		if q == "" || strings.Contains(strings.ToLower(g.Name), q) || strings.Contains(strings.ToLower(g.ShortCode), q) {
			shown = append(shown, g)
		}
	}
	vgState.shown = shown
	filter := vgState.filter
	nAll := len(vgState.groups)

	var list strings.Builder
	switch {
	case loading:
		list.WriteString(hint("info", i18n.T("vrchat.groups.loadingGroups")))
	case nAll == 0:
		list.WriteString(emptyState(i18n.T("vrchat.groups.noneFound")))
	case len(shown) == 0:
		list.WriteString(emptyState(i18n.T("vrchat.groups.noMatch")))
	default:
		list.WriteString(`<div class=vrc-glist>`)
		for i, g := range shown {
			fmt.Fprintf(&list, `<button class=vrc-glist-item data-act="vrcg-open:%d"><span>%s</span><span class=vrc-gcount>%s · %s</span></button>`,
				i, html.EscapeString(g.Name), html.EscapeString(g.ShortCode), html.EscapeString(i18n.Tn("vrchat.groups.members", g.MemberCount)))
		}
		list.WriteString(`</div>`)
	}
	vgState.mu.Unlock()

	body := `<form data-act=vrcg-filter><input class=field-input name=q value="` + html.EscapeString(filter) +
		`" placeholder="Filter my groups… (Enter)"></form>` + list.String()
	return card(i18n.T("vrchat.groups.myGroupsTitle"), btn(i18n.T("common.refresh"), "ghost", "vrcg-refresh-groups", ""), body)
}

// ── workspace (header + section) ──

func (u *UI) vgWorkspaceHTML() string {
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

	var b strings.Builder
	b.WriteString(`<div class="rp-card vrcg-head">`)
	b.WriteString(`<div class=vrcg-head-top><div class=vrcg-title>` + html.EscapeString(title) + `</div>` +
		btnRow(btn(i18n.T("common.refresh"), "ghost", "vrcg-reload", ""), btn(i18n.T("vrchat.groups.backToMyGroups"), "outline", "vrcg-back", "")) + `</div>`)
	if info != nil {
		var bd []string
		if info.ShortCode != "" {
			bd = append(bd, badge(info.ShortCode, "outline"))
		}
		bd = append(bd, badge(i18n.Tn("vrchat.groups.members", info.MemberCount), "secondary"))
		if info.OnlineCount > 0 {
			bd = append(bd, badge(i18n.T("vrchat.groups.onlineCount", i18n.A{"count": fmt.Sprint(info.OnlineCount)}), "success"))
		}
		if info.Privacy != "" {
			bd = append(bd, badge(info.Privacy, "info"))
		}
		if info.JoinState != "" {
			bd = append(bd, badge(i18n.T("vrchat.groups.joinState", i18n.A{"state": info.JoinState}), "info"))
		}
		if info.IsVerified {
			bd = append(bd, badge(i18n.T("vrchat.groups.verified"), "success"))
		}
		if owner {
			bd = append(bd, badge(i18n.T("vrchat.groups.youOwnThisGroup"), "success"))
		}
		b.WriteString(`<div class=vrcg-badges>` + strings.Join(bd, "") + `</div>`)
	}
	b.WriteString(subTabs("vrcg-view:", view,
		[2]string{"overview", i18n.T("vrchat.groups.tab.overview")}, [2]string{"members", i18n.T("vrchat.groups.tab.members")}, [2]string{"requests", i18n.T("vrchat.groups.tab.requests")},
		[2]string{"invites", i18n.T("vrchat.groups.tab.invites")}, [2]string{"bans", i18n.T("vrchat.groups.tab.bans")}, [2]string{"posts", i18n.T("vrchat.groups.tab.posts")}, [2]string{"audit", i18n.T("vrchat.groups.tab.auditLog")}))
	b.WriteString(`</div>`)

	switch view {
	case "members":
		b.WriteString(u.vgMembersHTML())
	case "requests", "invites", "bans":
		b.WriteString(u.vgUsersSectionHTML(view))
	case "posts":
		b.WriteString(u.vgPostsHTML())
	case "audit":
		b.WriteString(u.vgAuditHTML())
	default:
		b.WriteString(u.vgOverviewHTML())
	}
	return b.String()
}

// ── overview: about + my permissions + enriched roles ──

func (u *UI) vgOverviewHTML() string {
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

	if loading && info == nil {
		return card(i18n.T("vrchat.groups.tab.overview"), "", hint("info", i18n.T("vrchat.groups.loadingGroup")))
	}
	if info == nil {
		return card(i18n.T("vrchat.groups.tab.overview"), "", hint("warn", i18n.T("vrchat.groups.couldNotLoad")))
	}

	var b strings.Builder

	var about strings.Builder
	if info.Description != "" {
		about.WriteString(`<div class=vrcg-desc>` + html.EscapeString(info.Description) + `</div>`)
	}
	about.WriteString(kv(i18n.T("vrchat.groups.kv.shortCode"), orDash(info.ShortCode)))
	about.WriteString(kv(i18n.T("vrchat.groups.tab.members"), i18n.T("vrchat.groups.memberCountOnline", i18n.A{"count": fmt.Sprint(info.MemberCount), "online": fmt.Sprint(info.OnlineCount)})))
	about.WriteString(kv(i18n.T("vrchat.groups.kv.privacy"), orDash(info.Privacy)))
	about.WriteString(kv(i18n.T("vrchat.groups.kv.joinState"), orDash(info.JoinState)))
	if owner {
		about.WriteString(kv(i18n.T("vrchat.groups.kv.owner"), i18n.T("vrchat.groups.you")))
	} else {
		about.WriteString(kv(i18n.T("vrchat.groups.kv.owner"), orDash(info.OwnerID)))
	}
	if info.Rules != "" {
		about.WriteString(`<details class=vrcg-det><summary>` + html.EscapeString(i18n.T("vrchat.groups.groupRules")) + `</summary><div class=vrcg-desc>` +
			html.EscapeString(info.Rules) + `</div></details>`)
	}
	b.WriteString(card(i18n.T("vrchat.groups.about"), "", about.String()))

	var pb strings.Builder
	switch {
	case owner:
		pb.WriteString(badge(i18n.T("vrchat.groups.ownerFullPermissions"), "success"))
	case len(perms) == 0:
		pb.WriteString(hint("info", i18n.T("vrchat.groups.noManagementPerms")))
	default:
		var pv []string
		for _, p := range perms {
			v := "secondary"
			if p == "*" {
				v = "success"
			}
			pv = append(pv, badge(p, v))
		}
		pb.WriteString(`<div class=vrcg-badges>` + strings.Join(pv, "") + `</div>`)
	}
	b.WriteString(card(i18n.T("vrchat.groups.yourPermissions"), "", pb.String()))

	var rb strings.Builder
	if len(roles) == 0 {
		rb.WriteString(emptyState(i18n.T("vrchat.groups.noRolesVisible")))
	}
	for _, r := range roles {
		var tags []string
		if r.IsManagementRole {
			tags = append(tags, badge(i18n.T("vrchat.groups.managementBadge"), "warning"))
		}
		if r.IsSelfAssignable {
			tags = append(tags, badge(i18n.T("vrchat.groups.selfAssignBadge"), "info"))
		}
		if r.RequiresTwoFactor {
			tags = append(tags, badge(i18n.T("vrchat.groups.twoFARequired"), "error"))
		}
		head := `<div class=vrcg-mname><b>` + html.EscapeString(r.Name) + `</b>` + strings.Join(tags, "") +
			`<span class=vrcg-count>` + html.EscapeString(i18n.T("vrchat.groups.order", i18n.A{"n": strconv.Itoa(r.Order)})) + `</span></div>`
		sub := ""
		if r.Description != "" {
			sub = `<div class=vrcg-mmeta>` + html.EscapeString(r.Description) + `</div>`
		}
		det := ""
		if len(r.Permissions) > 0 {
			var pv strings.Builder
			for _, p := range r.Permissions {
				pv.WriteString(badge(p, "secondary"))
			}
			det = `<details class=vrcg-det><summary>` + html.EscapeString(i18n.Tn("vrchat.groups.permissionsCount", len(r.Permissions))) +
				`</summary><div class=vrcg-badges>` + pv.String() + `</div></details>`
		}
		rb.WriteString(`<div class=vrcg-rolerow>` + head + sub + det + `</div>`)
	}
	b.WriteString(card(i18n.T("vrchat.groups.rolesTitle", i18n.A{"count": fmt.Sprint(len(roles))}), "", rb.String()))
	return b.String()
}

// ── members ──

func (u *UI) vgMembersHTML() string {
	vgState.mu.Lock()
	pg := vgState.members
	roleName := vgRoleNames(vgState.roles)
	canRoles := vgHasPerm(vgState.owner, vgState.perms, "group-roles-assign")
	canKick := vgHasPerm(vgState.owner, vgState.perms, "group-members-remove")
	canBan := vgHasPerm(vgState.owner, vgState.perms, "group-bans-manage")
	vgState.mu.Unlock()

	var b strings.Builder
	switch {
	case !pg.loaded && pg.loading:
		b.WriteString(hint("info", i18n.T("vrchat.groups.loadingMembers")))
	case !pg.loaded:
		b.WriteString(hint("warn", i18n.T("vrchat.groups.notLoaded")))
	case len(pg.rows) == 0:
		b.WriteString(emptyState(i18n.T("vrchat.groups.noMembersVisible")))
	default:
		for i, m := range pg.rows {
			var tags []string
			for _, rid := range m.RoleIDs {
				n := roleName[rid]
				if n == "" {
					n = vgShortID(rid)
				}
				tags = append(tags, badge(n, "secondary"))
			}
			if m.IsRepresenting != nil && *m.IsRepresenting {
				tags = append(tags, badge(i18n.T("vrchat.groups.representing"), "success"))
			}
			var meta []string
			if m.JoinedAt != nil {
				meta = append(meta, i18n.T("vrchat.groups.joined", i18n.A{"when": vgWhen(*m.JoinedAt)}))
			}
			if m.MembershipStatus != nil && *m.MembershipStatus != "" && *m.MembershipStatus != "member" {
				meta = append(meta, *m.MembershipStatus)
			}
			var acts []string
			if canRoles {
				acts = append(acts, btn(i18n.T("vrchat.groups.action.roles"), "ghost", fmt.Sprintf("vrcg-roles:%d", i), ""))
			}
			if canKick {
				acts = append(acts, btn(i18n.T("vrchat.groups.action.kick"), "warn", fmt.Sprintf("vrcg-kick:%d", i), ""))
			}
			if canBan {
				acts = append(acts, btn(i18n.T("vrchat.groups.action.ban"), "destructive", fmt.Sprintf("vrcg-ban:%d", i), ""))
			}
			fmt.Fprintf(&b, `<div class=vrcg-mrow><div class=vrcg-mmain><div class=vrcg-mname>%s%s</div><div class=vrcg-mmeta>%s</div></div><div class=vrcg-macts>%s</div></div>`,
				html.EscapeString(vgMemberName(m)), strings.Join(tags, ""),
				html.EscapeString(strings.Join(meta, " · ")), strings.Join(acts, ""))
		}
	}
	b.WriteString(vgPager(pg.loaded, pg.loading, pg.end, len(pg.rows), "members", vgMaxMembers))
	return card(i18n.T("vrchat.groups.membersLoadedTitle", i18n.A{"count": fmt.Sprint(len(pg.rows))}), "", b.String())
}

// ── requests / invites / bans (shared row list) ──

func (u *UI) vgUsersSectionHTML(kind string) string {
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

	title, empty := "", ""
	head := ""
	rowBtns := func(int) []string { return nil }
	switch kind {
	case "requests":
		title, empty = "Join requests", "No pending join requests."
		if canInv {
			rowBtns = func(i int) []string {
				return []string{btn("Accept", "go", fmt.Sprintf("vrcg-req-a:%d", i), ""),
					btn("Reject", "warn", fmt.Sprintf("vrcg-req-r:%d", i), "")}
			}
		}
	case "invites":
		title, empty = "Invites", "No outstanding invites."
		if canInv {
			head = btnRow(btn("Invite user…", "primary", "vrcg-invite", ""))
			rowBtns = func(i int) []string {
				return []string{btn("Cancel", "warn", fmt.Sprintf("vrcg-inv-cancel:%d", i), "")}
			}
		}
	default:
		title, empty = "Bans", "No banned users."
		if canBan {
			rowBtns = func(i int) []string {
				return []string{btn("Unban", "outline", fmt.Sprintf("vrcg-unban:%d", i), "")}
			}
		}
	}

	var b strings.Builder
	b.WriteString(head)
	switch {
	case !pg.loaded && pg.loading:
		b.WriteString(hint("info", "Loading…"))
	case !pg.loaded:
		b.WriteString(hint("warn", "Not loaded - Refresh to retry."))
	case len(pg.rows) == 0:
		b.WriteString(emptyState(empty))
	default:
		for i, r := range pg.rows {
			sub := strings.Join(vgNonEmpty(vgWhen(r.When), r.Sub), " · ")
			b.WriteString(itemRow(r.Name, sub, rowBtns(i)...))
		}
	}
	b.WriteString(vgPager(pg.loaded, pg.loading, pg.end, len(pg.rows), kind, vgMaxRows))
	return card(title, "", b.String())
}

// ── posts + announcement ──

func (u *UI) vgPostsHTML() string {
	vgState.mu.Lock()
	pg := vgState.posts
	ann := vgState.ann
	canAnn := vgHasPerm(vgState.owner, vgState.perms, "group-announcement-manage")
	vgState.mu.Unlock()

	var b strings.Builder

	// Current pinned announcement (fetched with the first posts page).
	if ann != nil {
		cur := `<div class=vrcg-post><div class=vrcg-mname><b>` + html.EscapeString(ann.Title) + `</b></div>` +
			`<div class=vrcg-mmeta>` + html.EscapeString(vgWhen(ann.CreatedAt)) + `</div>` +
			`<div class=vrcg-post-text>` + html.EscapeString(ann.Text) + `</div></div>`
		b.WriteString(card("Current announcement", tipTopic("vrchat-announcement"), cur))
	} else if pg.loaded {
		b.WriteString(card("Current announcement", tipTopic("vrchat-announcement"), emptyState("No announcement set.")))
	}

	if canAnn {
		form := `<form data-act=vrcg-ann>` +
			`<label class=field data-label=ann-title><span class=field-label>Title</span><input class=field-input name=title maxlength=100></label>` +
			`<label class=field data-label=ann-text><span class=field-label>Text</span><textarea class=field-input name=text rows=3 maxlength=5000></textarea></label>` +
			`<label class=field data-label=ann-imageid><span class=field-label>Image ID (optional)</span><input class=field-input name=imageid placeholder="file_…"></label>` +
			`<label class=row><span class=row-label>Send notification to members</span>` +
			`<span class=switch><input type=checkbox name=notify value=1><span class=switch-track></span></span></label>` +
			`<button class="rp-btn rp-btn--go" type=submit>Post announcement</button></form>` +
			hint("info", "Replaces the group's current announcement; VRChat enforces its own length limits.")
		b.WriteString(card("New announcement", "", form))

		postForm := `<form data-act=vrcg-post>` +
			`<label class=field data-label=post-title><span class=field-label>Title</span><input class=field-input name=title maxlength=100></label>` +
			`<label class=field data-label=post-text><span class=field-label>Text</span><textarea class=field-input name=text rows=3 maxlength=5000></textarea></label>` +
			`<label class=row><span class=row-label>Send notification to members</span>` +
			`<span class=switch><input type=checkbox name=notify value=1><span class=switch-track></span></span></label>` +
			`<button class="rp-btn rp-btn--go" type=submit>Create post</button></form>` +
			hint("info", "Adds to the group's post feed (doesn't replace the announcement).")
		b.WriteString(card("New post", "", postForm))
	}

	var pb strings.Builder
	switch {
	case !pg.loaded && pg.loading:
		pb.WriteString(hint("info", "Loading posts…"))
	case !pg.loaded:
		pb.WriteString(hint("warn", "Not loaded - Refresh to retry."))
	case len(pg.rows) == 0:
		pb.WriteString(emptyState("No posts yet."))
	default:
		for i, p := range pg.rows {
			meta := strings.Join(vgNonEmpty(vgWhen(p.CreatedAt), p.Visibility, p.AuthorID), " · ")
			del := ""
			if canAnn && p.ID != "" {
				del = `<div class=vrcg-post-actions>` + btn("Delete", "destructive", fmt.Sprintf("vrcg-post-del:%d", i), "") + `</div>`
			}
			pb.WriteString(`<div class=vrcg-post><div class=vrcg-mname><b>` + html.EscapeString(p.Title) + `</b></div>` +
				`<div class=vrcg-mmeta>` + html.EscapeString(meta) + `</div>` +
				`<div class=vrcg-post-text>` + html.EscapeString(p.Text) + `</div>` + del + `</div>`)
		}
	}
	pb.WriteString(vgPager(pg.loaded, pg.loading, pg.end, len(pg.rows), "posts", vgMaxPosts))
	b.WriteString(card(fmt.Sprintf("Posts - %d loaded", len(pg.rows)), "", pb.String()))
	return b.String()
}

// ── audit log ──

func (u *UI) vgAuditHTML() string {
	vgState.mu.Lock()
	pg := vgState.audit
	canAudit := vgHasPerm(vgState.owner, vgState.perms, "group-audit-view")
	vgState.mu.Unlock()

	var b strings.Builder
	if !canAudit {
		b.WriteString(hint("info", "Viewing usually requires the group-audit-view permission."))
	}
	switch {
	case !pg.loaded && pg.loading:
		b.WriteString(hint("info", "Loading audit log…"))
	case !pg.loaded:
		b.WriteString(hint("warn", "Not loaded - Refresh to retry."))
	case len(pg.rows) == 0:
		b.WriteString(emptyState("No audit entries."))
	default:
		for _, a := range pg.rows {
			head := `<span class=vrcg-atime>` + html.EscapeString(vgWhen(a.When)) + `</span>` +
				badge(orDash(a.Event), "info") + `<span>` + html.EscapeString(a.Actor) + `</span>`
			desc := ""
			if a.Desc != "" {
				desc = `<div class=vrcg-mmeta>` + html.EscapeString(a.Desc) + `</div>`
			}
			det := `<details class=vrcg-det><summary>raw entry</summary><pre class=vrcg-json>` +
				html.EscapeString(a.Raw) + `</pre></details>`
			b.WriteString(`<div class=vrcg-arow><div class=vrcg-mname>` + head + `</div>` + desc + det + `</div>`)
		}
	}
	b.WriteString(vgPager(pg.loaded, pg.loading, pg.end, len(pg.rows), "audit", vgMaxAudit))
	return card(fmt.Sprintf("Audit log - %d loaded", len(pg.rows)), "", b.String())
}

// ── modal bodies (re-patched in place) ──

// vgRoleBodyHTML renders the pending member's add/remove-role list (modal #vrcg-role-body).
func (u *UI) vgRoleBodyHTML() string {
	vgState.mu.Lock()
	p := vgState.pend
	roles := vgState.roles
	var has map[string]bool
	ok := p.idx >= 0 && p.idx < len(vgState.members.rows)
	if ok {
		m := vgState.members.rows[p.idx]
		ok = m.UserID != nil && *m.UserID == p.userID
		if ok {
			has = make(map[string]bool, len(m.RoleIDs))
			for _, rid := range m.RoleIDs {
				has[rid] = true
			}
		}
	}
	vgState.mu.Unlock()
	if !ok {
		return hint("warn", "Member list changed - close and reopen.")
	}
	if len(roles) == 0 {
		return hint("info", "Roles not loaded yet - Refresh the group.")
	}
	var b strings.Builder
	for i, r := range roles {
		lbl := r.Name
		if r.IsManagementRole {
			lbl += " (management)"
		}
		var act string
		if has[r.ID] {
			act = btn("Remove", "warn", fmt.Sprintf("vrcg-role-del:%d", i), "")
		} else {
			act = btn("Add", "go", fmt.Sprintf("vrcg-role-add:%d", i), "")
		}
		b.WriteString(itemRow(lbl, r.Description, act))
	}
	return b.String()
}

// vgInviteListHTML renders the filtered friends list (modal #vrcg-inv-list); writes the shown
// order back so vrcg-inv-pick indices match.
func (u *UI) vgInviteListHTML() string {
	vgState.mu.Lock()
	loading := vgState.friendsLoading
	q := strings.ToLower(strings.TrimSpace(vgState.fq))
	shown := vgState.shownFriends[:0]
	for _, f := range vgState.friends {
		if q == "" || strings.Contains(strings.ToLower(f.DisplayName), q) {
			shown = append(shown, f)
		}
	}
	vgState.shownFriends = shown

	var b strings.Builder
	switch {
	case loading:
		b.WriteString(hint("info", "Loading friends…"))
	case len(shown) == 0:
		b.WriteString(emptyState("No friends match."))
	default:
		const maxShow = 100 // render cap; filter to narrow
		for i, f := range shown {
			if i >= maxShow {
				b.WriteString(`<div class=vrcg-count>…more matches - filter to narrow down</div>`)
				break
			}
			b.WriteString(itemRow(f.DisplayName, f.Status, btn("Invite", "primary", fmt.Sprintf("vrcg-inv-pick:%d", i), "")))
		}
	}
	vgState.mu.Unlock()
	return b.String()
}

// ── small render helpers ──

// vgPager renders the Load-more / cap footer for a paged list.
func vgPager(loaded, loading, end bool, n int, section string, max int) string {
	switch {
	case loading && loaded:
		return `<div class=btn-row>` + hint("info", "Loading…") + `</div>`
	case loaded && !end && n > 0:
		return btnRow(btn("Load more", "outline", "vrcg-more:"+section, ""))
	case loaded && n >= max:
		return `<div class=btn-row>` + hint("warn", fmt.Sprintf("Showing first %d.", max)) + `</div>`
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
