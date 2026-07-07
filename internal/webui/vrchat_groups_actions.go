package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/vrchat"
)

// VRChat ▸ Groups sub-tab (vrcg-*): group-management workspace over the local sealed session -
// same Manager→Client()→groups_mgmt call pattern as app.vrchatGateway. List actions carry
// INDICES into vgState snapshots (user strings never enter data-act); kick/ban/role modals act
// on a pending user captured at modal-open. Every list is paged + hard-capped (never unbounded);
// no polling - refresh buttons only. Rendering lives in render_vrchat_groups.go.

const (
	vgPageN      = 50   // page size for every paged list
	vgMaxMembers = 1000 // hard caps per list - Load-more stops here
	vgMaxRows    = 400  // requests / invites / bans
	vgMaxPosts   = 200
	vgMaxAudit   = 500
)

// vgPage is one bounded paged list.
type vgPage[T any] struct {
	rows    []T
	loaded  bool // first fetch finished OK (empty ≠ not loaded)
	end     bool // no more pages (short page or cap hit)
	loading bool
}

// vgGroupInfo is the slice of GetGroup we render.
type vgGroupInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ShortCode   string `json:"shortCode"`
	Description string `json:"description"`
	Rules       string `json:"rules"`
	MemberCount int    `json:"memberCount"`
	OnlineCount int    `json:"onlineMemberCount"`
	JoinState   string `json:"joinState"`
	Privacy     string `json:"privacy"`
	OwnerID     string `json:"ownerId"`
	IsVerified  bool   `json:"isVerified"`
}

// vgUserRow is a decoded request/invite/ban row (only what we render).
type vgUserRow struct{ UserID, Name, When, Sub string }

type vgPost struct{ ID, Title, Text, AuthorID, CreatedAt, Visibility string }

// vgAnn is the group's current announcement (GET /groups/{id}/announcement slice).
type vgAnn struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
}

type vgAudit struct {
	When, Actor, Event, Desc string
	Raw                      string // pretty JSON of the full entry (collapsed <details>)
}

// vgPend is the member a kick/ban/roles modal targets (idx revalidated by userID).
type vgPend struct {
	idx    int
	userID string
	name   string
}

var vgState struct {
	mu  sync.Mutex
	sub string // vrchat tab sub-view: "" = profile

	groupsLoaded, groupsLoading bool
	groups                      []vrchat.Group
	filter                      string
	shown                       []vrchat.Group // filtered picker order (index target)

	selID, selName string
	view           string // overview|members|requests|invites|bans|posts|audit

	ovLoading bool
	info      *vgGroupInfo // replaced, never mutated (renderers read outside the lock)
	roles     []vrchat.GroupRole
	perms     map[string]bool
	owner     bool

	members vgPage[vrchat.GroupMemberFull]
	reqs    vgPage[vgUserRow]
	invs    vgPage[vgUserRow]
	bans    vgPage[vgUserRow]
	posts   vgPage[vgPost]
	audit   vgPage[vgAudit]

	ann *vgAnn // current announcement (nil = none/not loaded); fetched with the posts page

	pend     vgPend
	pendPost vgPost // post a delete modal targets (captured at modal-open)

	friendsLoading bool
	friends        []vrchat.Friend
	shownFriends   []vrchat.Friend // filtered invite-picker order (index target)
	fq             string
}

