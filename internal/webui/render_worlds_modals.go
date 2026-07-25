package webui

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/vrcperm"
	"rave.page/mate/internal/zigui"
)

// Worlds DIALOG surfaces (wave-4 dialog sweep B): the permission-list editor, the poster-slot
// editor, the friend/group/role pickers and the GitHub device-code dialog. State builders stay
// impure (they hold wsState.mu and RECORD the picker index order); the renderers below are PURE
// and mirrored in native/zigui/src/dialogs_b.zig (gate: zigui_golden_dialogs_b_test.go).
//
// Same literal rule as the Worlds tab (render_worlds.go): prose the Go renderer inserted as
// source literals - ws-help paragraphs, form placeholders, submit-button labels - stays
// UNESCAPED in both renderers (they carry apostrophes; escaping would change the DOM). They are
// trusted literals resolved here, never user input. Everything user-derived (list/group/role/
// friend names, poster URLs, device codes) is escaped.
//
// Three of these surfaces are patched in place after their async load: #world-fr-list,
// #world-grp-list and #world-role-list each have their own state + export.

// ── permission-list editor ──

// wsEntryRowSt is one granted entry (friend name or group role). Act is index-derived.
type wsEntryRowSt struct {
	Label string `json:"label"`
	Act   string `json:"act"`
}

// wsListEditorSt is the per-list entry editor dialog. Help/AddPh/AddBtn are RAW trusted literals.
type wsListEditorSt struct {
	Title     string         `json:"title"`
	Help      string         `json:"help"`
	Empty     bool           `json:"empty,omitempty"`
	EmptyMsg  string         `json:"emptyMsg,omitempty"`
	Entries   []wsEntryRowSt `json:"entries,omitempty"`
	DelLabel  string         `json:"delLabel"`
	AddPh     string         `json:"addPh"`
	AddBtn    string         `json:"addBtn"`
	FriendBtn string         `json:"friendBtn"`
	FriendAct string         `json:"friendAct"`
	GroupBtn  string         `json:"groupBtn"`
	GroupAct  string         `json:"groupAct"`
}

// wsListEditorHTML builds the per-list entry editor (delete + add-name + friend/role pickers).
// Records the edited list id in wsState so index-based entry actions resolve it.
func (u *UI) wsListEditorHTML(l *config.PermList) string {
	st := wsListEditorState(l)
	if zigui.Available() {
		if h, ok := zigui.RenderWsListEditor(stateJSON(st)); ok {
			return h
		}
	}
	return wsListEditorHTMLOf(st)
}

// wsListEditorState resolves the entry labels; the wsState.editList write-back is the
// load-bearing side effect the entry actions index against.
func wsListEditorState(l *config.PermList) wsListEditorSt {
	wsState.mu.Lock()
	wsState.editList = l.ID
	wsState.mu.Unlock()

	st := wsListEditorSt{
		Title:     "Edit list: " + l.Name,
		Help:      "Role grants publish that role's member names to the gist (unlisted but public URL). Only whole-group/role member names are listed - never user ids.",
		EmptyMsg:  "Empty list - add friends or group roles",
		Entries:   make([]wsEntryRowSt, 0, len(l.Entries)),
		DelLabel:  "Delete",
		AddPh:     "exact VRChat display name",
		AddBtn:    "Add name",
		FriendBtn: "Add friend…",
		FriendAct: "world-friends:" + l.ID,
		GroupBtn:  "Add group role…",
		GroupAct:  "world-groups:" + l.ID,
	}
	st.Empty = len(l.Entries) == 0
	for i := range l.Entries {
		e := l.Entries[i]
		label := "User: " + e.Display
		if e.Kind == config.PermEntryGroupRole {
			role := e.RoleName
			if role == "" {
				role = "all members"
			}
			label = "Group role: " + e.GroupName + " - " + role
		}
		st.Entries = append(st.Entries, wsEntryRowSt{Label: label, Act: "world-ent-del:" + strconv.Itoa(i)})
	}
	return st
}

