package webui

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/vrcperm"
	"rave.page/mate/internal/zigui"
)

// Worlds tab at parity with the Fyne buildWorldSync: link/setup hint, GitHub link control,
// permission lists (edit/publish/delete + friend/group-role pickers), per-target publish status
// (URL/copy/open-gist), poster/events/now-playing channels with publish toggles, and the Unity
// hand-off card. Status regions live under stable ids the "worlds" live tick patches;
// editors/pickers render as modals. User-supplied strings never go into data-act (fmt %q would
// mangle control chars / backslashes) - pickers index wsState.
//
// Zig-rendered (native/zigui/src/worlds.zig): the *State builders own everything impure (config,
// GitHub session, publish status, off-thread Unity inspect cache, federation memo); the *HTML
// renderers stay the Go fallback + golden reference (zigui_golden_worlds_test.go). The tick-patched
// fragments (#world-linkhint, #world-gh, #world-st-<key>, #world-unity-rows) each export their own
// renderer. The modal editors/pickers live in render_worlds_modals.go (same Zig treatment).

// ── resolved render state (JSON → Zig) ──
//
// Prose fields the Go renderer inserted as source literals (ws-help paragraphs, card titles, the
// add-list placeholder/submit label) stay UNESCAPED in both renderers - escaping them would change
// the DOM (they carry apostrophes). They are trusted literals resolved here, never user input.

// wsHintSt is a bare hint chip (#world-linkhint).
type wsHintSt struct {
	Tone string `json:"tone"`
	Text string `json:"text"`
}

// wsGitHubSt is the compact GitHub link control (#world-gh). Mode ∈ {unavailable,linked,unlinked}.
type wsGitHubSt struct {
	Mode         string `json:"mode"`
	Msg          string `json:"msg"` // hint text (unavailable/unlinked)
	LinkedLabel  string `json:"linkedLabel"`
	LinkedDL     string `json:"linkedDl"` // strings.ToLower(LinkedLabel)
	Login        string `json:"login"`
	LinkedHelp   string `json:"linkedHelp"`
	UnlinkLabel  string `json:"unlinkLabel"`
	UnlinkedHelp string `json:"unlinkedHelp"`
	DeviceLabel  string `json:"deviceLabel"`
	PatLabel     string `json:"patLabel"`
}

// wsStatusSt is one publish target's live status (#world-st-<key>).
type wsStatusSt struct {
	Tone      string `json:"tone"`
	Line      string `json:"line"`
	URL       string `json:"url"` // "" = nothing published yet (no URL block)
	CopyLabel string `json:"copyLabel"`
	OpenLabel string `json:"openLabel"`
	HTMLURL   string `json:"htmlUrl"` // "" = no open-gist button
}

// wsListRowSt is one permission list.
type wsListRowSt struct {
	Key     string     `json:"key"` // status-region key ("list:<id>"; emitted raw, Go parity)
	Name    string     `json:"name"`
	Entries string     `json:"entries"` // "N entries"
	EditAct string     `json:"editAct"`
	PubAct  string     `json:"pubAct"`
	DelAct  string     `json:"delAct"`
	Status  wsStatusSt `json:"status"`
}

// wsListsSt is the permission-lists card.
type wsListsSt struct {
	Help           string        `json:"help"`
	Empty          string        `json:"empty"` // "" = at least one list
	Rows           []wsListRowSt `json:"rows,omitempty"`
	EditLabel      string        `json:"editLabel"`
	PubLabel       string        `json:"pubLabel"`
	DelLabel       string        `json:"delLabel"`
	AddPlaceholder string        `json:"addPlaceholder"`
	AddLabel       string        `json:"addLabel"`
}

// wsPosterRowSt is one poster slot.
type wsPosterRowSt struct {
	Title   string `json:"title"` // "n. caption"
	Sub     string `json:"sub"`   // allowlist warning; "" = none
	EditAct string `json:"editAct"`
	DelAct  string `json:"delAct"`
}

