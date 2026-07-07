package webui

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/vrcperm"
)

// renderWorlds is the Worlds tab at parity with the Fyne buildWorldSync: link/setup hint,
// GitHub link control, permission lists (edit/publish/delete + friend/group-role pickers),
// per-target publish status (URL/copy/open-gist), poster/events/now-playing channels with
// publish toggles, and the Unity hand-off card. Status regions live under stable ids the
// "worlds" live tick patches; editors/pickers render as modals. User-supplied strings never
// go into data-act (fmt %q would mangle control chars / backslashes) - pickers index wsState.
func (u *UI) renderWorlds() string {
	if u.svc.WorldSync == nil {
		return panel("Worlds", "") + emptyState("World Sync unavailable")
	}
	var b strings.Builder
	b.WriteString(panel("Worlds", "Feed VRChat worlds from gists - permission lists, poster billboards, events + a live now-playing card. Updated live, no world rebuild."))
	b.WriteString(`<div id=world-linkhint>` + u.worldsLinkHintInner() + `</div>`)
	b.WriteString(section("GitHub", `<div id=world-gh>`+u.worldsGitHubInner()+`</div>`))
	b.WriteString(section("Permission lists", u.worldsListsCard()))
	b.WriteString(section("Poster billboards", u.worldsPostersCard()))
	b.WriteString(section("Upcoming events", u.worldsEventsCard()))
	b.WriteString(section("Now playing", u.worldsNowPlayingCard()))
	b.WriteString(section("Unity projects", u.worldsUnityCard()))
	return b.String()
}

// worldsLinkHintInner reports what still needs linking (GitHub / VRChat) for full function.
func (u *UI) worldsLinkHintInner() string {
	var missing []string
	if u.svc.GitHub == nil || !u.svc.GitHub.SignedIn() {
		missing = append(missing, "GitHub")
	}
	if u.svc.Vrchat == nil || !u.svc.Vrchat.State().LoggedIn {
		missing = append(missing, "VRChat (friends browser + group-role expansion)")
	}
	if len(missing) == 0 {
		return hint("ok", "All links connected - ready to publish")
	}
	return hint("warn", "Link missing: "+strings.Join(missing, " · "))
}

// worldsGitHubInner is the compact GitHub link control (device-code / PAT / unlink).
func (u *UI) worldsGitHubInner() string {
	gh := u.svc.GitHub
	if gh == nil {
		return card("", "", hint("bad", "GitHub integration unavailable in this build"))
	}
	if gh.SignedIn() {
		return card("", btnRow(btn("Unlink", "outline", "world-gh-unlink", "")),
			kv("Linked as", gh.Login())+`<p class=ws-help>Token sealed at rest (gist scope). Publish targets below write to your gists.</p>`)
	}
	body := hint("warn", "GitHub not linked - needed to publish gists") +
		`<p class=ws-help>Link a GitHub account (gist scope only). Device code needs an OAuth app client id; pasting a classic PAT with 'gist' scope always works. Sealed at rest, never logged.</p>` +
		btnRow(btn("Link GitHub (device code)", "primary", "world-gh-device", ""), btn("Paste token…", "outline", "world-gh-pat", ""))
	return card("", "", body)
}

// ── permission lists ──

func (u *UI) worldsListsCard() string {
	f := &u.svc.Cfg.Features.WorldSync
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<p class=ws-help>Each list publishes one gist (allow.txt newline names + allow.json envelope) worlds poll - VideoTXL Remote Whitelist, ProTV, generic loaders. Group-role entries expand to current member names at publish time (Udon has no runtime group API).</p>`)
	if len(f.Lists) == 0 {
		b.WriteString(emptyState("No permission lists yet - add one below"))
	}
	for i := range f.Lists {
		l := &f.Lists[i]
		trailing := btnRow(
			btn("Edit", "outline", "world-list-edit:"+l.ID, ""),
			btn("Publish", "explore", "ws-pub-list:"+l.Name, ""),
			btn("Delete", "destructive", "world-list-del:"+l.ID, ""),
		)
		b.WriteString(`<div class=ws-listrow>`)
		b.WriteString(itemRow(l.Name, fmt.Sprintf("%d entries", len(l.Entries)), trailing))
		b.WriteString(u.wsStatusRow("list:"+l.ID, l.GistID, vrcperm.FileNames))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<form class=ws-addrow data-act=world-list-add>` +
		`<input class=field-input name=name placeholder="list name (e.g. VIP video control)" autocomplete=off>` +
		`<button class="rp-btn rp-btn--primary" type=submit>Add list</button></form>`)
	b.WriteString(`</div>`)
	return b.String()
}