// wsListEditorHTMLOf is the pure list-editor renderer.
func wsListEditorHTMLOf(st wsListEditorSt) string {
	var body strings.Builder
	body.WriteString(`<p class=ws-help>` + st.Help + `</p>`)
	body.WriteString(`<div class=ws-entries>`)
	if st.Empty {
		body.WriteString(emptyState(st.EmptyMsg))
	}
	for _, e := range st.Entries {
		body.WriteString(itemRow(e.Label, "", btn(st.DelLabel, "destructive", e.Act, "")))
	}
	body.WriteString(`</div>`)
	body.WriteString(`<form class=ws-addrow data-act=world-name-add>` +
		`<input class=field-input name=name placeholder=` + attrQ(st.AddPh) + ` autocomplete=off>` +
		`<button class="rp-btn rp-btn--outline" type=submit>` + st.AddBtn + `</button></form>`)
	body.WriteString(btnRow(
		btn(st.FriendBtn, "primary", st.FriendAct, ""),
		btn(st.GroupBtn, "outline", st.GroupAct, ""),
	))
	return modal(st.Title, body.String(), "")
}

// ── poster-slot editor ──

// wsPosterEditorSt is the poster-slot editor form. Labels/placeholders/Save are RAW literals.
type wsPosterEditorSt struct {
	Title   string `json:"title"`
	Idx     string `json:"idx"` // strconv.Itoa, spliced into a hidden input
	ImgLbl  string `json:"imgLbl"`
	Img     string `json:"img"`
	ImgPh   string `json:"imgPh"`
	CapLbl  string `json:"capLbl"`
	Caption string `json:"caption"`
	CapPh   string `json:"capPh"`
	LinkLbl string `json:"linkLbl"`
	Link    string `json:"link"`
	LinkPh  string `json:"linkPh"`
	HasWarn bool   `json:"hasWarn,omitempty"` // image host not on VRChat's allowlist
	Warn    string `json:"warn,omitempty"`
	Save    string `json:"save"`
}

// wsPosterEditorHTML builds the poster-slot editor form.
func (u *UI) wsPosterEditorHTML(idx int, p config.WorldPoster) string {
	st := wsPosterEditorState(idx, p)
	if zigui.Available() {
		if h, ok := zigui.RenderWsPosterEditor(stateJSON(st)); ok {
			return h
		}
	}
	return wsPosterEditorHTMLOf(st)
}

// wsPosterEditorState resolves the poster slot + the image-host allowlist verdict.
func wsPosterEditorState(idx int, p config.WorldPoster) wsPosterEditorSt {
	st := wsPosterEditorSt{
		Title: "Edit poster", Idx: strconv.Itoa(idx),
		ImgLbl: "Image", Img: p.Img, ImgPh: "https://i.imgur.com/… (VRC image-allowlisted host)",
		CapLbl: "Caption", Caption: p.Caption, CapPh: "caption",
		LinkLbl: "Link", Link: p.Link, LinkPh: "https://rave.page/… (shown as text/QR)",
		Save: "Save",
	}
	if p.Img != "" && !vrcperm.ImageHostAllowed(p.Img) {
		st.HasWarn = true
		st.Warn = "Host not on VRChat's image allowlist - prefab shows text only"
	}
	return st
}

// wsPosterEditorHTMLOf is the pure poster-editor renderer.
func wsPosterEditorHTMLOf(st wsPosterEditorSt) string {
	warn := ""
	if st.HasWarn {
		warn = `<div class=wsst-line>` + hint("bad", st.Warn) + `</div>`
	}
	body := `<form data-act=world-poster-save>` +
		`<input type=hidden name=idx value="` + st.Idx + `">` +
		wsPosterField(st.ImgLbl, "img", st.Img, st.ImgPh) +
		wsPosterField(st.CapLbl, "caption", st.Caption, st.CapPh) +
		wsPosterField(st.LinkLbl, "link", st.Link, st.LinkPh) +
		warn +
		`<div class=btn-row><button class="rp-btn rp-btn--primary" type=submit>` + st.Save + `</button></div></form>`
	return modal(st.Title, body, "")
}