// wsPostersSt is the poster-billboards card.
type wsPostersSt struct {
	CardTitle   string          `json:"cardTitle"`
	AddLabel    string          `json:"addLabel"`
	PubLabel    string          `json:"pubLabel"`
	ToggleLabel string          `json:"toggleLabel"`
	ToggleDL    string          `json:"toggleDl"`
	ToggleOn    bool            `json:"toggleOn"`
	Help        string          `json:"help"`
	Empty       string          `json:"empty"` // "" = at least one poster
	Rows        []wsPosterRowSt `json:"rows,omitempty"`
	EditLabel   string          `json:"editLabel"`
	DelLabel    string          `json:"delLabel"`
	Status      wsStatusSt      `json:"status"`
}

// wsEventsSt is the events-board card.
type wsEventsSt struct {
	CardTitle   string     `json:"cardTitle"`
	PubLabel    string     `json:"pubLabel"`
	ToggleLabel string     `json:"toggleLabel"`
	ToggleDL    string     `json:"toggleDl"`
	ToggleOn    bool       `json:"toggleOn"`
	Help        string     `json:"help"`
	Status      wsStatusSt `json:"status"`
}

// wsNowPlayingSt is the live DJ-card channel.
type wsNowPlayingSt struct {
	CardTitle   string     `json:"cardTitle"`
	PubLabel    string     `json:"pubLabel"`
	ToggleLabel string     `json:"toggleLabel"`
	ToggleDL    string     `json:"toggleDl"`
	ToggleOn    bool       `json:"toggleOn"`
	LinkLabel   string     `json:"linkLabel"`
	LinkDL      string     `json:"linkDl"`
	Link        string     `json:"link"`
	ImgLabel    string     `json:"imgLabel"`
	ImgDL       string     `json:"imgDl"`
	Img         string     `json:"img"`
	ImgWarn     string     `json:"imgWarn"` // "" = host allowlisted / empty
	Help        string     `json:"help"`
	Status      wsStatusSt `json:"status"`
}

// wsUnityRowSt is one valid Unity project.
type wsUnityRowSt struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	Act  string `json:"act"`
}

// wsUnitySt is the Unity hand-off rows (#world-unity-rows). Mode ∈ {empty,loading,rows}.
type wsUnitySt struct {
	Mode       string         `json:"mode"`
	Msg        string         `json:"msg"` // empty/loading text
	WriteLabel string         `json:"writeLabel"`
	Rows       []wsUnityRowSt `json:"rows,omitempty"`
}

// worldsState is the resolved render state for the Worlds tab.
type worldsState struct {
	Available   bool           `json:"available"`
	Title       string         `json:"title"`
	Sub         string         `json:"sub"`
	Unavailable string         `json:"unavailable"`
	LinkHint    wsHintSt       `json:"linkHint"`
	SecGitHub   string         `json:"secGitHub"`
	GH          wsGitHubSt     `json:"gh"`
	SecLists    string         `json:"secLists"`
	Lists       wsListsSt      `json:"lists"`
	SecPosters  string         `json:"secPosters"`
	Posters     wsPostersSt    `json:"posters"`
	SecEvents   string         `json:"secEvents"`
	Events      wsEventsSt     `json:"events"`
	SecNP       string         `json:"secNp"`
	NP          wsNowPlayingSt `json:"np"`
	SecUnity    string         `json:"secUnity"`
	UnityHelp   string         `json:"unityHelp"`
	Unity       wsUnitySt      `json:"unity"`
}

// ── bridges ──

// renderWorlds renders the Worlds tab (Zig when linked, Go otherwise).
func (u *UI) renderWorlds() string {
	st := u.worldsState()
	if zigui.Available() {
		if h, ok := zigui.RenderWorlds(stateJSON(st)); ok {
			return h
		}
	}
	return worldsHTML(st)
}

// worldsLinkHintInner reports what still needs linking (GitHub / VRChat) for full function.
func (u *UI) worldsLinkHintInner() string {
	st := u.worldsLinkHintState()
	if zigui.Available() {
		if h, ok := zigui.RenderWorldsLinkHint(stateJSON(st)); ok {
			return h
		}
	}
	return wsHintHTML(st)
}

// worldsGitHubInner is the compact GitHub link control (device-code / PAT / unlink).
func (u *UI) worldsGitHubInner() string {
	st := u.worldsGitHubState()
	if zigui.Available() {
		if h, ok := zigui.RenderWorldsGitHub(stateJSON(st)); ok {
			return h
		}
	}
	return wsGitHubHTML(st)
}