// wsStatusRow wraps a target's live publish status under a stable id (patched by the tick).
func (u *UI) wsStatusRow(key, gistID, file string) string {
	return `<div class=wsst id="world-st-` + key + `">` + u.wsStatusInner(key, gistID, file) + `</div>`
}

// wsStatusInner renders one target's last publish outcome + URL copy / open-gist actions.
func (u *UI) wsStatusInner(key, gistID, file string) string {
	ws := u.svc.WorldSync
	st := ws.Status(key)
	url := ws.RawURLFor(gistID, file)
	line, tone := "Not published yet.", "info"
	switch {
	case st.Err != "":
		line, tone = "Last publish: "+st.Err, "bad"
	case url != "" && !st.When.IsZero():
		line, tone = "Published "+st.When.Format("15:04:05"), "ok"
	case url != "":
		line, tone = "Ready", "ok"
	}
	var b strings.Builder
	b.WriteString(`<div class=wsst-line>` + hint(tone, line) + `</div>`)
	if url != "" {
		b.WriteString(`<div class=wsst-url>` + html.EscapeString(url) + `</div>`)
		btns := []string{btn("Copy world URL", "ghost", "copy", url)}
		if st.HTMLURL != "" {
			btns = append(btns, btn("Open gist", "outline", "open-url", st.HTMLURL))
		}
		b.WriteString(btnRow(btns...))
	}
	return b.String()
}

// ── poster billboards ──

func (u *UI) worldsPostersCard() string {
	f := &u.svc.Cfg.Features.WorldSync
	var b strings.Builder
	trailing := btnRow(btn("Add poster", "outline", "world-poster-add", ""), btn("Publish now", "explore", "ws-pub-posters", ""))
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<div class=card-head><span class=card-h>Billboards</span><span class=card-trail>` + trailing + `</span></div>`)
	b.WriteString(toggleRow("Publish", "world-posters-on", f.PostersOn))
	b.WriteString(`<p class=ws-help>Gist-fed image URL + caption + link for the poster prefab. VRChat images load through a separate host allowlist (i.imgur.com, *.github.io, i.ibb.co, …) - non-allowlisted hosts show text only.</p>`)
	if len(f.Posters) == 0 {
		b.WriteString(emptyState("No posters yet"))
	}
	for i := range f.Posters {
		p := f.Posters[i]
		capt := p.Caption
		if capt == "" {
			capt = p.Img
		}
		if capt == "" {
			capt = "(empty)"
		}
		sub := ""
		if p.Img != "" && !vrcperm.ImageHostAllowed(p.Img) {
			sub = "⚠ image host not VRC-allowlisted"
		}
		trail := btnRow(btn("Edit", "outline", "world-poster-edit:"+strconv.Itoa(i), ""), btn("Delete", "destructive", "world-poster-del:"+strconv.Itoa(i), ""))
		b.WriteString(itemRow(fmt.Sprintf("%d. %s", i+1, capt), sub, trail))
	}
	b.WriteString(u.wsStatusRow("posters", f.PostersGistID, vrcperm.FilePosters))
	b.WriteString(`</div>`)
	return b.String()
}

// ── upcoming events ──

func (u *UI) worldsEventsCard() string {
	f := &u.svc.Cfg.Features.WorldSync
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<div class=card-head><span class=card-h>Events board</span><span class=card-trail>` + btn("Publish now", "explore", "ws-pub-events", "") + `</span></div>`)
	b.WriteString(toggleRow("Publish", "world-events-on", f.EventsOn))
	b.WriteString(`<p class=ws-help>Publishes title + date of your upcoming rave.page events into a gist the events-board prefab polls. Worlds see changes within the refresh interval + ~5 min gist CDN cache.</p>`)
	b.WriteString(u.wsStatusRow("events", f.EventsGistID, vrcperm.FileEvents))
	b.WriteString(`</div>`)
	return b.String()
}

// ── now playing ──

