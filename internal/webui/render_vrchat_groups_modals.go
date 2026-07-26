package webui

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/zigui"
)

// VRChat ▸ Groups DIALOG surfaces (wave-4 dialog sweep B). State builders stay impure (they
// hold vgState.mu and write the shown-friend order back); the renderers below are PURE and
// mirrored byte-for-byte in native/zigui/src/dialogs_b.zig (gate: zigui_golden_dialogs_b_test.go).
//
// Six surfaces, two of which are independently patched fragments:
//   #vrcg-role-body  — the member's add/remove-role list (vgRoleMut re-patches it)
//   #vrcg-inv-list   — the filtered friends list (search + the async friends load patch it)
//   roles modal      — the shell that embeds #vrcg-role-body
//   invite modal     — the shell that embeds #vrcg-inv-list
//   kick/ban confirm — verb-parameterised destructive confirm
//   post-delete confirm
//
// Raw (trusted) fields, matching the Go source literals they replace: the kick/ban `Verb`
// (a "Kick"/"Ban" literal spliced unescaped into the body), the friends-list overflow marker
// and the two picker form blocks' fixed markup. Everything user-derived (member/group/post
// names, statuses, role descriptions) is escaped. Sentence literals that never vary
// ("? This cannot be undone." etc.) live in BOTH renderers - this file has no i18n keys, so
// there is nothing to resolve, and duplicating the literal keeps the goldens honest.

// ── #vrcg-role-body ──

// vgRoleRowSt is one group role with its add/remove button.
type vgRoleRowSt struct {
	Label    string `json:"label"`
	Desc     string `json:"desc"`
	BtnLabel string `json:"btnLabel"`
	BtnVar   string `json:"btnVar"`
	Act      string `json:"act"` // vrcg-role-add:<i> / vrcg-role-del:<i>, index-derived
}

// vgRoleBodySt is #vrcg-role-body: ONE hint (member list moved / roles not loaded) or a row
// per role. HasHint is explicit - an empty hint text must not flip the branch.
type vgRoleBodySt struct {
	HasHint  bool          `json:"hasHint,omitempty"`
	HintTone string        `json:"hintTone,omitempty"`
	HintText string        `json:"hintText,omitempty"`
	Rows     []vgRoleRowSt `json:"rows,omitempty"`
}

// vgRoleBodyHTML renders the pending member's add/remove-role list (modal #vrcg-role-body).
func (u *UI) vgRoleBodyHTML() string {
	st := vgRoleBodyState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVgRoleBodyV2", wireVgRoleBody(st), zigui.RenderVgRoleBodyV2,
			zigui.RenderVgRoleBody, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vgRoleBodyHTMLOf(st)
}

// vgRoleBodyState resolves the role rows for the pending member. Takes vgState.mu.
func vgRoleBodyState() vgRoleBodySt {
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
		return vgRoleBodySt{HasHint: true, HintTone: "warn", HintText: "Member list changed - close and reopen."}
	}
	if len(roles) == 0 {
		return vgRoleBodySt{HasHint: true, HintTone: "info", HintText: "Roles not loaded yet - Refresh the group."}
	}
	st := vgRoleBodySt{Rows: make([]vgRoleRowSt, 0, len(roles))}
	for i, r := range roles {
		row := vgRoleRowSt{Label: r.Name, Desc: r.Description}
		if r.IsManagementRole {
			row.Label += " (management)"
		}
		if has[r.ID] {
			row.BtnLabel, row.BtnVar, row.Act = "Remove", "warn", "vrcg-role-del:"+strconv.Itoa(i)
		} else {
			row.BtnLabel, row.BtnVar, row.Act = "Add", "go", "vrcg-role-add:"+strconv.Itoa(i)
		}
		st.Rows = append(st.Rows, row)
	}
	return st
}

// vgRoleBodyHTMLOf is the pure #vrcg-role-body renderer.
func vgRoleBodyHTMLOf(st vgRoleBodySt) string {
	if st.HasHint {
		return hint(st.HintTone, st.HintText)
	}
	var b strings.Builder
	for _, r := range st.Rows {
		b.WriteString(itemRow(r.Label, r.Desc, btn(r.BtnLabel, r.BtnVar, r.Act, "")))
	}
	return b.String()
}

// ── #vrcg-inv-list ──

// vgInviteRowSt is one invitable friend. Act is index-derived (vrcg-inv-pick:<i>).
type vgInviteRowSt struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Act    string `json:"act"`
}

