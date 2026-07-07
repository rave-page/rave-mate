package webui

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/vrcperm"
)

// Worlds tab action handlers + live-tick registration. Publish buttons reuse the already-wired
// ws-pub-* actions (ui.go); everything else is namespaced world-*. Picker state (loaded VRChat
// friends/groups/roles) is ephemeral UI state held here - single window, one user. Actions carry
// list ids (safe: we mint them as list-<digits>) or INDICES into wsState; user-supplied strings
// (display/group/role names, paths) never enter data-act (fmt %q would mangle them).

type groupRef struct{ ID, Name string }

var wsState struct {
	mu             sync.Mutex
	editList       string // list id the open editor/picker targets
	friends        []vrchat.Friend
	fq             string // friend filter
	friendsLoading bool
	mygroups       []vrchat.Group
	results        []vrchat.Group
	groupsLoading  bool
	pickGroups     []groupRef         // flattened order shown in the group picker (index target)
	roleGroup      groupRef           // group whose roles are shown
	roles          []vrchat.GroupRole // roles shown in the role picker (index target)
}

func init() {
	// Status region refresh (~1 Hz while Worlds is active): publish outcomes change off-thread.
	onLiveTick("worlds", func(u *UI) {
		if u.svc.WorldSync == nil {
			return
		}
		u.eval("window.__patch('world-linkhint'," + jsQuote(u.worldsLinkHintInner()) + ")")
		f := &u.svc.Cfg.Features.WorldSync
		for i := range f.Lists {
			l := &f.Lists[i]
			key := "list:" + l.ID
			u.eval("window.__patch('world-st-" + key + "'," + jsQuote(u.wsStatusInner(key, l.GistID, vrcperm.FileNames)) + ")")
		}
		u.eval("window.__patch('world-st-posters'," + jsQuote(u.wsStatusInner("posters", f.PostersGistID, vrcperm.FilePosters)) + ")")
		u.eval("window.__patch('world-st-events'," + jsQuote(u.wsStatusInner("events", f.EventsGistID, vrcperm.FileEvents)) + ")")
		u.eval("window.__patch('world-st-nowplaying'," + jsQuote(u.wsStatusInner("nowplaying", f.NowPlayingGistID, vrcperm.FileNowPlaying)) + ")")
	})

	// ── channel toggles + fields ──
	onExact("world-posters-on", func(u *UI, m actMsg) { u.wsSetBool(&u.svc.Cfg.Features.WorldSync.PostersOn, m.Val) })
	onExact("world-events-on", func(u *UI, m actMsg) { u.wsSetBool(&u.svc.Cfg.Features.WorldSync.EventsOn, m.Val) })
	onExact("world-np-on", func(u *UI, m actMsg) { u.wsSetBool(&u.svc.Cfg.Features.WorldSync.NowPlayingOn, m.Val) })
	onExact("world-np-link", func(u *UI, m actMsg) {
		u.svc.Cfg.Features.WorldSync.NowPlayingLink = strings.TrimSpace(m.Val)
		u.saveCfg()
	})
	onExact("world-np-img", func(u *UI, m actMsg) {
		u.svc.Cfg.Features.WorldSync.NowPlayingImg = strings.TrimSpace(m.Val)
		u.saveCfg()
		u.patchMain() // refresh the allowlist warning
	})

	// ── permission lists ──
	onExact("world-list-add", func(u *UI, m actMsg) {
		name := strings.TrimSpace(parseForm(m.Form)["name"])
		if name == "" {
			u.toast("Name the list first")
			return
		}
		f := &u.svc.Cfg.Features.WorldSync
		f.Lists = append(f.Lists, config.PermList{ID: fmt.Sprintf("list-%d", time.Now().UnixNano()), Name: name})
		u.saveCfg()
		u.patchMain()
		u.openModal(u.wsListEditorHTML(&f.Lists[len(f.Lists)-1]))
	})
	onPrefix("world-list-edit:", func(u *UI, m actMsg) {
		if l := u.wsList(m.arg("world-list-edit:")); l != nil {
			u.openModal(u.wsListEditorHTML(l))
		}
	})
	onPrefix("world-list-del:", func(u *UI, m actMsg) {
		id := m.arg("world-list-del:")
		l := u.wsList(id)
		if l == nil {
			return
		}
		body := `<p>Delete "` + html.EscapeString(l.Name) + `"? The gist stays on GitHub (delete it there if needed).</p>`
		footer := btnRow(btn("Delete", "destructive", "world-list-del-y:"+id, ""), btn("Cancel", "outline", "modal-close", ""))
		u.openModal(modal("Delete list", body, footer))
	})
	onPrefix("world-list-del-y:", func(u *UI, m actMsg) {
		id := m.arg("world-list-del-y:")
		f := &u.svc.Cfg.Features.WorldSync
		for i := range f.Lists {
			if f.Lists[i].ID == id {
				f.Lists = append(f.Lists[:i], f.Lists[i+1:]...)
				u.saveCfg()
				break
			}
		}
		u.closeModal()
		u.patchMain()
		u.toast("List deleted")
	})

	// ── list entries (index into the edited list = wsState.editList) ──
	onExact("world-name-add", func(u *UI, m actMsg) {
		l := u.wsList(u.wsEditList())
		name := strings.TrimSpace(parseForm(m.Form)["name"])
		if l == nil || name == "" {
			return
		}
		l.Entries = append(l.Entries, config.PermEntry{Kind: config.PermEntryUser, Display: name})
		u.saveCfg()
		u.patchMain()
		u.openModal(u.wsListEditorHTML(l))
	})
	onPrefix("world-ent-del:", func(u *UI, m actMsg) {
		l := u.wsList(u.wsEditList())
		idx, err := strconv.Atoi(m.arg("world-ent-del:"))
		if l == nil || err != nil || idx < 0 || idx >= len(l.Entries) {
			return
		}
		l.Entries = append(l.Entries[:idx], l.Entries[idx+1:]...)
		u.saveCfg()
		u.patchMain()
		u.openModal(u.wsListEditorHTML(l))
	})

	// ── posters ──
	onExact("world-poster-add", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.WorldSync
		f.Posters = append(f.Posters, config.WorldPoster{})
		u.saveCfg()
		u.patchMain()
		u.openModal(u.wsPosterEditorHTML(len(f.Posters)-1, config.WorldPoster{}))
	})
	onPrefix("world-poster-edit:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.WorldSync
		idx, err := strconv.Atoi(m.arg("world-poster-edit:"))
		if err != nil || idx < 0 || idx >= len(f.Posters) {
			return
		}
		u.openModal(u.wsPosterEditorHTML(idx, f.Posters[idx]))
	})
	onPrefix("world-poster-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.WorldSync
		idx, err := strconv.Atoi(m.arg("world-poster-del:"))
		if err != nil || idx < 0 || idx >= len(f.Posters) {
			return
		}
		f.Posters = append(f.Posters[:idx], f.Posters[idx+1:]...)
		u.saveCfg()
		u.patchMain()
	})
	onExact("world-poster-save", func(u *UI, m actMsg) {
		fm := parseForm(m.Form)
		f := &u.svc.Cfg.Features.WorldSync
		idx, err := strconv.Atoi(fm["idx"])
		if err != nil || idx < 0 || idx >= len(f.Posters) {
			return
		}
		f.Posters[idx] = config.WorldPoster{
			Img: strings.TrimSpace(fm["img"]), Caption: fm["caption"], Link: strings.TrimSpace(fm["link"]),
		}
		u.saveCfg()
		u.closeModal()
		u.patchMain()
	})

	// ── friend picker ──
	onPrefix("world-friends:", func(u *UI, m actMsg) { u.wsOpenFriendPicker(m.arg("world-friends:")) })
	onExact("world-fr-search", func(u *UI, m actMsg) {
		wsState.mu.Lock()
		wsState.fq = strings.TrimSpace(parseForm(m.Form)["q"])
		wsState.mu.Unlock()
		u.eval("window.__patch('world-fr-list'," + jsQuote(u.wsFriendListHTML()) + ")")
	})
	onPrefix("world-fr-pick:", func(u *UI, m actMsg) {
		idx, err := strconv.Atoi(m.arg("world-fr-pick:"))
		l := u.wsList(u.wsEditList())
		wsState.mu.Lock()
		friends := wsState.friends
		wsState.mu.Unlock()
		if err != nil || l == nil || idx < 0 || idx >= len(friends) {
			return
		}
		fr := friends[idx]
		l.Entries = append(l.Entries, config.PermEntry{Kind: config.PermEntryUser, UserID: fr.ID, Display: fr.DisplayName})
		u.saveCfg()
		u.patchMain()
		u.toast("Added " + fr.DisplayName)
	})

	// ── group / role picker ──
	onPrefix("world-groups:", func(u *UI, m actMsg) { u.wsOpenGroupPicker(m.arg("world-groups:")) })
	onExact("world-grp-search", func(u *UI, m actMsg) { u.wsGroupSearch(strings.TrimSpace(parseForm(m.Form)["q"])) })
	onPrefix("world-fav:", func(u *UI, m actMsg) {
		if ref, ok := u.wsGroupAt(m.arg("world-fav:")); ok {
			u.wsToggleFav(ref.ID, ref.Name)
			u.eval("window.__patch('world-grp-list'," + jsQuote(u.wsGroupListHTML()) + ")")
		}
	})
	onPrefix("world-roles:", func(u *UI, m actMsg) {
		if ref, ok := u.wsGroupAt(m.arg("world-roles:")); ok {
			u.wsOpenRolePicker(ref)
		}
	})
	onPrefix("world-role-pick:", func(u *UI, m actMsg) {
		l := u.wsList(u.wsEditList())
		if l == nil {
			return
		}
		arg := m.arg("world-role-pick:")
		wsState.mu.Lock()
		grp, roles := wsState.roleGroup, wsState.roles
		wsState.mu.Unlock()
		e := config.PermEntry{Kind: config.PermEntryGroupRole, GroupID: grp.ID, GroupName: grp.Name}
		if arg != "all" {
			idx, err := strconv.Atoi(arg)
			if err != nil || idx < 0 || idx >= len(roles) {
				return
			}
			e.RoleID, e.RoleName = roles[idx].ID, roles[idx].Name
		}
		l.Entries = append(l.Entries, e)
		u.saveCfg()
		u.patchMain()
		u.openModal(u.wsListEditorHTML(l))
	})

	// ── GitHub link ──
	onExact("world-gh-unlink", func(u *UI, m actMsg) {
		if u.svc.GitHub != nil {
			u.svc.GitHub.Logout()
		}
		u.wsPatchGitHub()
	})
	onExact("world-gh-device", func(u *UI, m actMsg) { u.wsGitHubDevice() })
	onExact("world-gh-pat", func(u *UI, m actMsg) {
		body := hint("info", "Classic token, 'gist' scope only") +
			`<p class=ws-help>Create at github.com/settings/tokens → classic → only the 'gist' scope. Stored sealed (OS secret store), never logged.</p>` +
			`<form data-act=world-gh-pat-save><input class=field-input name=pat type=password placeholder="ghp_… (gist scope)" autocomplete=off style="width:100%">` +
			`<div class=btn-row><button class="rp-btn rp-btn--primary" type=submit>Link</button></div></form>`
		u.openModal(modal("Paste GitHub token", body, ""))
	})
	onExact("world-gh-pat-save", func(u *UI, m actMsg) {
		gh := u.svc.GitHub
		pat := strings.TrimSpace(parseForm(m.Form)["pat"])
		if gh == nil || pat == "" {
			return
		}
		u.closeModal()
		u.toast("Linking GitHub…")
		u.bg(func() {
			ctx, cancel := u.actx()
			defer cancel()
			if err := gh.SetPAT(ctx, pat); err != nil {
				u.toast("Token rejected: " + err.Error())
				u.logErr("github pat", err)
				return
			}
			u.toast("GitHub linked as " + gh.Login())
			u.wsPatchGitHub()
		})
	})

	// ── Unity hand-off ──
	onPrefix("world-unity-write:", func(u *UI, m actMsg) {
		if u.svc.WorldSync == nil {
			return
		}
		idx, err := strconv.Atoi(m.arg("world-unity-write:"))
		projects := u.svc.Cfg.Features.Unity.Projects
		if err != nil || idx < 0 || idx >= len(projects) {
			return
		}
		if err := unityproj.WriteWorldSyncSources(projects[idx], u.svc.WorldSync.SourcesJSON()); err != nil {
			u.toast("Write failed: " + err.Error())
			u.logErr("unity write", err)
			return
		}
		u.toast("sources.json written")
	})
}