func init() {
	onPrefix("vrcg-sub:", func(u *UI, m actMsg) {
		vgState.mu.Lock()
		vgState.sub = m.arg("vrcg-sub:")
		vgState.mu.Unlock()
		u.patchMain()
	})
	onExact("vrcg-filter", func(u *UI, m actMsg) {
		vgState.mu.Lock()
		vgState.filter = strings.TrimSpace(parseForm(m.Form)["q"])
		vgState.mu.Unlock()
		u.vgPatch()
	})
	onExact("vrcg-refresh-groups", func(u *UI, _ actMsg) { u.vgLoadGroups(true) })
	onPrefix("vrcg-open:", func(u *UI, m actMsg) { u.vgOpen(m.arg("vrcg-open:")) })
	onExact("vrcg-back", func(u *UI, _ actMsg) {
		vgState.mu.Lock()
		vgState.selID, vgState.info = "", nil
		vgState.mu.Unlock()
		u.vgPatch()
	})
	onPrefix("vrcg-view:", func(u *UI, m actMsg) { u.vgSetView(m.arg("vrcg-view:")) })
	onExact("vrcg-reload", func(u *UI, _ actMsg) { u.vgReload() })
	onPrefix("vrcg-more:", func(u *UI, m actMsg) { u.vgLoadRows(m.arg("vrcg-more:"), false) })

	// members
	onPrefix("vrcg-roles:", func(u *UI, m actMsg) { u.vgRolesModal(m.arg("vrcg-roles:")) })
	onPrefix("vrcg-role-add:", func(u *UI, m actMsg) { u.vgRoleMut(m.arg("vrcg-role-add:"), true) })
	onPrefix("vrcg-role-del:", func(u *UI, m actMsg) { u.vgRoleMut(m.arg("vrcg-role-del:"), false) })
	onPrefix("vrcg-kick:", func(u *UI, m actMsg) { u.vgConfirmMember(m.arg("vrcg-kick:"), "kick") })
	onPrefix("vrcg-ban:", func(u *UI, m actMsg) { u.vgConfirmMember(m.arg("vrcg-ban:"), "ban") })
	onExact("vrcg-kick-y", func(u *UI, _ actMsg) { u.vgMemberMut("kick") })
	onExact("vrcg-ban-y", func(u *UI, _ actMsg) { u.vgMemberMut("ban") })

	// requests / invites / bans
	onPrefix("vrcg-req-a:", func(u *UI, m actMsg) { u.vgRespond(m.arg("vrcg-req-a:"), "accept") })
	onPrefix("vrcg-req-r:", func(u *UI, m actMsg) { u.vgRespond(m.arg("vrcg-req-r:"), "reject") })
	onPrefix("vrcg-inv-cancel:", func(u *UI, m actMsg) { u.vgCancelInvite(m.arg("vrcg-inv-cancel:")) })
	onPrefix("vrcg-unban:", func(u *UI, m actMsg) { u.vgUnban(m.arg("vrcg-unban:")) })
	onExact("vrcg-invite", func(u *UI, _ actMsg) { u.vgInviteModal() })
	onExact("vrcg-inv-search", func(u *UI, m actMsg) {
		vgState.mu.Lock()
		vgState.fq = strings.TrimSpace(parseForm(m.Form)["q"])
		vgState.mu.Unlock()
		u.eval("window.__patch('vrcg-inv-list'," + jsQuote(u.vgInviteListHTML()) + ")")
	})
	onPrefix("vrcg-inv-pick:", func(u *UI, m actMsg) { u.vgInviteFriend(m.arg("vrcg-inv-pick:")) })
	onExact("vrcg-inv-id", func(u *UI, m actMsg) { u.vgInviteByID(parseForm(m.Form)["userid"]) })

	// announcement + posts
	onExact("vrcg-ann", func(u *UI, m actMsg) { u.vgAnnounce(m.Form) })
	onExact("vrcg-post", func(u *UI, m actMsg) { u.vgCreatePost(m.Form) })
	onPrefix("vrcg-post-del:", func(u *UI, m actMsg) { u.vgConfirmPostDelete(m.arg("vrcg-post-del:")) })
	onExact("vrcg-post-del-y", func(u *UI, _ actMsg) { u.vgDeletePost() })
}

// ── small state helpers ──

// vrcgSub returns the VRChat tab's active sub-view ("profile"|"groups").
func (u *UI) vrcgSub() string {
	vgState.mu.Lock()
	defer vgState.mu.Unlock()
	if vgState.sub == "groups" {
		return "groups"
	}
	return "profile"
}

func (u *UI) vgPatch() {
	u.eval("window.__patch('vrcg-body'," + jsQuote(u.vrcgBody()) + ")")
}

// vgReady = VRChat wired + signed in (toast otherwise).
func (u *UI) vgReady() bool {
	if u.svc.Vrchat == nil || !u.svc.Vrchat.State().LoggedIn {
		u.toast("Sign in to VRChat first (Settings ▸ Integrations)")
		return false
	}
	return true
}

func vgSel() (id, name string) {
	vgState.mu.Lock()
	defer vgState.mu.Unlock()
	return vgState.selID, vgState.selName
}

func (u *UI) vgErr(what string, err error) {
	if err != nil {
		u.logErr("vrcg "+what, err)
		u.toast("Load failed: " + err.Error())
	}
}

// ── loaders ──