// wsPosterField is one hand-rolled name=-carrying form field (no data-act, so components.go
// fieldEx does not fit). label + placeholder are RAW literals; the value is escaped.
func wsPosterField(label, name, value, ph string) string {
	return `<label class=field><span class=field-label>` + label + `</span>` +
		`<input class=field-input name=` + name + ` value="` + htmlEscape(value) + `" placeholder="` + ph + `" autocomplete=off></label>`
}

// ── friend picker (#world-fr-list) ──

// wsPickRowSt is one picker row: a label plus its trailing action button(s).
type wsPickRowSt struct {
	Label string `json:"label"`
	Act   string `json:"act"`
}

// wsFriendListSt is #world-fr-list. Exactly one of Loading / Empty / Rows applies. Loading and
// the over-cap marker are RAW ws-help literals.
type wsFriendListSt struct {
	Loading    bool          `json:"loading,omitempty"`
	LoadingMsg string        `json:"loadingMsg,omitempty"`
	Rows       []wsPickRowSt `json:"rows,omitempty"`
	AddLabel   string        `json:"addLabel,omitempty"`
	HasMore    bool          `json:"hasMore,omitempty"` // the 60-row render cap was hit
	MoreMsg    string        `json:"moreMsg,omitempty"`
	Empty      bool          `json:"empty,omitempty"`
	EmptyMsg   string        `json:"emptyMsg,omitempty"`
}

// wsFriendPickerSt is the friend-picker dialog shell around #world-fr-list.
type wsFriendPickerSt struct {
	Title    string         `json:"title"`
	SearchPh string         `json:"searchPh"`
	BackLbl  string         `json:"backLbl"`
	BackAct  string         `json:"backAct"`
	List     wsFriendListSt `json:"list"`
}

// wsFriendPickerHTML is the friend-picker modal shell (list filled async by the handler).
func (u *UI) wsFriendPickerHTML(listID string) string {
	st := wsFriendPickerSt{
		Title: "Add friend", SearchPh: "filter friends…",
		BackLbl: "Back to list", BackAct: "world-list-edit:" + listID,
		List: wsFriendListState(),
	}
	if zigui.Available() {
		if h, ok := zigui.RenderWsFriendPicker(stateJSON(st)); ok {
			return h
		}
	}
	return wsFriendPickerHTMLOf(st)
}

// wsFriendPickerHTMLOf is the pure friend-picker renderer.
func wsFriendPickerHTMLOf(st wsFriendPickerSt) string {
	body := `<form class=ws-search data-act=world-fr-search>` +
		`<input class=field-input name=q placeholder=` + attrQ(st.SearchPh) + ` autocomplete=off></form>` +
		`<div class=ws-picklist id=world-fr-list>` + wsFriendListHTMLOf(st.List) + `</div>`
	return modal(st.Title, body, btn(st.BackLbl, "outline", st.BackAct, ""))
}

// wsFriendListHTML renders the loaded (filtered) friends into pick rows. The pick action carries
// the friend's index into wsState.friends (stable across filtering - never the display name).
func (u *UI) wsFriendListHTML() string {
	st := wsFriendListState()
	if zigui.Available() {
		if h, ok := zigui.RenderWsFriendList(stateJSON(st)); ok {
			return h
		}
	}
	return wsFriendListHTMLOf(st)
}

// wsFriendListState filters the loaded friends (Unicode ToLower stays Go-side) and caps at 60.
func wsFriendListState() wsFriendListSt {
	const capRows = 60
	wsState.mu.Lock()
	friends := wsState.friends
	q := strings.ToLower(strings.TrimSpace(wsState.fq))
	loading := wsState.friendsLoading
	wsState.mu.Unlock()
	if loading {
		return wsFriendListSt{Loading: true, LoadingMsg: "Loading friends…"}
	}
	st := wsFriendListSt{Rows: make([]wsPickRowSt, 0, len(friends)), AddLabel: "Add"}
	for i, fr := range friends {
		if q != "" && !strings.Contains(strings.ToLower(fr.DisplayName), q) {
			continue
		}
		if len(st.Rows) >= capRows {
			st.HasMore, st.MoreMsg = true, "… refine the filter to see more"
			break
		}
		st.Rows = append(st.Rows, wsPickRowSt{Label: fr.DisplayName, Act: "world-fr-pick:" + strconv.Itoa(i)})
	}
	if len(st.Rows) == 0 {
		st.Empty, st.EmptyMsg = true, "No match"
		if len(friends) == 0 {
			st.EmptyMsg = "No friends found"
		}
	}
	return st
}