// ── helpers ──

func (u *UI) wsSetBool(p *bool, val string) {
	*p = val == "true"
	u.saveCfg()
}

func (u *UI) wsList(id string) *config.PermList {
	f := &u.svc.Cfg.Features.WorldSync
	for i := range f.Lists {
		if f.Lists[i].ID == id {
			return &f.Lists[i]
		}
	}
	return nil
}

func (u *UI) wsEditList() string {
	wsState.mu.Lock()
	defer wsState.mu.Unlock()
	return wsState.editList
}

// wsGroupAt resolves a group-picker row index to its ref.
func (u *UI) wsGroupAt(arg string) (groupRef, bool) {
	idx, err := strconv.Atoi(arg)
	wsState.mu.Lock()
	defer wsState.mu.Unlock()
	if err != nil || idx < 0 || idx >= len(wsState.pickGroups) {
		return groupRef{}, false
	}
	return wsState.pickGroups[idx], true
}

func (u *UI) wsToggleFav(id, name string) {
	f := &u.svc.Cfg.Features.WorldSync
	for i, g := range f.FavoriteGroups {
		if g.ID == id {
			f.FavoriteGroups = append(f.FavoriteGroups[:i], f.FavoriteGroups[i+1:]...)
			u.saveCfg()
			return
		}
	}
	f.FavoriteGroups = append(f.FavoriteGroups, config.FavoriteGroup{ID: id, Name: name})
	u.saveCfg()
}