// vgLoadGroups loads my groups (single-flight; force = ignore cache).
func (u *UI) vgLoadGroups(force bool) {
	if u.svc.Vrchat == nil || !u.svc.Vrchat.State().LoggedIn {
		return
	}
	vgState.mu.Lock()
	if vgState.groupsLoading || (vgState.groupsLoaded && !force) {
		vgState.mu.Unlock()
		return
	}
	vgState.groupsLoading = true
	vgState.mu.Unlock()
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		got, err := u.svc.Vrchat.Client().UserGroups(ctx, u.svc.Vrchat.CurrentUserID())
		u.vgErr("groups", err)
		sort.Slice(got, func(i, j int) bool { return strings.ToLower(got[i].Name) < strings.ToLower(got[j].Name) })
		vgState.mu.Lock()
		// loaded=true even on error - render must NOT auto-retry (Refresh button does)
		vgState.groups, vgState.groupsLoaded, vgState.groupsLoading = got, true, false
		vgState.mu.Unlock()
		u.vgPatch()
	})
}

// vgOpen selects a picker row (index into vgState.shown) and loads the workspace.
func (u *UI) vgOpen(arg string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	if err != nil || idx < 0 || idx >= len(vgState.shown) {
		vgState.mu.Unlock()
		return
	}
	g := vgState.shown[idx]
	vgState.selID, vgState.selName = g.EffectiveID(), g.Name
	vgState.view = "overview"
	vgState.info, vgState.roles, vgState.perms, vgState.owner = nil, nil, nil, false
	vgState.members = vgPage[vrchat.GroupMemberFull]{}
	vgState.reqs, vgState.invs, vgState.bans = vgPage[vgUserRow]{}, vgPage[vgUserRow]{}, vgPage[vgUserRow]{}
	vgState.posts = vgPage[vgPost]{}
	vgState.audit = vgPage[vgAudit]{}
	vgState.ann = nil
	vgState.mu.Unlock()
	u.vgPatch()
	u.vgLoadOverview()
}

// vgLoadOverview fetches group meta + enriched roles + my permissions (stale-guarded by selID).
func (u *UI) vgLoadOverview() {
	id, _ := vgSel()
	if id == "" || !u.vgReady() {
		return
	}
	vgState.mu.Lock()
	if vgState.ovLoading {
		vgState.mu.Unlock()
		return
	}
	vgState.ovLoading = true
	vgState.mu.Unlock()
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		cli := u.svc.Vrchat.Client()
		var info *vgGroupInfo
		raw, err := cli.GetGroup(ctx, id)
		if err == nil {
			var gi vgGroupInfo
			if json.Unmarshal(raw, &gi) == nil {
				info = &gi
			}
		} else {
			u.vgErr("group", err)
		}
		roles, rErr := cli.GroupRoles(ctx, id)
		u.logErr("vrcg roles", rErr)
		sort.SliceStable(roles, func(i, j int) bool { return roles[i].Order < roles[j].Order })
		perms := map[string]bool{}
		if mp, pErr := cli.GroupMyPermissions(ctx, id); pErr == nil {
			if mem, ok := mp["membership"].(map[string]any); ok {
				if arr, ok := mem["permissions"].([]any); ok {
					for _, v := range arr {
						if s, ok := v.(string); ok {
							perms[s] = true
						}
					}
				}
			}
		} else {
			u.logErr("vrcg myperms", pErr)
		}
		owner := info != nil && info.OwnerID != "" && info.OwnerID == u.svc.Vrchat.CurrentUserID()
		vgState.mu.Lock()
		if vgState.selID == id { // drop stale load after switching groups
			vgState.info, vgState.roles, vgState.perms, vgState.owner = info, roles, perms, owner
		}
		vgState.ovLoading = false
		vgState.mu.Unlock()
		u.vgPatch()
	})
}

// vgSetView switches the workspace section, lazy-loading its first page.
func (u *UI) vgSetView(v string) {
	vgState.mu.Lock()
	vgState.view = v
	need := false
	switch v {
	case "members":
		need = !vgState.members.loaded && !vgState.members.loading
	case "requests":
		need = !vgState.reqs.loaded && !vgState.reqs.loading
	case "invites":
		need = !vgState.invs.loaded && !vgState.invs.loading
	case "bans":
		need = !vgState.bans.loaded && !vgState.bans.loading
	case "posts":
		need = !vgState.posts.loaded && !vgState.posts.loading
	case "audit":
		need = !vgState.audit.loaded && !vgState.audit.loading
	}
	vgState.mu.Unlock()
	u.vgPatch()
	if need {
		u.vgLoadRows(v, true)
	}
}