// wsFriendListHTMLOf is the pure #world-fr-list renderer.
func wsFriendListHTMLOf(st wsFriendListSt) string {
	if st.Loading {
		return `<p class=ws-help>` + st.LoadingMsg + `</p>`
	}
	var b strings.Builder
	for _, r := range st.Rows {
		b.WriteString(itemRow(r.Label, "", btn(st.AddLabel, "primary", r.Act, "")))
	}
	if st.HasMore {
		b.WriteString(`<p class=ws-help>` + st.MoreMsg + `</p>`)
	}
	if st.Empty {
		b.WriteString(emptyState(st.EmptyMsg))
	}
	return b.String()
}

// ── group picker (#world-grp-list) ──

// wsGroupRowSt is one group row: label + the pin/unpin and Roles… buttons.
type wsGroupRowSt struct {
	Label    string `json:"label"`
	FavLabel string `json:"favLabel"` // "☆ Pin" / "★ Unpin"
	FavAct   string `json:"favAct"`
	RolesAct string `json:"rolesAct"`
}

// wsGroupSecSt is one captioned section of the group list (Favorites / Your groups / results).
type wsGroupSecSt struct {
	Caption string         `json:"caption"`
	Rows    []wsGroupRowSt `json:"rows,omitempty"`
}

// wsGroupListSt is #world-grp-list. Loading is a leading ws-help paragraph, not a branch - the
// Go renderer emits it AND then whatever sections already resolved.
type wsGroupListSt struct {
	Loading    bool           `json:"loading,omitempty"`
	LoadingMsg string         `json:"loadingMsg,omitempty"`
	Sections   []wsGroupSecSt `json:"sections,omitempty"`
	RolesLabel string         `json:"rolesLabel,omitempty"`
	Empty      bool           `json:"empty,omitempty"` // !loading && no rows at all
	EmptyMsg   string         `json:"emptyMsg,omitempty"`
}

// wsGroupPickerSt is the group-picker dialog shell around #world-grp-list.
type wsGroupPickerSt struct {
	Title     string        `json:"title"`
	SearchPh  string        `json:"searchPh"`
	SearchBtn string        `json:"searchBtn"`
	Help      string        `json:"help"`
	BackLbl   string        `json:"backLbl"`
	BackAct   string        `json:"backAct"`
	List      wsGroupListSt `json:"list"`
}

// wsGroupPickerHTML is the group/role-picker modal shell.
func (u *UI) wsGroupPickerHTML(listID string) string {
	st := wsGroupPickerSt{
		Title: "Add group role", SearchPh: "search all groups…", SearchBtn: "Search",
		Help:    "Grant a whole group or a role. Member expansion only works where the member list is visible (public groups); private groups keep their last good expansion.",
		BackLbl: "Back to list", BackAct: "world-list-edit:" + listID,
		List: u.wsGroupListState(),
	}
	if zigui.Available() {
		if h, ok := zigui.RenderWsGroupPicker(stateJSON(st)); ok {
			return h
		}
	}
	return wsGroupPickerHTMLOf(st)
}

// wsGroupPickerHTMLOf is the pure group-picker renderer.
func wsGroupPickerHTMLOf(st wsGroupPickerSt) string {
	body := `<form class=ws-search data-act=world-grp-search>` +
		`<input class=field-input name=q placeholder=` + attrQ(st.SearchPh) + ` autocomplete=off>` +
		`<button class="rp-btn rp-btn--outline" type=submit>` + st.SearchBtn + `</button></form>` +
		`<div class=ws-picklist id=world-grp-list>` + wsGroupListHTMLOf(st.List) + `</div>` +
		`<p class=ws-help>` + st.Help + `</p>`
	return modal(st.Title, body, btn(st.BackLbl, "outline", st.BackAct, ""))
}