// vgInviteListSt is #vrcg-inv-list. Exactly one of Loading / Empty / Rows applies; the flags
// are explicit so a blank message can never flip the branch.
type vgInviteListSt struct {
	Loading    bool            `json:"loading,omitempty"`
	LoadingMsg string          `json:"loadingMsg,omitempty"`
	Empty      bool            `json:"empty,omitempty"`
	EmptyMsg   string          `json:"emptyMsg,omitempty"`
	Rows       []vgInviteRowSt `json:"rows,omitempty"`
	HasMore    bool            `json:"hasMore,omitempty"` // the 100-row render cap was hit
	MoreMsg    string          `json:"moreMsg,omitempty"` // RAW: Go source literal
}

// vgInviteListHTML renders the filtered friends list (modal #vrcg-inv-list); writes the shown
// order back so vrcg-inv-pick indices match.
func (u *UI) vgInviteListHTML() string {
	st := vgInviteListState()
	if zigui.Available() {
		if h, ok := zigWire("RenderVgInviteListV2", wireVgInviteList(st), zigui.RenderVgInviteListV2,
			zigui.RenderVgInviteList, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vgInviteListHTMLOf(st)
}

// vgInviteListState filters the loaded friends and RECORDS the shown order (vrcg-inv-pick
// indexes it). Takes vgState.mu; the write-back is a load-bearing side effect.
func vgInviteListState() vgInviteListSt {
	const maxShow = 100 // render cap; filter to narrow
	vgState.mu.Lock()
	defer vgState.mu.Unlock()
	loading := vgState.friendsLoading
	q := strings.ToLower(strings.TrimSpace(vgState.fq))
	shown := vgState.shownFriends[:0]
	for _, f := range vgState.friends {
		if q == "" || strings.Contains(strings.ToLower(f.DisplayName), q) {
			shown = append(shown, f)
		}
	}
	vgState.shownFriends = shown

	switch {
	case loading:
		return vgInviteListSt{Loading: true, LoadingMsg: "Loading friends…"}
	case len(shown) == 0:
		return vgInviteListSt{Empty: true, EmptyMsg: "No friends match."}
	}
	st := vgInviteListSt{Rows: make([]vgInviteRowSt, 0, len(shown))}
	for i, f := range shown {
		if i >= maxShow {
			st.HasMore, st.MoreMsg = true, "…more matches - filter to narrow down"
			break
		}
		st.Rows = append(st.Rows, vgInviteRowSt{Name: f.DisplayName, Status: f.Status, Act: "vrcg-inv-pick:" + strconv.Itoa(i)})
	}
	return st
}

// vgInviteListHTMLOf is the pure #vrcg-inv-list renderer.
func vgInviteListHTMLOf(st vgInviteListSt) string {
	var b strings.Builder
	switch {
	case st.Loading:
		b.WriteString(hint("info", st.LoadingMsg))
	case st.Empty:
		b.WriteString(emptyState(st.EmptyMsg))
	default:
		for _, f := range st.Rows {
			b.WriteString(itemRow(f.Name, f.Status, btn("Invite", "primary", f.Act, "")))
		}
		if st.HasMore {
			b.WriteString(`<div class=vrcg-count>` + st.MoreMsg + `</div>`)
		}
	}
	return b.String()
}

// ── roles modal (shell around #vrcg-role-body) ──

// vgRolesModalSt is the roles dialog: title + the embedded role-body fragment.
type vgRolesModalSt struct {
	Title string       `json:"title"`
	Body  vgRoleBodySt `json:"body"`
}

// vgRolesModalHTML builds the add/remove-role dialog for the pending member.
func (u *UI) vgRolesModalHTML(title string) string {
	st := vgRolesModalSt{Title: title, Body: vgRoleBodyState()}
	if zigui.Available() {
		if h, ok := zigWire("RenderVgRolesModalV2", wireVgRolesModal(st), zigui.RenderVgRolesModalV2,
			zigui.RenderVgRolesModal, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vgRolesModalHTMLOf(st)
}

// vgRolesModalHTMLOf is the pure roles-dialog renderer.
func vgRolesModalHTMLOf(st vgRolesModalSt) string {
	return modal(st.Title, `<div id=vrcg-role-body>`+vgRoleBodyHTMLOf(st.Body)+`</div>`, "")
}

// ── invite modal (shell around #vrcg-inv-list) ──

// vgInviteModalSt is the invite dialog: filter form, the friends list, and the invite-by-id form.
type vgInviteModalSt struct {
	Title    string         `json:"title"`
	SearchPh string         `json:"searchPh"`
	IDPh     string         `json:"idPh"`
	IDBtn    string         `json:"idBtn"`
	List     vgInviteListSt `json:"list"`
}

// vgInviteModalHTML builds the invite picker (the friends list arrives via #vrcg-inv-list).
func (u *UI) vgInviteModalHTML(title string) string {
	st := vgInviteModalSt{
		Title: title, SearchPh: "Filter friends… (Enter)",
		IDPh: "usr_… (invite by user ID)", IDBtn: "Invite ID",
		List: vgInviteListState(),
	}
	if zigui.Available() {
		if h, ok := zigWire("RenderVgInviteModalV2", wireVgInviteModal(st), zigui.RenderVgInviteModalV2,
			zigui.RenderVgInviteModal, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vgInviteModalHTMLOf(st)
}

// vgInviteModalHTMLOf is the pure invite-dialog renderer.
func vgInviteModalHTMLOf(st vgInviteModalSt) string {
	body := `<form data-act=vrcg-inv-search><input class=field-input name=q placeholder=` + attrQ(st.SearchPh) + `></form>` +
		`<div id=vrcg-inv-list>` + vgInviteListHTMLOf(st.List) + `</div>` +
		`<form data-act=vrcg-inv-id class=vrcg-invid><input class=field-input name=userid placeholder=` + attrQ(st.IDPh) + `>` +
		`<button class="rp-btn rp-btn--outline" type=submit>` + htmlEscape(st.IDBtn) + `</button></form>`
	return modal(st.Title, body, "")
}

// ── kick / ban confirm ──

// vgMemberConfirmSt is the kick/ban confirm dialog. Verb is a Go source literal
// ("Kick"/"Ban") spliced UNESCAPED into the body, exactly as the Go original did.
type vgMemberConfirmSt struct {
	Title  string `json:"title"`
	Verb   string `json:"verb"` // RAW in the body sentence
	Name   string `json:"name"`
	Group  string `json:"group"`
	Note   string `json:"note"`
	Act    string `json:"act"` // vrcg-kick-y / vrcg-ban-y
	Cancel string `json:"cancel"`
}

// vgMemberConfirmHTML builds the kick/ban confirm dialog.
func (u *UI) vgMemberConfirmHTML(st vgMemberConfirmSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderVgMemberConfirmV2", wireVgMemberConfirm(st), zigui.RenderVgMemberConfirmV2,
			zigui.RenderVgMemberConfirm, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vgMemberConfirmHTMLOf(st)
}

// vgMemberConfirmHTMLOf is the pure kick/ban confirm renderer.
func vgMemberConfirmHTMLOf(st vgMemberConfirmSt) string {
	body := `<p>` + st.Verb + ` <b>` + htmlEscape(st.Name) + `</b> from ` + htmlEscape(st.Group) + `? ` + htmlEscape(st.Note) + `</p>`
	return modal(st.Title, body,
		btnRow(btn(st.Verb, "destructive", st.Act, ""), btn(st.Cancel, "outline", "modal-close", "")))
}

// ── post-delete confirm ──

// vgPostConfirmSt is the delete-post confirm dialog.
type vgPostConfirmSt struct {
	Title   string `json:"title"`
	Post    string `json:"post"`
	Group   string `json:"group"`
	Confirm string `json:"confirm"`
	Cancel  string `json:"cancel"`
}

// vgPostConfirmHTML builds the delete-post confirm dialog.
func (u *UI) vgPostConfirmHTML(st vgPostConfirmSt) string {
	if zigui.Available() {
		if h, ok := zigWire("RenderVgPostConfirmV2", wireVgPostConfirm(st), zigui.RenderVgPostConfirmV2,
			zigui.RenderVgPostConfirm, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return vgPostConfirmHTMLOf(st)
}

// vgPostConfirmHTMLOf is the pure delete-post confirm renderer.
func vgPostConfirmHTMLOf(st vgPostConfirmSt) string {
	body := `<p>Delete post <b>` + htmlEscape(st.Post) + `</b> from ` + htmlEscape(st.Group) + `? This cannot be undone.</p>`
	return modal(st.Title, body,
		btnRow(btn(st.Confirm, "destructive", "vrcg-post-del-y", ""), btn(st.Cancel, "outline", "modal-close", "")))
}