func (u *UI) worldsNowPlayingCard() string {
	f := &u.svc.Cfg.Features.WorldSync
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<div class=card-head><span class=card-h>Live DJ card</span><span class=card-trail>` + btn("Publish now", "explore", "ws-pub-nowplaying", "") + `</span></div>`)
	b.WriteString(toggleRow("Publish while live", "world-np-on", f.NowPlayingOn))
	b.WriteString(field("Link", "world-np-link", f.NowPlayingLink, "text"))
	b.WriteString(field("Image", "world-np-img", f.NowPlayingImg, "text"))
	if f.NowPlayingImg != "" && !vrcperm.ImageHostAllowed(f.NowPlayingImg) {
		b.WriteString(`<div class=wsst-line>` + hint("bad", "Image host not on VRChat's image allowlist") + `</div>`)
	}
	b.WriteString(`<p class=ws-help>While a session is live, publishes the audible track (artist/title from the session hub's redacted output) at most once a minute. Worlds lag 1–6 min with the gist CDN cache.</p>`)
	b.WriteString(u.wsStatusRow("nowplaying", f.NowPlayingGistID, vrcperm.FileNowPlaying))
	b.WriteString(`</div>`)
	return b.String()
}

// ── Unity hand-off ──

func (u *UI) worldsUnityCard() string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<p class=ws-help>Writes Assets/rave.page/WorldSync/sources.json into the project. In Unity: Tools → rave.page → World Sync lists the feeds, wires a VideoTXL Remote Whitelist, or copies URLs. Re-write after publishing a new list.</p>`)
	rows := u.worldsUnityRows()
	if rows == "" {
		b.WriteString(emptyState("No Unity projects configured (Settings ▸ Integrations ▸ Unity)"))
	} else {
		b.WriteString(rows)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// worldsUnityRows lists valid Unity projects (write action carries the project INDEX, not the
// path - a Windows path would break fmt %q escaping in data-act).
func (u *UI) worldsUnityRows() string {
	var b strings.Builder
	for i, dir := range u.svc.Cfg.Features.Unity.Projects {
		info := unityproj.Inspect(dir)
		if !info.Valid {
			continue
		}
		b.WriteString(itemRow(info.Name, dir, btn("Write source URLs", "explore", "world-unity-write:"+strconv.Itoa(i), "")))
	}
	return b.String()
}

// ── modal editors (rendered into __modal via openModal) ──

// wsListEditorHTML builds the per-list entry editor (delete + add-name + friend/role pickers).
// Records the edited list id in wsState so index-based entry actions resolve it.
func (u *UI) wsListEditorHTML(l *config.PermList) string {
	wsState.mu.Lock()
	wsState.editList = l.ID
	wsState.mu.Unlock()

	var body strings.Builder
	body.WriteString(`<p class=ws-help>Role grants publish that role's member names to the gist (unlisted but public URL). Only whole-group/role member names are listed - never user ids.</p>`)
	body.WriteString(`<div class=ws-entries>`)
	if len(l.Entries) == 0 {
		body.WriteString(emptyState("Empty list - add friends or group roles"))
	}
	for i := range l.Entries {
		e := l.Entries[i]
		label := "User: " + e.Display
		if e.Kind == config.PermEntryGroupRole {
			role := e.RoleName
			if role == "" {
				role = "all members"
			}
			label = fmt.Sprintf("Group role: %s - %s", e.GroupName, role)
		}
		body.WriteString(itemRow(label, "", btn("Delete", "destructive", "world-ent-del:"+strconv.Itoa(i), "")))
	}
	body.WriteString(`</div>`)
	body.WriteString(`<form class=ws-addrow data-act=world-name-add>` +
		`<input class=field-input name=name placeholder="exact VRChat display name" autocomplete=off>` +
		`<button class="rp-btn rp-btn--outline" type=submit>Add name</button></form>`)
	body.WriteString(btnRow(
		btn("Add friend…", "primary", "world-friends:"+l.ID, ""),
		btn("Add group role…", "outline", "world-groups:"+l.ID, ""),
	))
	return modal("Edit list: "+l.Name, body.String(), "")
}

// wsPosterEditorHTML builds the poster-slot editor form.
func (u *UI) wsPosterEditorHTML(idx int, p config.WorldPoster) string {
	warn := ""
	if p.Img != "" && !vrcperm.ImageHostAllowed(p.Img) {
		warn = `<div class=wsst-line>` + hint("bad", "Host not on VRChat's image allowlist - prefab shows text only") + `</div>`
	}
	body := `<form data-act=world-poster-save>` +
		`<input type=hidden name=idx value="` + strconv.Itoa(idx) + `">` +
		`<label class=field><span class=field-label>Image</span><input class=field-input name=img value="` + html.EscapeString(p.Img) + `" placeholder="https://i.imgur.com/… (VRC image-allowlisted host)" autocomplete=off></label>` +
		`<label class=field><span class=field-label>Caption</span><input class=field-input name=caption value="` + html.EscapeString(p.Caption) + `" placeholder="caption" autocomplete=off></label>` +
		`<label class=field><span class=field-label>Link</span><input class=field-input name=link value="` + html.EscapeString(p.Link) + `" placeholder="https://rave.page/… (shown as text/QR)" autocomplete=off></label>` +
		warn +
		`<div class=btn-row><button class="rp-btn rp-btn--primary" type=submit>Save</button></div></form>`
	return modal("Edit poster", body, "")
}