// wsStatusInner renders one target's last publish outcome + URL copy / open-gist actions.
func (u *UI) wsStatusInner(key, gistID, file string) string {
	st := u.wsStatusState(key, gistID, file)
	if zigui.Available() {
		if h, ok := zigui.RenderWorldsStatus(stateJSON(st)); ok {
			return h
		}
	}
	return wsStatusHTML(st)
}

// worldsUnityRowsInner lists valid Unity projects from the cached inspects.
func (u *UI) worldsUnityRowsInner() string {
	st := u.worldsUnityState()
	if zigui.Available() {
		if h, ok := zigui.RenderWorldsUnityRows(stateJSON(st)); ok {
			return h
		}
	}
	return wsUnityRowsHTML(st)
}

// ── state builders ──

// worldsState resolves config + GitHub session + publish status + Unity inspects into render state.
func (u *UI) worldsState() worldsState {
	st := worldsState{
		Available:   u.svc.WorldSync != nil,
		Title:       "Worlds",
		Sub:         "Feed VRChat worlds from gists - permission lists, poster billboards, events + a live now-playing card. Updated live, no world rebuild.",
		Unavailable: "World Sync unavailable",
		SecGitHub:   "GitHub",
		SecLists:    "Permission lists",
		SecPosters:  "Poster billboards",
		SecEvents:   "Upcoming events",
		SecNP:       "Now playing",
		SecUnity:    "Unity projects",
		UnityHelp:   "Writes Assets/rave.page/WorldSync/sources.json into the project. In Unity: Tools → rave.page → World Sync lists the feeds, wires a VideoTXL Remote Whitelist, or copies URLs. Re-write after publishing a new list.",
	}
	if !st.Available {
		return st
	}
	st.LinkHint = u.worldsLinkHintState()
	st.GH = u.worldsGitHubState()
	st.Lists = u.worldsListsState()
	st.Posters = u.worldsPostersState()
	st.Events = u.worldsEventsState()
	st.NP = u.worldsNowPlayingState()
	st.Unity = u.worldsUnityState()
	return st
}

// worldsLinkHintState resolves the link-status chip. VRChat counts as linked when a PAIRED
// instance serves it (federation) - reads the memo only; a cold memo kicks an off-thread probe
// and the 1 Hz tick repaints this hint.
func (u *UI) worldsLinkHintState() wsHintSt {
	var missing []string
	if u.svc.GitHub == nil || !u.svc.GitHub.SignedIn() {
		missing = append(missing, "GitHub")
	}
	viaPeer := ""
	if u.svc.Vrchat == nil || !u.svc.Vrchat.State().LoggedIn {
		if _, name, ok := u.wsVrcFedCached(); ok {
			viaPeer = name
		} else {
			u.wsVrcFedKick()
			missing = append(missing, "VRChat (friends browser + group-role expansion)")
		}
	}
	if len(missing) == 0 {
		if viaPeer != "" {
			return wsHintSt{Tone: "ok", Text: "All links connected - VRChat via peer " + viaPeer}
		}
		return wsHintSt{Tone: "ok", Text: "All links connected - ready to publish"}
	}
	return wsHintSt{Tone: "warn", Text: "Link missing: " + strings.Join(missing, " · ")}
}

// worldsGitHubState resolves the GitHub link control.
func (u *UI) worldsGitHubState() wsGitHubSt {
	gh := u.svc.GitHub
	st := wsGitHubSt{
		LinkedLabel: "Linked as",
		LinkedHelp:  "Token sealed at rest (gist scope). Publish targets below write to your gists.",
		UnlinkLabel: "Unlink",
		UnlinkedHelp: "Link a GitHub account (gist scope only). Device code needs an OAuth app client id; " +
			"pasting a classic PAT with 'gist' scope always works. Sealed at rest, never logged.",
		DeviceLabel: "Link GitHub (device code)",
		PatLabel:    "Paste token…",
	}
	st.LinkedDL = strings.ToLower(st.LinkedLabel)
	switch {
	case gh == nil:
		st.Mode, st.Msg = "unavailable", "GitHub integration unavailable in this build"
	case gh.SignedIn():
		st.Mode, st.Login = "linked", gh.Login()
	default:
		st.Mode, st.Msg = "unlinked", "GitHub not linked - needed to publish gists"
	}
	return st
}