// wsGroupListHTML renders favorites + your groups + search results as pick rows, and records the
// flattened display order in wsState.pickGroups so fav/roles actions index it (group names may
// carry chars fmt %q would mangle in data-act).
func (u *UI) wsGroupListHTML() string {
	st := u.wsGroupListState()
	if zigui.Available() {
		if h, ok := zigui.RenderWsGroupList(stateJSON(st)); ok {
			return h
		}
	}
	return wsGroupListHTMLOf(st)
}

// wsGroupListState flattens favorites + own groups + search results and RECORDS the display
// order in wsState.pickGroups - the load-bearing side effect the fav/roles actions index.
func (u *UI) wsGroupListState() wsGroupListSt {
	f := &u.svc.Cfg.Features.WorldSync
	wsState.mu.Lock()
	mine := wsState.mygroups
	results := wsState.results
	loading := wsState.groupsLoading
	wsState.mu.Unlock()

	isFav := func(id string) bool {
		for _, g := range f.FavoriteGroups {
			if g.ID == id {
				return true
			}
		}
		return false
	}

	st := wsGroupListSt{Sections: make([]wsGroupSecSt, 0, 3), RolesLabel: "Roles…"}
	var refs []groupRef
	row := func(id, name string, members int) wsGroupRowSt {
		idx := len(refs)
		refs = append(refs, groupRef{ID: id, Name: name})
		favLabel := "☆ Pin"
		if isFav(id) {
			favLabel = "★ Unpin"
		}
		lbl := name
		if members > 0 {
			lbl = name + " (" + strconv.Itoa(members) + " members)"
		}
		return wsGroupRowSt{Label: lbl, FavLabel: favLabel,
			FavAct: "world-fav:" + strconv.Itoa(idx), RolesAct: "world-roles:" + strconv.Itoa(idx)}
	}

	if loading {
		st.Loading, st.LoadingMsg = true, "Loading your groups…"
	}
	if len(f.FavoriteGroups) > 0 {
		sec := wsGroupSecSt{Caption: "Favorites", Rows: make([]wsGroupRowSt, 0, len(f.FavoriteGroups))}
		for _, g := range f.FavoriteGroups {
			sec.Rows = append(sec.Rows, row(g.ID, g.Name, 0))
		}
		st.Sections = append(st.Sections, sec)
	}
	if len(mine) > 0 {
		sec := wsGroupSecSt{Caption: "Your groups", Rows: make([]wsGroupRowSt, 0, len(mine))}
		for _, g := range mine {
			if !isFav(g.EffectiveID()) {
				sec.Rows = append(sec.Rows, row(g.EffectiveID(), g.Name, g.MemberCount))
			}
		}
		st.Sections = append(st.Sections, sec)
	}
	if len(results) > 0 {
		sec := wsGroupSecSt{Caption: "Search results", Rows: make([]wsGroupRowSt, 0, len(results))}
		for _, g := range results {
			if !isFav(g.EffectiveID()) {
				sec.Rows = append(sec.Rows, row(g.EffectiveID(), g.Name, g.MemberCount))
			}
		}
		st.Sections = append(st.Sections, sec)
	}
	if !loading && len(refs) == 0 {
		st.Empty, st.EmptyMsg = true, "No groups - search above"
	}

	wsState.mu.Lock()
	wsState.pickGroups = refs
	wsState.mu.Unlock()
	return st
}

// wsGroupListHTMLOf is the pure #world-grp-list renderer.
func wsGroupListHTMLOf(st wsGroupListSt) string {
	var b strings.Builder
	if st.Loading {
		b.WriteString(`<p class=ws-help>` + st.LoadingMsg + `</p>`)
	}
	for _, sec := range st.Sections {
		b.WriteString(`<div class=ws-caps>` + sec.Caption + `</div>`)
		for _, r := range sec.Rows {
			b.WriteString(itemRow(r.Label, "", btnRow(
				btn(r.FavLabel, "ghost", r.FavAct, ""),
				btn(st.RolesLabel, "primary", r.RolesAct, ""),
			)))
		}
	}
	if st.Empty {
		b.WriteString(emptyState(st.EmptyMsg))
	}
	return b.String()
}

// ── role picker (#world-role-list) ──