// vgReload refreshes whatever is showing.
func (u *UI) vgReload() {
	vgState.mu.Lock()
	v, sel := vgState.view, vgState.selID
	vgState.mu.Unlock()
	if sel == "" {
		u.vgLoadGroups(true)
		return
	}
	switch v {
	case "", "overview":
		u.vgLoadOverview()
	default:
		u.vgLoadRows(v, true)
	}
}

// vgBegin marks a page fetch started (call under vgState.mu). reset drops loaded rows.
func vgBegin[T any](p *vgPage[T], reset bool) (offset int, ok bool) {
	if reset {
		*p = vgPage[T]{}
	}
	if p.loading || p.end {
		return 0, false
	}
	p.loading = true
	return len(p.rows), true
}

// vgFinish stores one fetched page (call under vgState.mu). sel=false ⇒ stale, drop.
func vgFinish[T any](p *vgPage[T], sel bool, page []T, err error, max int) {
	p.loading = false
	if !sel {
		return
	}
	if err != nil {
		return
	}
	p.loaded = true
	p.rows = append(p.rows, page...)
	p.end = len(page) < vgPageN || len(p.rows) >= max
}

// vgLoadRows fetches the next (or first, reset=true) page of a workspace section.
func (u *UI) vgLoadRows(section string, reset bool) {
	id, _ := vgSel()
	if id == "" || !u.vgReady() {
		return
	}
	var offset int
	ok := false
	vgState.mu.Lock()
	switch section {
	case "members":
		offset, ok = vgBegin(&vgState.members, reset)
	case "requests":
		offset, ok = vgBegin(&vgState.reqs, reset)
	case "invites":
		offset, ok = vgBegin(&vgState.invs, reset)
	case "bans":
		offset, ok = vgBegin(&vgState.bans, reset)
	case "posts":
		offset, ok = vgBegin(&vgState.posts, reset)
	case "audit":
		offset, ok = vgBegin(&vgState.audit, reset)
	}
	vgState.mu.Unlock()
	if !ok {
		return
	}
	u.vgPatch() // show loading state
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		cli := u.svc.Vrchat.Client()
		switch section {
		case "members":
			rows, err := cli.GroupMembersFull(ctx, id, offset, vgPageN)
			u.vgErr("members", err)
			vgState.mu.Lock()
			vgFinish(&vgState.members, vgState.selID == id, rows, err, vgMaxMembers)
			vgState.mu.Unlock()
		case "requests":
			raw, err := cli.GroupRequests(ctx, id, offset, vgPageN)
			u.vgErr("requests", err)
			vgState.mu.Lock()
			vgFinish(&vgState.reqs, vgState.selID == id, vgDecodeUsers(raw), err, vgMaxRows)
			vgState.mu.Unlock()
		case "invites":
			raw, err := cli.GroupInvites(ctx, id, offset, vgPageN)
			u.vgErr("invites", err)
			vgState.mu.Lock()
			vgFinish(&vgState.invs, vgState.selID == id, vgDecodeUsers(raw), err, vgMaxRows)
			vgState.mu.Unlock()
		case "bans":
			raw, err := cli.GroupBans(ctx, id, offset, vgPageN)
			u.vgErr("bans", err)
			vgState.mu.Lock()
			vgFinish(&vgState.bans, vgState.selID == id, vgDecodeUsers(raw), err, vgMaxRows)
			vgState.mu.Unlock()
		case "posts":
			raw, err := cli.GroupPosts(ctx, id, offset, vgPageN, false)
			u.vgErr("posts", err)
			// First page also refreshes the pinned announcement (best-effort; {} ⇒ none).
			var ann *vgAnn
			if offset == 0 {
				if ar, aerr := cli.GroupCurrentAnnouncement(ctx, id); aerr == nil {
					var a vgAnn
					if json.Unmarshal(ar, &a) == nil && (a.Title != "" || a.Text != "") {
						ann = &a
					}
				}
			}
			vgState.mu.Lock()
			vgFinish(&vgState.posts, vgState.selID == id, vgDecodePosts(raw), err, vgMaxPosts)
			if offset == 0 && vgState.selID == id {
				vgState.ann = ann
			}
			vgState.mu.Unlock()
		case "audit":
			raw, err := cli.GroupAuditLogs(ctx, id, offset, vgPageN, "", "")
			u.vgErr("audit", err)
			vgState.mu.Lock()
			vgFinish(&vgState.audit, vgState.selID == id, vgDecodeAudit(raw), err, vgMaxAudit)
			vgState.mu.Unlock()
		}
		u.vgPatch()
	})
}