// wsFriendPickerHTML is the friend-picker modal shell (list filled async by the handler).
func (u *UI) wsFriendPickerHTML(listID string) string {
	body := `<form class=ws-search data-act=world-fr-search><input class=field-input name=q placeholder="filter friends…" autocomplete=off></form>` +
		`<div class=ws-picklist id=world-fr-list>` + u.wsFriendListHTML() + `</div>`
	return modal("Add friend", body, btn("Back to list", "outline", "world-list-edit:"+listID, ""))
}

// wsFriendListHTML renders the loaded (filtered) friends into pick rows. The pick action carries
// the friend's index into wsState.friends (stable across filtering - never the display name).
func (u *UI) wsFriendListHTML() string {
	wsState.mu.Lock()
	friends := wsState.friends
	q := strings.ToLower(strings.TrimSpace(wsState.fq))
	loading := wsState.friendsLoading
	wsState.mu.Unlock()
	if loading {
		return `<p class=ws-help>Loading friends…</p>`
	}
	var b strings.Builder
	shown := 0
	for i, fr := range friends {
		if q != "" && !strings.Contains(strings.ToLower(fr.DisplayName), q) {
			continue
		}
		if shown >= 60 {
			b.WriteString(`<p class=ws-help>… refine the filter to see more</p>`)
			break
		}
		shown++
		b.WriteString(itemRow(fr.DisplayName, "", btn("Add", "primary", "world-fr-pick:"+strconv.Itoa(i), "")))
	}
	if shown == 0 {
		if len(friends) == 0 {
			b.WriteString(emptyState("No friends found"))
		} else {
			b.WriteString(emptyState("No match"))
		}
	}
	return b.String()
}

// wsGroupPickerHTML is the group/role-picker modal shell.
func (u *UI) wsGroupPickerHTML(listID string) string {
	body := `<form class=ws-search data-act=world-grp-search><input class=field-input name=q placeholder="search all groups…" autocomplete=off><button class="rp-btn rp-btn--outline" type=submit>Search</button></form>` +
		`<div class=ws-picklist id=world-grp-list>` + u.wsGroupListHTML() + `</div>` +
		`<p class=ws-help>Grant a whole group or a role. Member expansion only works where the member list is visible (public groups); private groups keep their last good expansion.</p>`
	return modal("Add group role", body, btn("Back to list", "outline", "world-list-edit:"+listID, ""))
}

// wsGroupListHTML renders favorites + your groups + search results as pick rows, and records the
// flattened display order in wsState.pickGroups so fav/roles actions index it (group names may
// carry chars fmt %q would mangle in data-act).
func (u *UI) wsGroupListHTML() string {
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

	var b strings.Builder
	var refs []groupRef
	emit := func(id, name string, members int) {
		idx := len(refs)
		refs = append(refs, groupRef{ID: id, Name: name})
		favLabel := "☆ Pin"
		if isFav(id) {
			favLabel = "★ Unpin"
		}
		lbl := name
		if members > 0 {
			lbl = fmt.Sprintf("%s (%d members)", name, members)
		}
		trail := btnRow(
			btn(favLabel, "ghost", "world-fav:"+strconv.Itoa(idx), ""),
			btn("Roles…", "primary", "world-roles:"+strconv.Itoa(idx), ""),
		)
		b.WriteString(itemRow(lbl, "", trail))
	}

	if loading {
		b.WriteString(`<p class=ws-help>Loading your groups…</p>`)
	}
	if len(f.FavoriteGroups) > 0 {
		b.WriteString(`<div class=ws-caps>Favorites</div>`)
		for _, g := range f.FavoriteGroups {
			emit(g.ID, g.Name, 0)
		}
	}
	if len(mine) > 0 {
		b.WriteString(`<div class=ws-caps>Your groups</div>`)
		for _, g := range mine {
			if !isFav(g.EffectiveID()) {
				emit(g.EffectiveID(), g.Name, g.MemberCount)
			}
		}
	}
	if len(results) > 0 {
		b.WriteString(`<div class=ws-caps>Search results</div>`)
		for _, g := range results {
			if !isFav(g.EffectiveID()) {
				emit(g.EffectiveID(), g.Name, g.MemberCount)
			}
		}
	}
	if !loading && len(refs) == 0 {
		b.WriteString(emptyState("No groups - search above"))
	}

	wsState.mu.Lock()
	wsState.pickGroups = refs
	wsState.mu.Unlock()
	return b.String()
}