// wsStatusState resolves one publish target's last outcome + raw URL.
func (u *UI) wsStatusState(key, gistID, file string) wsStatusSt {
	ws := u.svc.WorldSync
	st := wsStatusSt{Tone: "info", Line: "Not published yet.", CopyLabel: "Copy world URL", OpenLabel: "Open gist"}
	if ws == nil {
		return st
	}
	s := ws.Status(key)
	st.URL, st.HTMLURL = ws.RawURLFor(gistID, file), s.HTMLURL
	switch {
	case s.Err != "":
		st.Line, st.Tone = "Last publish: "+s.Err, "bad"
	case st.URL != "" && !s.When.IsZero():
		st.Line, st.Tone = "Published "+s.When.Format("15:04:05"), "ok"
	case st.URL != "":
		st.Line, st.Tone = "Ready", "ok"
	}
	if st.URL == "" {
		st.HTMLURL = "" // no URL block ⇒ no buttons at all
	}
	return st
}

// worldsListsState resolves the permission lists + their publish status.
func (u *UI) worldsListsState() wsListsSt {
	f := &u.svc.Cfg.Features.WorldSync
	st := wsListsSt{
		Help: "Each list publishes one gist (allow.txt newline names + allow.json envelope) worlds poll - " +
			"VideoTXL Remote Whitelist, ProTV, generic loaders. Group-role entries expand to current member " +
			"names at publish time (Udon has no runtime group API).",
		Rows:      []wsListRowSt{},
		EditLabel: "Edit", PubLabel: "Publish", DelLabel: "Delete",
		AddPlaceholder: "list name (e.g. VIP video control)", AddLabel: "Add list",
	}
	if len(f.Lists) == 0 {
		st.Empty = "No permission lists yet - add one below"
	}
	for i := range f.Lists {
		l := &f.Lists[i]
		st.Rows = append(st.Rows, wsListRowSt{
			Key:     "list:" + l.ID,
			Name:    l.Name,
			Entries: fmt.Sprintf("%d entries", len(l.Entries)),
			EditAct: "world-list-edit:" + l.ID,
			PubAct:  "ws-pub-list:" + l.Name,
			DelAct:  "world-list-del:" + l.ID,
			Status:  u.wsStatusState("list:"+l.ID, l.GistID, vrcperm.FileNames),
		})
	}
	return st
}

// worldsPostersState resolves the poster slots (+ image-host allowlist warnings).
func (u *UI) worldsPostersState() wsPostersSt {
	f := &u.svc.Cfg.Features.WorldSync
	st := wsPostersSt{
		CardTitle: "Billboards", AddLabel: "Add poster", PubLabel: "Publish now",
		ToggleLabel: "Publish", ToggleOn: f.PostersOn,
		Help: "Gist-fed image URL + caption + link for the poster prefab. VRChat images load through a " +
			"separate host allowlist (i.imgur.com, *.github.io, i.ibb.co, …) - non-allowlisted hosts show text only.",
		Rows:      []wsPosterRowSt{},
		EditLabel: "Edit", DelLabel: "Delete",
		Status: u.wsStatusState("posters", f.PostersGistID, vrcperm.FilePosters),
	}
	st.ToggleDL = strings.ToLower(st.ToggleLabel)
	if len(f.Posters) == 0 {
		st.Empty = "No posters yet"
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
		st.Rows = append(st.Rows, wsPosterRowSt{
			Title:   fmt.Sprintf("%d. %s", i+1, capt),
			Sub:     sub,
			EditAct: "world-poster-edit:" + strconv.Itoa(i),
			DelAct:  "world-poster-del:" + strconv.Itoa(i),
		})
	}
	return st
}

// worldsEventsState resolves the events-board channel.
func (u *UI) worldsEventsState() wsEventsSt {
	f := &u.svc.Cfg.Features.WorldSync
	st := wsEventsSt{
		CardTitle: "Events board", PubLabel: "Publish now",
		ToggleLabel: "Publish", ToggleOn: f.EventsOn,
		Help: "Publishes title + date of your upcoming rave.page events into a gist the events-board prefab " +
			"polls. Worlds see changes within the refresh interval + ~5 min gist CDN cache.",
		Status: u.wsStatusState("events", f.EventsGistID, vrcperm.FileEvents),
	}
	st.ToggleDL = strings.ToLower(st.ToggleLabel)
	return st
}