// ── wire decoders (json.RawMessage → render structs; decode boundary only) ──

func vgDecodeUsers(raw []json.RawMessage) []vgUserRow {
	out := make([]vgUserRow, 0, len(raw))
	for _, r := range raw {
		var w struct {
			UserID string `json:"userId"`
			User   struct {
				ID          string `json:"id"`
				DisplayName string `json:"displayName"`
			} `json:"user"`
			CreatedAt        string `json:"createdAt"`
			RequestedAt      string `json:"requestedAt"`
			BannedAt         string `json:"bannedAt"`
			JoinedAt         string `json:"joinedAt"`
			Message          string `json:"requestMessage"`
			MembershipStatus string `json:"membershipStatus"`
		}
		if json.Unmarshal(r, &w) != nil {
			continue
		}
		row := vgUserRow{UserID: w.UserID}
		if row.UserID == "" {
			row.UserID = w.User.ID
		}
		row.Name = w.User.DisplayName
		if row.Name == "" {
			row.Name = row.UserID
		}
		row.When = vgFirst(w.BannedAt, w.RequestedAt, w.CreatedAt, w.JoinedAt)
		row.Sub = vgFirst(w.Message, w.MembershipStatus)
		if row.UserID != "" {
			out = append(out, row)
		}
	}
	return out
}

func vgDecodePosts(raw []json.RawMessage) []vgPost {
	out := make([]vgPost, 0, len(raw))
	for _, r := range raw {
		var w struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Text       string `json:"text"`
			AuthorID   string `json:"authorId"`
			CreatedAt  string `json:"createdAt"`
			Visibility string `json:"visibility"`
		}
		if json.Unmarshal(r, &w) != nil {
			continue
		}
		out = append(out, vgPost{ID: w.ID, Title: w.Title, Text: w.Text, AuthorID: w.AuthorID, CreatedAt: w.CreatedAt, Visibility: w.Visibility})
	}
	return out
}

func vgDecodeAudit(raw []json.RawMessage) []vgAudit {
	out := make([]vgAudit, 0, len(raw))
	for _, r := range raw {
		var w struct {
			CreatedSnake string `json:"created_at"`
			CreatedCamel string `json:"createdAt"`
			ActorName    string `json:"actorDisplayName"`
			ActorID      string `json:"actorId"`
			EventType    string `json:"eventType"`
			Description  string `json:"description"`
		}
		if json.Unmarshal(r, &w) != nil {
			continue
		}
		var buf bytes.Buffer
		pretty := string(r)
		if json.Indent(&buf, r, "", "  ") == nil {
			pretty = buf.String()
		}
		out = append(out, vgAudit{
			When:  vgFirst(w.CreatedSnake, w.CreatedCamel),
			Actor: vgFirst(w.ActorName, w.ActorID),
			Event: w.EventType,
			Desc:  w.Description,
			Raw:   pretty,
		})
	}
	return out
}

// vgFirst returns the first non-empty string.
func vgFirst(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// ── member mutations (kick / ban / roles) ──

// vgConfirmMember captures the member behind a list index and opens the kick/ban confirm modal.
func (u *UI) vgConfirmMember(arg, kind string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	if err != nil || idx < 0 || idx >= len(vgState.members.rows) {
		vgState.mu.Unlock()
		return
	}
	m := vgState.members.rows[idx]
	uid := ""
	if m.UserID != nil {
		uid = *m.UserID
	}
	vgState.pend = vgPend{idx: idx, userID: uid, name: vgMemberName(m)}
	name, gname := vgState.pend.name, vgState.selName
	vgState.mu.Unlock()
	if uid == "" {
		return
	}
	verb, note := "Kick", "They can rejoin or request to join again."
	if kind == "ban" {
		verb, note = "Ban", "They are removed and cannot rejoin until unbanned."
	}
	body := `<p>` + verb + ` <b>` + htmlEscape(name) + `</b> from ` + htmlEscape(gname) + `? ` + htmlEscape(note) + `</p>`
	u.openModal(modal(verb+" member", body,
		btnRow(btn(verb, "destructive", "vrcg-"+kind+"-y", ""), btn("Cancel", "outline", "modal-close", ""))))
}

// vgMemberMut runs the confirmed kick/ban against the pending member.
func (u *UI) vgMemberMut(kind string) {
	id, _ := vgSel()
	vgState.mu.Lock()
	p := vgState.pend
	vgState.mu.Unlock()
	if id == "" || p.userID == "" || !u.vgReady() {
		return
	}
	u.closeModal()
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		cli := u.svc.Vrchat.Client()
		var err error
		verb, past := "Kick", "Kicked "
		if kind == "ban" {
			verb, past = "Ban", "Banned "
			_, err = cli.BanGroupMember(ctx, id, p.userID)
		} else {
			_, err = cli.KickGroupMember(ctx, id, p.userID)
		}
		if err != nil {
			u.toast(verb + " failed: " + err.Error())
			u.logErr("vrcg "+kind, err)
			return
		}
		vgState.mu.Lock()
		for i := range vgState.members.rows {
			m := vgState.members.rows[i]
			if m.UserID != nil && *m.UserID == p.userID {
				vgState.members.rows = append(vgState.members.rows[:i], vgState.members.rows[i+1:]...)
				break
			}
		}
		if vgState.info != nil && vgState.info.MemberCount > 0 { // copy-on-write, renderers hold the old ptr
			gi := *vgState.info
			gi.MemberCount--
			vgState.info = &gi
		}
		banLoaded := vgState.bans.loaded
		vgState.mu.Unlock()
		u.toast(past + p.name)
		if kind == "ban" && banLoaded {
			u.vgLoadRows("bans", true) // patches when done
			return
		}
		u.vgPatch()
	})
}

