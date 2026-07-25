//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Worlds golden gate: the Zig renderers must be BYTE-IDENTICAL to the Go renderers for
// representative states - full tab + every patched fragment (#world-linkhint, #world-gh,
// #world-st-<key>, #world-unity-rows).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// worldsFixtures: unavailable, empty (nothing linked/configured), populated, escaping edge,
// long values, unicode.
func worldsFixtures() map[string]worldsState {
	status := func(tone, line, url, htmlURL string) wsStatusSt {
		return wsStatusSt{Tone: tone, Line: line, URL: url, HTMLURL: htmlURL,
			CopyLabel: "Copy world URL", OpenLabel: "Open gist"}
	}
	base := func() worldsState {
		return worldsState{
			Available: true, Title: "Worlds",
			Sub:         "Feed VRChat worlds from gists - permission lists, poster billboards, events + a live now-playing card. Updated live, no world rebuild.",
			Unavailable: "World Sync unavailable",
			LinkHint:    wsHintSt{Tone: "warn", Text: "Link missing: GitHub"},
			SecGitHub:   "GitHub",
			GH: wsGitHubSt{Mode: "unlinked", Msg: "GitHub not linked - needed to publish gists",
				LinkedLabel: "Linked as", LinkedDL: "linked as",
				LinkedHelp:   "Token sealed at rest (gist scope). Publish targets below write to your gists.",
				UnlinkLabel:  "Unlink",
				UnlinkedHelp: "Link a GitHub account (gist scope only). Device code needs an OAuth app client id; pasting a classic PAT with 'gist' scope always works. Sealed at rest, never logged.",
				DeviceLabel:  "Link GitHub (device code)", PatLabel: "Paste token…"},
			SecLists: "Permission lists",
			Lists: wsListsSt{
				Help:  "Each list publishes one gist (allow.txt newline names + allow.json envelope) worlds poll - VideoTXL Remote Whitelist, ProTV, generic loaders. Group-role entries expand to current member names at publish time (Udon has no runtime group API).",
				Empty: "No permission lists yet - add one below", Rows: []wsListRowSt{},
				EditLabel: "Edit", PubLabel: "Publish", DelLabel: "Delete",
				AddPlaceholder: "list name (e.g. VIP video control)", AddLabel: "Add list",
			},
			SecPosters: "Poster billboards",
			Posters: wsPostersSt{
				CardTitle: "Billboards", AddLabel: "Add poster", PubLabel: "Publish now",
				ToggleLabel: "Publish", ToggleDL: "publish", ToggleOn: false,
				Help:  "Gist-fed image URL + caption + link for the poster prefab. VRChat images load through a separate host allowlist (i.imgur.com, *.github.io, i.ibb.co, …) - non-allowlisted hosts show text only.",
				Empty: "No posters yet", Rows: []wsPosterRowSt{},
				EditLabel: "Edit", DelLabel: "Delete",
				Status: status("info", "Not published yet.", "", ""),
			},
			SecEvents: "Upcoming events",
			Events: wsEventsSt{
				CardTitle: "Events board", PubLabel: "Publish now",
				ToggleLabel: "Publish", ToggleDL: "publish",
				Help:   "Publishes title + date of your upcoming rave.page events into a gist the events-board prefab polls. Worlds see changes within the refresh interval + ~5 min gist CDN cache.",
				Status: status("info", "Not published yet.", "", ""),
			},
			SecNP: "Now playing",
			NP: wsNowPlayingSt{
				CardTitle: "Live DJ card", PubLabel: "Publish now",
				ToggleLabel: "Publish while live", ToggleDL: "publish while live",
				LinkLabel: "Link", LinkDL: "link", ImgLabel: "Image", ImgDL: "image",
				Help:   "While a session is live, publishes the audible track (artist/title from the session hub's redacted output) at most once a minute. Worlds lag 1–6 min with the gist CDN cache.",
				Status: status("info", "Not published yet.", "", ""),
			},
			SecUnity:  "Unity projects",
			UnityHelp: "Writes Assets/rave.page/WorldSync/sources.json into the project. In Unity: Tools → rave.page → World Sync lists the feeds, wires a VideoTXL Remote Whitelist, or copies URLs. Re-write after publishing a new list.",
			Unity: wsUnitySt{Mode: "empty", Msg: "No Unity projects configured (Settings ▸ Integrations ▸ Unity)",
				WriteLabel: "Write source URLs", Rows: []wsUnityRowSt{}},
		}
	}

	unavailable := base()
	unavailable.Available = false

	empty := base()

	populated := base()
	populated.LinkHint = wsHintSt{Tone: "ok", Text: "All links connected - VRChat via peer studio-pc"}
	populated.GH.Mode, populated.GH.Login, populated.GH.Msg = "linked", "dymattic", ""
	populated.Lists.Empty = ""
	populated.Lists.Rows = []wsListRowSt{
		{Key: "list:l1", Name: "VIP video control", Entries: "12 entries",
			EditAct: "world-list-edit:l1", PubAct: "ws-pub-list:VIP video control", DelAct: "world-list-del:l1",
			Status: status("ok", "Published 21:15:03", "https://gist.githubusercontent.com/raw/allow.txt", "https://gist.github.com/abc")},
		{Key: "list:l2", Name: "Crew", Entries: "0 entries",
			EditAct: "world-list-edit:l2", PubAct: "ws-pub-list:Crew", DelAct: "world-list-del:l2",
			Status: status("bad", "Last publish: 403 forbidden", "", "")},
	}
	populated.Posters.ToggleOn = true
	populated.Posters.Empty = ""
	populated.Posters.Rows = []wsPosterRowSt{
		{Title: "1. Friday lineup", Sub: "", EditAct: "world-poster-edit:0", DelAct: "world-poster-del:0"},
		{Title: "2. https://example.com/x.png", Sub: "⚠ image host not VRC-allowlisted",
			EditAct: "world-poster-edit:1", DelAct: "world-poster-del:1"},
	}
	populated.Posters.Status = status("ok", "Ready", "https://gist.githubusercontent.com/raw/posters.json", "")
	populated.Events.ToggleOn = true
	populated.NP.ToggleOn = true
	populated.NP.Link = "https://rave.page/dymattic"
	populated.NP.Img = "https://i.imgur.com/a.png"
	populated.Unity = wsUnitySt{Mode: "rows", WriteLabel: "Write source URLs",
		Rows: []wsUnityRowSt{
			{Name: "RaveWorld", Dir: `C:\Unity\RaveWorld`, Act: "world-unity-write:0"},
			{Name: "Test", Dir: `D:\proj\Test`, Act: "world-unity-write:2"},
		}}

	escaping := base()
	escaping.Title = `W&orlds <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Unavailable = `un&avail<">`
	escaping.LinkHint = wsHintSt{Tone: "warn", Text: `Link missing: G&itHub <"x">`}
	escaping.SecGitHub = `Git&Hub<">`
	escaping.SecLists = `Lists&<">`
	escaping.SecPosters = `Posters&<">`
	escaping.SecEvents = `Events&<">`
	escaping.SecNP = `NP&<">`
	escaping.SecUnity = `Unity&<">`
	escaping.GH.Mode, escaping.GH.Login = "linked", `dj&"<>'`
	escaping.Lists.Empty = ""
	escaping.Lists.Rows = []wsListRowSt{
		{Key: "list:l&1", Name: `L&"ist'<>`, Entries: "3 entries",
			EditAct: `world-list-edit:l&1`, PubAct: `ws-pub-list:L&"ist'<>`, DelAct: `world-list-del:l&1`,
			Status: status("ok", `Published & <21:15>`, `https://raw/x?a=1&b="2"`, `https://gist/abc?a=1&b=2`)},
	}
	escaping.Posters.Empty = ""
	escaping.Posters.Rows = []wsPosterRowSt{
		{Title: `1. c&"apt'<>`, Sub: `⚠ h&"ost'<>`, EditAct: "world-poster-edit:0", DelAct: "world-poster-del:0"},
	}
	escaping.NP.Link = `https://rave.page/?a=1&b="2"`
	escaping.NP.Img = `https://x/y?a=1&b='2'`
	escaping.NP.ImgWarn = `Image host not on VRChat's image allowlist`
	escaping.Unity = wsUnitySt{Mode: "rows", WriteLabel: `W&"rite'<>`,
		Rows: []wsUnityRowSt{{Name: `P&"roj'<>`, Dir: `C:\p&"roj'<>`, Act: "world-unity-write:0"}}}

	long := base()
	longS := strings.Repeat("very-long-", 120)
	long.LinkHint = wsHintSt{Tone: "warn", Text: "Link missing: " + longS}
	long.Lists.Empty = ""
	long.Lists.Rows = []wsListRowSt{{Key: "list:" + strings.Repeat("i", 200), Name: longS, Entries: "99999 entries",
		EditAct: "world-list-edit:" + strings.Repeat("i", 200), PubAct: "ws-pub-list:" + longS,
		DelAct: "world-list-del:" + strings.Repeat("i", 200),
		Status: status("ok", "Published 00:00:00", "https://raw/"+longS, "https://gist/"+longS)}}
	long.NP.Link = longS
	long.NP.Img = longS
	long.Unity = wsUnitySt{Mode: "rows", WriteLabel: "Write source URLs",
		Rows: []wsUnityRowSt{{Name: longS, Dir: strings.Repeat("d", 400), Act: "world-unity-write:999"}}}

	unicode := base()
	unicode.Title = "ワールド 🌐"
	unicode.LinkHint = wsHintSt{Tone: "ok", Text: "Все ссылки подключены"}
	unicode.GH.Mode, unicode.GH.Login = "linked", "диджей"
	unicode.Lists.Empty = ""
	unicode.Lists.Rows = []wsListRowSt{{Key: "list:l1", Name: "許可リスト 中文", Entries: "3 entries",
		EditAct: "world-list-edit:l1", PubAct: "ws-pub-list:許可リスト 中文", DelAct: "world-list-del:l1",
		Status: status("ok", "Опубликовано 21:15:03", "https://raw/ワールド.txt", "")}}
	unicode.Posters.Empty = ""
	unicode.Posters.Rows = []wsPosterRowSt{{Title: "1. ポスター 🎞️", Sub: "⚠ ホストが許可されていません",
		EditAct: "world-poster-edit:0", DelAct: "world-poster-del:0"}}
	unicode.NP.Link = "https://rave.page/ラヴ"
	unicode.Unity = wsUnitySt{Mode: "loading", Msg: "Загрузка…", WriteLabel: "Write source URLs", Rows: []wsUnityRowSt{}}

	ghUnavailable := base()
	ghUnavailable.GH = wsGitHubSt{Mode: "unavailable", Msg: "GitHub integration unavailable in this build"}

	return map[string]worldsState{
		"unavailable":   unavailable,
		"empty":         empty,
		"populated":     populated,
		"escaping":      escaping,
		"long":          long,
		"unicode":       unicode,
		"ghUnavailable": ghUnavailable,
	}
}

func TestZigWorldsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range worldsFixtures() {
		t.Run(name, func(t *testing.T) {
			assertZigEqual(t, "full", worldsHTML(st), stateJSON(st), zigui.RenderWorlds)
			assertZigEqual(t, "linkhint", wsHintHTML(st.LinkHint), stateJSON(st.LinkHint), zigui.RenderWorldsLinkHint)
			assertZigEqual(t, "github", wsGitHubHTML(st.GH), stateJSON(st.GH), zigui.RenderWorldsGitHub)
			assertZigEqual(t, "unityrows", wsUnityRowsHTML(st.Unity), stateJSON(st.Unity), zigui.RenderWorldsUnityRows)
			for _, s := range []wsStatusSt{st.Posters.Status, st.Events.Status, st.NP.Status} {
				assertZigEqual(t, "status", wsStatusHTML(s), stateJSON(s), zigui.RenderWorldsStatus)
			}
			for _, l := range st.Lists.Rows {
				assertZigEqual(t, "status:list", wsStatusHTML(l.Status), stateJSON(l.Status), zigui.RenderWorldsStatus)
			}
		})
	}
}