// worldsNowPlayingState resolves the live DJ-card channel.
func (u *UI) worldsNowPlayingState() wsNowPlayingSt {
	f := &u.svc.Cfg.Features.WorldSync
	st := wsNowPlayingSt{
		CardTitle: "Live DJ card", PubLabel: "Publish now",
		ToggleLabel: "Publish while live", ToggleOn: f.NowPlayingOn,
		LinkLabel: "Link", Link: f.NowPlayingLink,
		ImgLabel: "Image", Img: f.NowPlayingImg,
		Help: "While a session is live, publishes the audible track (artist/title from the session hub's " +
			"redacted output) at most once a minute. Worlds lag 1–6 min with the gist CDN cache.",
		Status: u.wsStatusState("nowplaying", f.NowPlayingGistID, vrcperm.FileNowPlaying),
	}
	st.ToggleDL = strings.ToLower(st.ToggleLabel)
	st.LinkDL, st.ImgDL = strings.ToLower(st.LinkLabel), strings.ToLower(st.ImgLabel)
	if f.NowPlayingImg != "" && !vrcperm.ImageHostAllowed(f.NowPlayingImg) {
		st.ImgWarn = "Image host not on VRChat's image allowlist"
	}
	return st
}

// worldsUnityState resolves the cached Unity inspects into write-target rows.
func (u *UI) worldsUnityState() wsUnitySt {
	st := wsUnitySt{
		Msg:        "No Unity projects configured (Settings ▸ Integrations ▸ Unity)",
		WriteLabel: "Write source URLs",
		Rows:       []wsUnityRowSt{},
	}
	projects := u.svc.Cfg.Features.Unity.Projects
	if len(projects) == 0 {
		st.Mode = "empty"
		return st
	}
	infos, ready := u.worldsUnityInspects(projects)
	if !ready {
		st.Mode, st.Msg = "loading", i18n.T("remote.loading")
		return st
	}
	for i, dir := range projects {
		if !infos[dir].Valid {
			continue
		}
		st.Rows = append(st.Rows, wsUnityRowSt{Name: infos[dir].Name, Dir: dir, Act: "world-unity-write:" + strconv.Itoa(i)})
	}
	if len(st.Rows) == 0 {
		st.Mode = "empty" // projects configured but none are valid Unity projects
		return st
	}
	st.Mode = "rows"
	return st
}

// ── pure renderers (golden reference; byte-identical to native/zigui/src/worlds.zig) ──

func worldsHTML(st worldsState) string {
	if !st.Available {
		return panel(st.Title, "") + emptyState(st.Unavailable)
	}
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	b.WriteString(`<div id=world-linkhint>` + wsHintHTML(st.LinkHint) + `</div>`)
	b.WriteString(section(st.SecGitHub, `<div id=world-gh>`+wsGitHubHTML(st.GH)+`</div>`))
	b.WriteString(section(st.SecLists, wsListsHTML(st.Lists)))
	b.WriteString(section(st.SecPosters, wsPostersHTML(st.Posters)))
	b.WriteString(section(st.SecEvents, wsEventsHTML(st.Events)))
	b.WriteString(section(st.SecNP, wsNowPlayingHTML(st.NP)))
	b.WriteString(section(st.SecUnity, wsUnityHTML(st)))
	return b.String()
}

func wsHintHTML(st wsHintSt) string { return hint(st.Tone, st.Text) }

func wsGitHubHTML(st wsGitHubSt) string {
	switch st.Mode {
	case "unavailable":
		return card("", "", hint("bad", st.Msg))
	case "linked":
		return card("", btnRow(btn(st.UnlinkLabel, "outline", "world-gh-unlink", "")),
			kvDL(st.LinkedLabel, st.LinkedDL, st.Login)+`<p class=ws-help>`+st.LinkedHelp+`</p>`)
	}
	body := hint("warn", st.Msg) +
		`<p class=ws-help>` + st.UnlinkedHelp + `</p>` +
		btnRow(btn(st.DeviceLabel, "primary", "world-gh-device", ""), btn(st.PatLabel, "outline", "world-gh-pat", ""))
	return card("", "", body)
}