// vgRolesModal opens the add/remove-role modal for a member (index into members).
func (u *UI) vgRolesModal(arg string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	if err != nil || idx < 0 || idx >= len(vgState.members.rows) {
		vgState.mu.Unlock()
		return
	}
	m := vgState.members.rows[idx]
	uid := ""
	if m.UserID != nil {
		uid = *m.UserID
	}
	vgState.pend = vgPend{idx: idx, userID: uid, name: vgMemberName(m)}
	name := vgState.pend.name
	vgState.mu.Unlock()
	if uid == "" {
		return
	}
	u.openModal(modal("Roles - "+name, `<div id=vrcg-role-body>`+u.vgRoleBodyHTML()+`</div>`, ""))
}

// vgRoleMut adds/removes the role at index arg (into vgState.roles) on the pending member.
func (u *UI) vgRoleMut(arg string, add bool) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	p := vgState.pend
	var role vrchat.GroupRole
	ok := err == nil && idx >= 0 && idx < len(vgState.roles)
	if ok {
		role = vgState.roles[idx]
	}
	vgState.mu.Unlock()
	id, _ := vgSel()
	if !ok || id == "" || p.userID == "" || !u.vgReady() {
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		cli := u.svc.Vrchat.Client()
		var mErr error
		if add {
			_, mErr = cli.AddGroupRole(ctx, id, p.userID, role.ID)
		} else {
			_, mErr = cli.RemoveGroupRole(ctx, id, p.userID, role.ID)
		}
		if mErr != nil {
			u.toast("Role change failed: " + mErr.Error())
			u.logErr("vrcg role", mErr)
			return
		}
		vgState.mu.Lock()
		if p.idx >= 0 && p.idx < len(vgState.members.rows) {
			m := &vgState.members.rows[p.idx]
			if m.UserID != nil && *m.UserID == p.userID {
				if add {
					m.RoleIDs = append(m.RoleIDs, role.ID)
				} else {
					for i, rid := range m.RoleIDs {
						if rid == role.ID {
							m.RoleIDs = append(m.RoleIDs[:i], m.RoleIDs[i+1:]...)
							break
						}
					}
				}
			}
		}
		vgState.mu.Unlock()
		verb := "removed from"
		if add {
			verb = "added to"
		}
		u.toast("Role " + role.Name + " " + verb + " " + p.name)
		u.eval("window.__patch('vrcg-role-body'," + jsQuote(u.vgRoleBodyHTML()) + ")")
		u.vgPatch()
	})
}

// ── request / invite / ban row actions ──

// vgRespond accepts/rejects the join request at index arg.
func (u *UI) vgRespond(arg, action string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	var r vgUserRow
	ok := err == nil && idx >= 0 && idx < len(vgState.reqs.rows)
	if ok {
		r = vgState.reqs.rows[idx]
	}
	vgState.mu.Unlock()
	id, _ := vgSel()
	if !ok || id == "" || r.UserID == "" || !u.vgReady() {
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Vrchat.Client().RespondGroupRequest(ctx, id, r.UserID, action); err != nil {
			u.toast("Request " + action + " failed: " + err.Error())
			u.logErr("vrcg request", err)
			return
		}
		msg := "Rejected "
		if action == "accept" {
			msg = "Accepted "
		}
		u.toast(msg + r.Name)
		vgRemoveUserRow(&vgState.reqs, r.UserID)
		u.vgPatch()
	})
}