func (u *UI) wsPatchGitHub() {
	u.eval("window.__patch('world-gh'," + jsQuote(u.worldsGitHubInner()) + ")")
	u.eval("window.__patch('world-linkhint'," + jsQuote(u.worldsLinkHintInner()) + ")")
}

// wsVrcReady reports VRChat linked; toasts + returns false otherwise.
func (u *UI) wsVrcReady() bool {
	if u.svc.Vrchat == nil || !u.svc.Vrchat.State().LoggedIn {
		u.toast("Link VRChat first (Settings ▸ Integrations)")
		return false
	}
	return true
}

func (u *UI) wsOpenFriendPicker(listID string) {
	if !u.wsVrcReady() {
		return
	}
	wsState.mu.Lock()
	wsState.editList, wsState.fq, wsState.friends, wsState.friendsLoading = listID, "", nil, true
	wsState.mu.Unlock()
	u.openModal(u.wsFriendPickerHTML(listID))
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
		wsState.mu.Lock()
		wsState.friends, wsState.friendsLoading = got, false
		wsState.mu.Unlock()
		u.eval("window.__patch('world-fr-list'," + jsQuote(u.wsFriendListHTML()) + ")")
	})
}

func (u *UI) wsOpenGroupPicker(listID string) {
	if !u.wsVrcReady() {
		return
	}
	wsState.mu.Lock()
	wsState.editList, wsState.mygroups, wsState.results, wsState.groupsLoading = listID, nil, nil, true
	wsState.mu.Unlock()
	u.openModal(u.wsGroupPickerHTML(listID))
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		got, err := u.svc.Vrchat.Client().UserGroups(ctx, u.svc.Vrchat.CurrentUserID())
		u.logErr("vrchat groups", err)
		wsState.mu.Lock()
		wsState.mygroups, wsState.groupsLoading = got, false
		wsState.mu.Unlock()
		u.eval("window.__patch('world-grp-list'," + jsQuote(u.wsGroupListHTML()) + ")")
	})
}