// wsStatusRow wraps a target's live publish status under a stable id (patched by the tick).
func wsStatusRow(key string, st wsStatusSt) string {
	return `<div class=wsst id="world-st-` + key + `">` + wsStatusHTML(st) + `</div>`
}

func wsStatusHTML(st wsStatusSt) string {
	var b strings.Builder
	b.WriteString(`<div class=wsst-line>` + hint(st.Tone, st.Line) + `</div>`)
	if st.URL != "" {
		b.WriteString(`<div class=wsst-url>` + html.EscapeString(st.URL) + `</div>`)
		btns := []string{btn(st.CopyLabel, "ghost", "copy", st.URL)}
		if st.HTMLURL != "" {
			btns = append(btns, btn(st.OpenLabel, "outline", "open-url", st.HTMLURL))
		}
		b.WriteString(btnRow(btns...))
	}
	return b.String()
}

func wsListsHTML(st wsListsSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<p class=ws-help>` + st.Help + `</p>`)
	if st.Empty != "" {
		b.WriteString(emptyState(st.Empty))
	}
	for _, l := range st.Rows {
		trailing := btnRow(
			btn(st.EditLabel, "outline", l.EditAct, ""),
			btn(st.PubLabel, "explore", l.PubAct, ""),
			btn(st.DelLabel, "destructive", l.DelAct, ""),
		)
		b.WriteString(`<div class=ws-listrow>`)
		b.WriteString(itemRow(l.Name, l.Entries, trailing))
		b.WriteString(wsStatusRow(l.Key, l.Status))
		b.WriteString(`</div>`)
	}
	b.WriteString(`<form class=ws-addrow data-act=world-list-add>` +
		`<input class=field-input name=name placeholder="` + st.AddPlaceholder + `" autocomplete=off>` +
		`<button class="rp-btn rp-btn--primary" type=submit>` + st.AddLabel + `</button></form>`)
	b.WriteString(`</div>`)
	return b.String()
}

func wsPostersHTML(st wsPostersSt) string {
	var b strings.Builder
	trailing := btnRow(btn(st.AddLabel, "outline", "world-poster-add", ""), btn(st.PubLabel, "explore", "ws-pub-posters", ""))
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<div class=card-head><span class=card-h>` + st.CardTitle + `</span><span class=card-trail>` + trailing + `</span></div>`)
	b.WriteString(toggleRowDL(st.ToggleLabel, st.ToggleDL, "world-posters-on", st.ToggleOn))
	b.WriteString(`<p class=ws-help>` + st.Help + `</p>`)
	if st.Empty != "" {
		b.WriteString(emptyState(st.Empty))
	}
	for _, p := range st.Rows {
		trail := btnRow(btn(st.EditLabel, "outline", p.EditAct, ""), btn(st.DelLabel, "destructive", p.DelAct, ""))
		b.WriteString(itemRow(p.Title, p.Sub, trail))
	}
	b.WriteString(wsStatusRow("posters", st.Status))
	b.WriteString(`</div>`)
	return b.String()
}