// vgCancelInvite rescinds the invite at index arg.
func (u *UI) vgCancelInvite(arg string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	var r vgUserRow
	ok := err == nil && idx >= 0 && idx < len(vgState.invs.rows)
	if ok {
		r = vgState.invs.rows[idx]
	}
	vgState.mu.Unlock()
	id, _ := vgSel()
	if !ok || id == "" || r.UserID == "" || !u.vgReady() {
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Vrchat.Client().CancelGroupInvite(ctx, id, r.UserID); err != nil {
			u.toast("Cancel failed: " + err.Error())
			u.logErr("vrcg invite cancel", err)
			return
		}
		u.toast("Invite cancelled: " + r.Name)
		vgRemoveUserRow(&vgState.invs, r.UserID)
		u.vgPatch()
	})
}

// vgUnban lifts the ban at index arg.
func (u *UI) vgUnban(arg string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	var r vgUserRow
	ok := err == nil && idx >= 0 && idx < len(vgState.bans.rows)
	if ok {
		r = vgState.bans.rows[idx]
	}
	vgState.mu.Unlock()
	id, _ := vgSel()
	if !ok || id == "" || r.UserID == "" || !u.vgReady() {
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Vrchat.Client().UnbanGroupMember(ctx, id, r.UserID); err != nil {
			u.toast("Unban failed: " + err.Error())
			u.logErr("vrcg unban", err)
			return
		}
		u.toast("Unbanned " + r.Name)
		vgRemoveUserRow(&vgState.bans, r.UserID)
		u.vgPatch()
	})
}

func vgRemoveUserRow(p *vgPage[vgUserRow], uid string) {
	vgState.mu.Lock()
	for i := range p.rows {
		if p.rows[i].UserID == uid {
			p.rows = append(p.rows[:i], p.rows[i+1:]...)
			break
		}
	}
	vgState.mu.Unlock()
}

// ── invite flow ──

// vgInviteModal opens the invite picker and loads friends (paged, capped at 500/side).
func (u *UI) vgInviteModal() {
	_, name := vgSel()
	if !u.vgReady() {
		return
	}
	vgState.mu.Lock()
	vgState.fq, vgState.friends, vgState.shownFriends, vgState.friendsLoading = "", nil, nil, true
	vgState.mu.Unlock()
	body := `<form data-act=vrcg-inv-search><input class=field-input name=q placeholder="Filter friends… (Enter)"></form>` +
		`<div id=vrcg-inv-list>` + u.vgInviteListHTML() + `</div>` +
		`<form data-act=vrcg-inv-id class=vrcg-invid><input class=field-input name=userid placeholder="usr_… (invite by user ID)">` +
		`<button class="rp-btn rp-btn--outline" type=submit>Invite ID</button></form>`
	u.openModal(modal("Invite to "+name, body, ""))
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cli := u.svc.Vrchat.Client()
		var got []vrchat.Friend
		for _, offline := range []bool{false, true} {
			for offset := 0; offset < 500; offset += 100 {
				page, err := cli.Friends(ctx, offset, 100, offline)
				if err != nil || len(page) == 0 {
					break
				}
				got = append(got, page...)
				if len(page) < 100 {
					break
				}
			}
		}
		sort.Slice(got, func(i, j int) bool { return strings.ToLower(got[i].DisplayName) < strings.ToLower(got[j].DisplayName) })
		vgState.mu.Lock()
		vgState.friends, vgState.friendsLoading = got, false
		vgState.mu.Unlock()
		u.eval("window.__patch('vrcg-inv-list'," + jsQuote(u.vgInviteListHTML()) + ")")
	})
}

// vgInviteFriend invites the friend at index arg (into the filtered shownFriends order).
func (u *UI) vgInviteFriend(arg string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	var fr vrchat.Friend
	ok := err == nil && idx >= 0 && idx < len(vgState.shownFriends)
	if ok {
		fr = vgState.shownFriends[idx]
	}
	vgState.mu.Unlock()
	if !ok {
		return
	}
	u.vgInvite(fr.ID, fr.DisplayName)
}