func (u *UI) wsGroupSearch(q string) {
	if q == "" || !u.wsVrcReady() {
		return
	}
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		got, err := u.svc.Vrchat.Client().SearchGroups(ctx, q, 0, 30)
		if err != nil {
			u.toast("Search failed: " + err.Error())
			return
		}
		wsState.mu.Lock()
		wsState.results = got
		wsState.mu.Unlock()
		u.eval("window.__patch('world-grp-list'," + jsQuote(u.wsGroupListHTML()) + ")")
	})
}

// wsOpenRolePicker loads a group's roles and renders the grant modal (roles indexed via wsState).
func (u *UI) wsOpenRolePicker(grp groupRef) {
	if !u.wsVrcReady() {
		return
	}
	listID := u.wsEditList()
	wsState.mu.Lock()
	wsState.roleGroup, wsState.roles = grp, nil
	wsState.mu.Unlock()
	u.openModal(modal("Roles of "+grp.Name, `<div id=world-role-list><p class=ws-help>Loading roles…</p></div>`,
		btn("Back to groups", "outline", "world-groups:"+listID, "")))
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		roles, err := u.svc.Vrchat.Client().GroupRoles(ctx, grp.ID)
		if err != nil {
			u.eval("window.__patch('world-role-list'," + jsQuote(hint("bad", "Could not load roles: "+err.Error())) + ")")
			return
		}
		wsState.mu.Lock()
		wsState.roles = roles
		wsState.mu.Unlock()
		var b strings.Builder
		b.WriteString(itemRow("All members", "", btn("Grant", "primary", "world-role-pick:all", "")))
		for i, r := range roles {
			lbl := r.Name
			if r.IsManagementRole {
				lbl += " (management)"
			}
			b.WriteString(itemRow(lbl, "", btn("Grant", "primary", "world-role-pick:"+strconv.Itoa(i), "")))
		}
		u.eval("window.__patch('world-role-list'," + jsQuote(b.String()) + ")")
	})
}

// wsGitHubDevice runs the GitHub device-code flow: show code modal + poll off-thread.
func (u *UI) wsGitHubDevice() {
	gh := u.svc.GitHub
	if gh == nil {
		return
	}
	u.toast("Starting GitHub link…")
	u.bg(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		da, err := gh.StartDevice(ctx)
		if err != nil {
			u.toast("Link failed: " + err.Error())
			u.logErr("github device", err)
			return
		}
		body := `<p class=ws-help>Open the activation page and enter this code, then approve in your browser:</p>` +
			`<div class=ws-devcode>` + html.EscapeString(da.UserCode) + `</div>` +
			btnRow(btn("Copy code", "ghost", "copy", da.UserCode), btn("Open activation page", "outline", "open-url", da.VerificationURI))
		u.openModal(modal("Link GitHub", body, ""))
		_ = openURL(da.VerificationURI)
		err = gh.PollDevice(ctx, da)
		u.closeModal()
		if err != nil {
			u.toast("Link failed: " + err.Error())
			return
		}
		u.toast("GitHub linked")
		u.wsPatchGitHub()
	})
}