func wsEventsHTML(st wsEventsSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<div class=card-head><span class=card-h>` + st.CardTitle + `</span><span class=card-trail>` +
		btn(st.PubLabel, "explore", "ws-pub-events", "") + `</span></div>`)
	b.WriteString(toggleRowDL(st.ToggleLabel, st.ToggleDL, "world-events-on", st.ToggleOn))
	b.WriteString(`<p class=ws-help>` + st.Help + `</p>`)
	b.WriteString(wsStatusRow("events", st.Status))
	b.WriteString(`</div>`)
	return b.String()
}

func wsNowPlayingHTML(st wsNowPlayingSt) string {
	var b strings.Builder
	b.WriteString(`<div class="rp-card">`)
	b.WriteString(`<div class=card-head><span class=card-h>` + st.CardTitle + `</span><span class=card-trail>` +
		btn(st.PubLabel, "explore", "ws-pub-nowplaying", "") + `</span></div>`)
	b.WriteString(toggleRowDL(st.ToggleLabel, st.ToggleDL, "world-np-on", st.ToggleOn))
	b.WriteString(fieldExDL(st.LinkLabel, st.LinkDL, "world-np-link", st.Link, "text", "", ""))
	b.WriteString(fieldExDL(st.ImgLabel, st.ImgDL, "world-np-img", st.Img, "text", "", ""))
	if st.ImgWarn != "" {
		b.WriteString(`<div class=wsst-line>` + hint("bad", st.ImgWarn) + `</div>`)
	}
	b.WriteString(`<p class=ws-help>` + st.Help + `</p>`)
	b.WriteString(wsStatusRow("nowplaying", st.Status))
	b.WriteString(`</div>`)
	return b.String()
}

func wsUnityHTML(st worldsState) string {
	return `<div class="rp-card"><p class=ws-help>` + st.UnityHelp + `</p>` +
		`<div id=world-unity-rows>` + wsUnityRowsHTML(st.Unity) + `</div></div>` // stable id: the async inspect cache re-patches it
}

func wsUnityRowsHTML(st wsUnitySt) string {
	if st.Mode != "rows" { // empty (none configured / none valid) or loading placeholder
		return emptyState(st.Msg)
	}
	var b strings.Builder
	for _, r := range st.Rows {
		b.WriteString(itemRow(r.Name, r.Dir, btn(st.WriteLabel, "explore", r.Act, "")))
	}
	return b.String()
}

// worldsUnityTTL re-inspects even when the project list is unchanged, so a project that becomes
// (in)valid on disk (Unity generating ProjectSettings, a moved/deleted dir) surfaces without editing
// the list - the sig-only key can't see on-disk state changes. Matches the sibling probe caches.
const worldsUnityTTL = 30 * time.Second

// worldsUnityCache holds off-thread unityproj.Inspect results keyed by a signature of the project
// list (any add/remove/reorder changes the sig → re-scan) plus a TTL for on-disk state changes.
// Published snapshot is immutable.
type worldsUnityCache struct {
	mu    sync.Mutex
	sig   string
	infos map[string]unityproj.Project
	at    time.Time
	ready bool
	busy  bool
}

// worldsUnityInspects returns cached inspects for the current project list, kicking an off-thread
// re-scan when the list changed (sig mismatch) or the cache is cold. The per-project os.Stat sweep
// runs in u.bg - never on the Worlds render goroutine.
func (u *UI) worldsUnityInspects(projects []string) (map[string]unityproj.Project, bool) {
	sig := strings.Join(projects, "\x00")
	u.wuCache.mu.Lock()
	sameSig := u.wuCache.ready && u.wuCache.sig == sig
	infos := u.wuCache.infos
	if sameSig && time.Since(u.wuCache.at) < worldsUnityTTL {
		u.wuCache.mu.Unlock()
		return infos, true
	}
	kick := !u.wuCache.busy
	if kick {
		u.wuCache.busy = true
	}
	u.wuCache.mu.Unlock()
	if kick {
		cp := append([]string(nil), projects...) // snapshot on the actWorker - config removal shifts the live slice in place
		u.bg(func() { u.refreshWorldsUnity(sig, cp) })
	}
	if sameSig { // TTL expired but same project list: serve the last snapshot while it refreshes (no flash)
		return infos, true
	}
	return nil, false // sig changed / cold: show loading until the scan lands
}

// refreshWorldsUnity inspects each project off-thread, publishes an immutable snapshot keyed by the
// project-list signature, and re-patches the Unity rows when Worlds is active.
func (u *UI) refreshWorldsUnity(sig string, projects []string) {
	infos := make(map[string]unityproj.Project, len(projects))
	for _, dir := range projects {
		infos[dir] = unityproj.Inspect(dir)
	}
	u.wuCache.mu.Lock()
	u.wuCache.sig, u.wuCache.infos, u.wuCache.at, u.wuCache.ready, u.wuCache.busy = sig, infos, time.Now(), true, false
	u.wuCache.mu.Unlock()
	if !u.stopped() && u.activeTab() == "worlds" {
		u.eval("window.__patch('world-unity-rows'," + jsQuote(u.worldsUnityRowsInner()) + ")")
	}
}