func (u *UI) vgInviteByID(uid string) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		u.toast("Enter a user ID (usr_…)")
		return
	}
	u.vgInvite(uid, uid)
}

func (u *UI) vgInvite(uid, name string) {
	id, _ := vgSel()
	if id == "" || !u.vgReady() {
		return
	}
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Vrchat.Client().InviteToGroup(ctx, id, uid, false); err != nil {
			u.toast("Invite failed: " + err.Error())
			u.logErr("vrcg invite", err)
			return
		}
		u.toast("Invited " + name)
		vgState.mu.Lock()
		reload := vgState.invs.loaded
		vgState.mu.Unlock()
		if reload {
			u.vgLoadRows("invites", true)
		}
	})
}

// ── announcement ──

// vgAnnounce posts a group announcement from the vrcg-ann form.
func (u *UI) vgAnnounce(form string) {
	id, _ := vgSel()
	if id == "" || !u.vgReady() {
		return
	}
	m := parseForm(form)
	title, text := strings.TrimSpace(m["title"]), strings.TrimSpace(m["text"])
	if title == "" || text == "" {
		u.toast("Announcement needs a title and text")
		return
	}
	in := vrchat.AnnouncementIn{Title: title, Text: text, SendNotification: m["notify"] != "", ImageID: strings.TrimSpace(m["imageid"])}
	u.toast("Posting announcement…")
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Vrchat.Client().GroupAnnouncement(ctx, id, in); err != nil {
			u.toast("Announcement failed: " + err.Error())
			u.logErr("vrcg announcement", err)
			return
		}
		u.toast("Announcement posted")
		u.vgLoadRows("posts", true) // clears the form via re-render + shows the new post
	})
}

// vgCreatePost creates a group post from the vrcg-post form.
func (u *UI) vgCreatePost(form string) {
	id, _ := vgSel()
	if id == "" || !u.vgReady() {
		return
	}
	m := parseForm(form)
	title, text := strings.TrimSpace(m["title"]), strings.TrimSpace(m["text"])
	if title == "" || text == "" {
		u.toast("Post needs a title and text")
		return
	}
	in := vrchat.PostIn{Title: title, Text: text, SendNotification: m["notify"] != ""}
	u.toast("Creating post…")
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Vrchat.Client().CreateGroupPost(ctx, id, in); err != nil {
			u.toast("Post failed: " + err.Error())
			u.logErr("vrcg post", err)
			return
		}
		u.toast("Post created")
		u.vgLoadRows("posts", true)
	})
}

// vgConfirmPostDelete opens the delete confirm for a post (arg = index into posts rows).
func (u *UI) vgConfirmPostDelete(arg string) {
	idx, err := strconv.Atoi(arg)
	vgState.mu.Lock()
	if err != nil || idx < 0 || idx >= len(vgState.posts.rows) {
		vgState.mu.Unlock()
		return
	}
	p := vgState.posts.rows[idx]
	vgState.pendPost = p
	gname := vgState.selName
	vgState.mu.Unlock()
	if p.ID == "" {
		u.toast("Post has no id - refresh posts and retry")
		return
	}
	body := `<p>Delete post <b>` + htmlEscape(p.Title) + `</b> from ` + htmlEscape(gname) + `? This cannot be undone.</p>`
	u.openModal(modal("Delete post", body,
		btnRow(btn("Delete post", "destructive", "vrcg-post-del-y", ""), btn("Cancel", "outline", "modal-close", ""))))
}

// vgDeletePost runs the confirmed delete against the pending post.
func (u *UI) vgDeletePost() {
	id, _ := vgSel()
	vgState.mu.Lock()
	p := vgState.pendPost
	vgState.mu.Unlock()
	if id == "" || p.ID == "" || !u.vgReady() {
		return
	}
	u.closeModal()
	u.bg(func() {
		ctx, cancel := u.actx()
		defer cancel()
		if _, err := u.svc.Vrchat.Client().DeleteGroupPost(ctx, id, p.ID); err != nil {
			u.toast("Delete failed: " + err.Error())
			u.logErr("vrcg post-del", err)
			return
		}
		vgState.mu.Lock()
		for i := range vgState.posts.rows {
			if vgState.posts.rows[i].ID == p.ID {
				vgState.posts.rows = append(vgState.posts.rows[:i], vgState.posts.rows[i+1:]...)
				break
			}
		}
		vgState.pendPost = vgPost{}
		vgState.mu.Unlock()
		u.toast("Post deleted")
		u.vgPatch()
	})
}