// wsRoleListSt is #world-role-list: "All members" plus one row per group role. Loading is the
// initial state the modal shell renders before the async load patches it.
type wsRoleListSt struct {
	Loading    bool          `json:"loading,omitempty"`
	LoadingMsg string        `json:"loadingMsg,omitempty"`
	AllLabel   string        `json:"allLabel,omitempty"`
	GrantLabel string        `json:"grantLabel,omitempty"`
	Rows       []wsPickRowSt `json:"rows,omitempty"`
}

// wsRolePickerSt is the role-grant dialog shell around #world-role-list.
type wsRolePickerSt struct {
	Title   string       `json:"title"`
	BackLbl string       `json:"backLbl"`
	BackAct string       `json:"backAct"`
	List    wsRoleListSt `json:"list"`
}

// wsRolePickerHTML builds the role-grant dialog for a group (roles load async into #world-role-list).
func (u *UI) wsRolePickerHTML(groupName, listID string) string {
	st := wsRolePickerSt{
		Title: "Roles of " + groupName, BackLbl: "Back to groups", BackAct: "world-groups:" + listID,
		List: wsRoleListSt{Loading: true, LoadingMsg: "Loading roles…"},
	}
	if zigui.Available() {
		if h, ok := zigui.RenderWsRolePicker(stateJSON(st)); ok {
			return h
		}
	}
	return wsRolePickerHTMLOf(st)
}

// wsRolePickerHTMLOf is the pure role-picker renderer.
func wsRolePickerHTMLOf(st wsRolePickerSt) string {
	body := `<div id=world-role-list>` + wsRoleListHTMLOf(st.List) + `</div>`
	return modal(st.Title, body, btn(st.BackLbl, "outline", st.BackAct, ""))
}

// wsRoleListHTML renders the loaded roles into grant rows (#world-role-list patch).
func (u *UI) wsRoleListHTML(st wsRoleListSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderWsRoleList(stateJSON(st)); ok {
			return h
		}
	}
	return wsRoleListHTMLOf(st)
}

// wsRoleListHTMLOf is the pure #world-role-list renderer.
func wsRoleListHTMLOf(st wsRoleListSt) string {
	if st.Loading {
		return `<p class=ws-help>` + st.LoadingMsg + `</p>`
	}
	var b strings.Builder
	b.WriteString(itemRow(st.AllLabel, "", btn(st.GrantLabel, "primary", "world-role-pick:all", "")))
	for _, r := range st.Rows {
		b.WriteString(itemRow(r.Label, "", btn(st.GrantLabel, "primary", r.Act, "")))
	}
	return b.String()
}

// ── GitHub device-code dialog ──

// wsDeviceSt is the GitHub device-code dialog. Help is a RAW literal; the code + URI are escaped
// (the code also rides as a data-val, which btn escapes).
type wsDeviceSt struct {
	Title   string `json:"title"`
	Help    string `json:"help"`
	Code    string `json:"code"`
	CopyLbl string `json:"copyLbl"`
	OpenLbl string `json:"openLbl"`
	URI     string `json:"uri"`
}

// wsDeviceHTML builds the GitHub device-code dialog.
func (u *UI) wsDeviceHTML(code, uri string) string {
	st := wsDeviceSt{
		Title: "Link GitHub",
		Help:  "Open the activation page and enter this code, then approve in your browser:",
		Code:  code, CopyLbl: "Copy code", OpenLbl: "Open activation page", URI: uri,
	}
	if zigui.Available() {
		if h, ok := zigui.RenderWsDevice(stateJSON(st)); ok {
			return h
		}
	}
	return wsDeviceHTMLOf(st)
}

// wsDeviceHTMLOf is the pure device-code dialog renderer.
func wsDeviceHTMLOf(st wsDeviceSt) string {
	body := `<p class=ws-help>` + st.Help + `</p>` +
		`<div class=ws-devcode>` + htmlEscape(st.Code) + `</div>` +
		btnRow(btn(st.CopyLbl, "ghost", "copy", st.Code), btn(st.OpenLbl, "outline", "open-url", st.URI))
	return modal(st.Title, body, "")
}
