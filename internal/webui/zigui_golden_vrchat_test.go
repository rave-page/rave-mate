//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// VRChat golden gate: the Zig renderers must be BYTE-IDENTICAL to the Go renderers for
// representative states - full tab + every patched fragment (#vrc-status-region, #vrc-editor,
// #vrc-campaths, #vrc-photos-body) and the Groups sub-view (#vrcg-body).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// vrcFixtures: unavailable, empty (signed out), populated, escaping edge, long values, unicode.
func vrcFixtures() map[string]vrcTabSt {
	base := func() vrcTabSt {
		return vrcTabSt{
			Available: true, Title: "VRChat", Sub: "Presence, groups, camera paths",
			Unavailable: "VRChat unavailable",
			Status: vrcStatusSt{Present: true, Variant: "muted", Label: "VRChat", DL: "vrchat",
				Line: "Not signed in"},
			SubActive:    "profile",
			SubTabs:      []vgTabSt{{"profile", "Profile"}, {"groups", "Groups"}},
			Groups:       vrcgFixtureState(),
			SecStatusBio: "Status & bio", SignInHint: "Sign in to edit your profile",
			Editor:    vrcEditorFixture(),
			SecEmotes: "Animated emoji", Emotes: vrcEmotesFixture(),
			SecCamPaths: "Camera paths", SecPhotos: "Screenshots",
			CamPaths: vrcCampathsSt{State: "loading", Msg: "Loading…"},
			Photos:   vrcPhotosSt{State: "loading", Msg: "Loading…"},
		}
	}
	unavailable := base()
	unavailable.Available = false

	empty := base() // signed out: no editor, no tools

	populated := base()
	populated.LoggedIn = true
	populated.HasTools = true
	populated.Status = vrcStatusSt{Present: true, Variant: "success", Label: "Signed in as dymattic",
		DL: "signed in as dymattic", Line: "Pipeline live"}
	populated.CamPaths = vrcCampathsSt{
		State: "detail",
		Items: []vrcPathItemSt{
			{Idx: 0, Label: "Club: sweep (12 pts, 30s)", Active: true},
			{Idx: 1, Label: "Player-relative: orbit (4 pts, 8s)"},
		},
		SVG:     `<div id="cpv-vrc" class=cpv-view><svg class=cpv-svg><g id="cpv-vrc-geo"></g></svg></div>`,
		PlayBtn: `<span id="cpv-vrc-play"><button class="rp-btn rp-btn--go" data-act="cpv-play:vrc">▶ Play path</button></span>`,
		Name:    "sweep", Info: "in Club · 12 keyframes · 30.0s · saved 2026-07-24 21:15",
		Load: "Load into VRChat", Copy: "Copy file path", CopyPath: `C:\Users\dj\Pictures\VRChat\CamPaths\sweep.json`,
		Organize: "Organize now", Hint: "Camera paths load from VRChat's CamPaths folder.",
	}
	populated.Photos = vrcPhotosSt{
		State: "detail",
		Groups: []vrcPhotoGrpSt{
			{Label: "All Photos", Count: 42, Active: true},
			{Label: "2026-07", Count: 12},
		},
		Cells: []vrcPhotoCellSt{
			{File: `C:\pics\a.png`, TitleQ: `"a.png"`, Label: "2026-07", Src: "http://127.0.0.1:47700/img/tok1"},
			{File: `C:\pics\b.png`, TitleQ: `"b.png"`, Label: "2026-07"},
		},
		Note: "Showing first 60 of 142", OpenFolder: "Open folder", PhotosDir: `C:\pics`,
	}

	escaping := base()
	escaping.LoggedIn = true
	escaping.HasTools = true
	escaping.Title = `VR&Chat <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Unavailable = `un&avail<">`
	escaping.SubTabs = []vgTabSt{{"profile", `Pro&file<">`}, {"groups", `G'roups&`}}
	escaping.Status = vrcStatusSt{Present: true, Variant: "warning", Label: `Signed in as <"dj&">`,
		DL: `signed in as <"dj&">`, Line: `err: bad token & <retry>`}
	escaping.SecStatusBio = `Status &<"bio">`
	escaping.SecEmotes = `Emo&tes<">`
	escaping.SecCamPaths = `Cam&paths<">`
	escaping.SecPhotos = `Pho&tos<">`
	escaping.Editor = vrcEditorFixture()
	escaping.Editor.StatusTitle = `Sta&tus<">`
	escaping.Editor.StatusTip = `<span class=tt data-label="tt-x">i</span>` // trusted pre-rendered markup
	escaping.Editor.DescVal = `d&"v'<>`
	escaping.Editor.BioVal = `b&"io'<>`
	escaping.Editor.Preview = `p&"rev'<>`
	escaping.Editor.HasPreview = true
	escaping.Editor.Presence = []vrcOptSt{{Val: "", Label: `pick&"one'<>`}, {Val: `jo&"in'<>`, Label: `Jo&"in'<>`, Sel: true}}
	escaping.Editor.StatusPreset = vrcPresetSelSt{Act: "vrc-status-preset", Placeholder: `ph&"'<>`, Names: []string{`n&"1'<>`, "n2"}}
	escaping.Editor.BioPreset = vrcPresetSelSt{Act: "vrc-bio-preset", Placeholder: `ph2&"'<>`, Names: []string{`m&"1'<>`}}
	escaping.Emotes.OutDir = `C:\out&"dir'<>`
	escaping.Emotes.UploadURL = `https://vrchat.com/home/emoji?a=1&b=2`
	escaping.CamPaths = vrcCampathsSt{State: "detail",
		Items: []vrcPathItemSt{{Idx: 0, Label: `p&"ath'<>`, Active: true}},
		SVG:   `<svg><text>&amp;</text></svg>`, PlayBtn: `<span id="cpv-vrc-play"></span>`,
		Name: `n&"ame'<>`, Info: `i&"nfo'<>`, Load: `L&"oad'<>`, Copy: `C&"opy'<>`,
		CopyPath: `C:\p&"ath'<>\x.json`, Organize: `O&"rg'<>`, Hint: `h&"int'<>`}
	escaping.Photos = vrcPhotosSt{State: "detail",
		Groups: []vrcPhotoGrpSt{{Label: `g&"rp'<>`, Count: 3, Active: true}},
		Cells: []vrcPhotoCellSt{{File: `C:\p&"ics'<>\a.png`, TitleQ: `"a&#34;.png"`, Label: `l&"bl'<>`,
			Src: `http://127.0.0.1:47700/img/t?a=1&b=2`}},
		Note: `n&"ote'<>`, OpenFolder: `O&"pen'<>`, PhotosDir: `C:\p&"ics'<>`}
	escaping.Groups = vrcgEscapingState()

	long := base()
	long.LoggedIn = true
	long.HasTools = true
	longS := strings.Repeat("very-long-", 120)
	long.Editor.DescVal = longS
	long.Editor.BioVal = longS
	long.Editor.Preview = longS + "x"
	long.Editor.HasPreview = true
	long.Editor.BioPreset = vrcPresetSelSt{Act: "vrc-bio-preset", Placeholder: longS, Names: []string{longS, longS}}
	long.CamPaths = vrcCampathsSt{State: "detail",
		Items:    []vrcPathItemSt{{Idx: 999, Label: longS, Active: true}},
		SVG:      "<svg></svg>",
		Name:     longS,
		Info:     longS,
		CopyPath: `C:\` + strings.Repeat("d", 300) + `\x.json`,
		Load:     "Load", Copy: "Copy", Organize: "Org", Hint: longS}
	long.Photos = vrcPhotosSt{State: "detail",
		Groups: []vrcPhotoGrpSt{{Label: longS, Count: 100000, Active: true}},
		Cells:  []vrcPhotoCellSt{{File: strings.Repeat("f", 500) + ".png", TitleQ: `"` + strings.Repeat("t", 400) + `"`, Label: longS}},
		Note:   longS, OpenFolder: "Open", PhotosDir: strings.Repeat("p", 300)}

	unicode := base()
	unicode.LoggedIn = true
	unicode.HasTools = true
	unicode.Title = "ブイアールチャット 🎧"
	unicode.Sub = "größer Журнал"
	unicode.SubTabs = []vgTabSt{{"profile", "Профиль"}, {"groups", "グループ"}}
	unicode.Status = vrcStatusSt{Present: true, Variant: "success", Label: "Вход: диджей", DL: "вход: диджей", Line: "中文 pipeline 🎛️"}
	unicode.Editor.DescVal = "ラヴ 🎶 größer"
	unicode.Editor.BioVal = "Кириллица 中文 🎧"
	unicode.Editor.Preview = "Кириллица 中文 🎧!"
	unicode.Editor.HasPreview = true
	unicode.Emotes.OutDir = `C:\Пользователи\ラヴ`
	unicode.CamPaths = vrcCampathsSt{State: "empty", Msg: "パスがありません"}
	unicode.Photos = vrcPhotosSt{State: "detail",
		Groups: []vrcPhotoGrpSt{{Label: "写真 🎞️", Count: 7, Active: true}},
		Cells:  []vrcPhotoCellSt{{File: `C:\写真\a.png`, TitleQ: `"写真.png"`, Label: "写真 🎞️", Src: "http://127.0.0.1:47700/img/ток"}},
		Note:   "最初の60件", OpenFolder: "フォルダを開く", PhotosDir: `C:\写真`}
	unicode.Groups = vrcgUnicodeState()

	groupsView := base()
	groupsView.SubActive = "groups"
	groupsView.Groups = vrcgWorkspaceState()

	return map[string]vrcTabSt{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
		"groupsView":  groupsView,
	}
}

func vrcEditorFixture() vrcEditorSt {
	return vrcEditorSt{
		StatusTitle: "Status", StatusTip: "",
		PresenceLabel: "Presence",
		Presence: []vrcOptSt{
			{Val: "", Label: "Keep current"},
			{Val: "join me", Label: "join me"},
			{Val: "active", Label: "active", Sel: true},
			{Val: "ask me", Label: "ask me"},
			{Val: "busy", Label: "busy"},
		},
		StatusMsgLabel: "Status message",
		DescCls:        "vrc-count", DescCount: "6 / 32", DescVal: "on air", MaxDesc: 32,
		SaveStatus: "Save status",
		StatusPreset: vrcPresetSelSt{Act: "vrc-status-preset", Placeholder: "Load preset…",
			Names: []string{"gig", "chill"}},
		PresetsLabel: "Presets…",
		BioTitle:     "Bio",
		BioCls:       "vrc-count over", BioCount: "700 / 512", BioVal: "line1\nline2", MaxBio: 512,
		SaveBio: "Save bio", BioHint: "Saved to VRChat on submit",
		PreviewLabel: "Resolved preview", Preview: "", HasPreview: false,
		BioPreset: vrcPresetSelSt{Act: "vrc-bio-preset", Placeholder: "Load bio…", Names: []string{"tour"}},
		VarsLabel: "Variables…", RefreshLabel: "Refresh events",
	}
}

func vrcEmotesFixture() vrcEmotesSt {
	return vrcEmotesSt{
		Hint:        "Generates a flipbook sheet VRChat accepts as an animated emoji.",
		SourceLabel: "Source clip", NameLabel: "Emoji name", FramesLabel: "Frames", FPSLabel: "FPS",
		TrimStart: "Trim start", TrimEnd: "Trim end", OutDirLabel: "Output folder",
		FrameOpts: []vrcFrameOptSt{
			{Frames: 4, Grid: 2, Res: 512},
			{Frames: 16, Grid: 4, Res: 256, Sel: true},
			{Frames: 64, Grid: 8, Res: 128},
		},
		OutDir:   `C:\Users\dj\Pictures\VRChat\Flipbooks`,
		PingPong: "Ping-pong", Crop: "Crop", Generate: "Generate",
		OpenFolder: "Open output folder", OpenUpload: "Open emoji upload page",
		UploadURL: "https://vrchat.com/home/emoji",
	}
}

// ── Groups sub-view fixtures ──

func vrcgFixtureState() vrcgState {
	return vrcgState{
		Available: true, Unavailable: "VRChat unavailable",
		SignedIn: true, SignInTitle: "Groups", SignInHint: "Sign in to manage groups",
		Mode: "picker",
		Picker: vgPickerSt{
			Title: "My groups", Refresh: "Refresh", Filter: "", State: "rows",
			Rows: []vgPickerRowSt{
				{Idx: 0, Name: "Rave Page", Meta: "rave.7042 · 1204 members"},
				{Idx: 1, Name: "Late Night", Meta: "late.0001 · 12 members"},
			},
		},
	}
}

func vrcgWorkspaceState() vrcgState {
	st := vrcgFixtureState()
	st.Mode = "workspace"
	st.WS = vgWorkspaceSt{
		Title: "Rave Page", Refresh: "Refresh", Back: "Back to my groups",
		Badges: []vgBadgeSt{{"rave.7042", "outline"}, {"1204 members", "secondary"}, {"12 online", "success"},
			{"public", "info"}, {"Join state: open", "info"}, {"Verified", "success"}, {"You own this group", "success"}},
		View: "overview",
		Tabs: []vgTabSt{{"overview", "Overview"}, {"members", "Members"}, {"requests", "Requests"},
			{"invites", "Invites"}, {"bans", "Bans"}, {"posts", "Posts"}, {"audit", "Audit log"}},
		Overview: vgOverviewSt{
			CardTitle: "Overview", LoadingMsg: "Loading group…", MissingMsg: "Could not load",
			AboutTitle: "About", Desc: "The rave.page community group.",
			KVs: []vgKVSt{
				{Label: "Short code", DL: "short code", Value: "rave.7042"},
				{Label: "Members", DL: "members", Value: "1204 (12 online)"},
				{Label: "Privacy", DL: "privacy", Value: "public"},
				{Label: "Join state", DL: "join state", Value: "open"},
				{Label: "Owner", DL: "owner", Value: "You"},
			},
			RulesTitle: "Group rules", Rules: "Be nice.\nNo spam.",
			PermsTitle: "Your permissions", PermsMode: "owner", PermsMsg: "Owner - full permissions",
			RolesTitle: "Roles - 2",
			Roles: []vgRoleSt{
				{Name: "Admin", Tags: []vgBadgeSt{{"management", "warning"}, {"2FA required", "error"}},
					Order: "Order 1", Desc: "Full control", PermSum: "3 permissions",
					Perms: []string{"*", "group-members-remove", "group-bans-manage"}},
				{Name: "Member", Tags: []vgBadgeSt{{"self-assign", "info"}}, Order: "Order 9", Perms: []string{}},
			},
		},
	}
	return st
}

func vrcgEscapingState() vrcgState {
	st := vrcgFixtureState()
	st.Unavailable = `un&avail<">`
	st.SignInTitle = `G&roups<">`
	st.SignInHint = `s&ign'<>`
	st.Picker = vgPickerSt{
		Title: `My g&roups<">`, Refresh: `R&efresh'<>`, Filter: `f&"ilter'<>`, State: "rows",
		Rows: []vgPickerRowSt{{Idx: 7, Name: `A&B <"quoted'>`, Meta: `sc&"'<> · 3 members`}},
	}
	return st
}

func vrcgUnicodeState() vrcgState {
	st := vrcgFixtureState()
	st.Picker = vgPickerSt{Title: "マイグループ", Refresh: "Обновить", Filter: "ラヴ", State: "rows",
		Rows: []vgPickerRowSt{{Idx: 0, Name: "Кириллица 中文 🎛️", Meta: "код · 5 участников"}}}
	return st
}

// vrcgFixtures exercises every Groups view + list state (the sub-view has its own export).
func vrcgFixtures() map[string]vrcgState {
	unavailable := vrcgState{Unavailable: "VRChat unavailable", SignInTitle: "Groups", SignInHint: "sign in"}

	signedOut := vrcgFixtureState()
	signedOut.SignedIn = false

	empty := vrcgFixtureState()
	empty.Picker = vgPickerSt{Title: "My groups", Refresh: "Refresh", State: "none", Msg: "No groups found", Rows: []vgPickerRowSt{}}

	loading := vrcgFixtureState()
	loading.Picker = vgPickerSt{Title: "My groups", Refresh: "Refresh", State: "loading", Msg: "Loading groups…", Rows: []vgPickerRowSt{}}

	overview := vrcgWorkspaceState()

	overviewPerms := vrcgWorkspaceState()
	overviewPerms.WS.Badges = []vgBadgeSt{{"1204 members", "secondary"}}
	overviewPerms.WS.Overview.PermsMode = "list"
	overviewPerms.WS.Overview.PermsMsg = ""
	overviewPerms.WS.Overview.PermBadges = []vgBadgeSt{{"*", "success"}, {"group-bans-manage", "secondary"}}
	overviewPerms.WS.Overview.Rules = ""
	overviewPerms.WS.Overview.Desc = ""
	overviewPerms.WS.Overview.Roles = []vgRoleSt{}
	overviewPerms.WS.Overview.RolesEmpty = "No roles visible"

	overviewLoading := vrcgWorkspaceState()
	overviewLoading.WS.Overview.Loading = true

	overviewMissing := vrcgWorkspaceState()
	overviewMissing.WS.Overview.Missing = true

	members := vrcgWorkspaceState()
	members.WS.View = "members"
	members.WS.Members = vgMembersSt{
		CardTitle: "Members - 2 loaded", State: "rows",
		Rows: []vgMemberRowSt{
			{Name: "dymattic", Tags: []vgBadgeSt{{"Admin", "secondary"}, {"representing", "success"}},
				Meta: "Joined 2026-01-02 10:00", Acts: []vgBtnSt{{"Roles", "ghost", "vrcg-roles:0"},
					{"Kick", "warn", "vrcg-kick:0"}, {"Ban", "destructive", "vrcg-ban:0"}}},
			{Name: `usr_&"<>'`, Tags: []vgBadgeSt{{"usr_1234567890…", "secondary"}}, Meta: "", Acts: []vgBtnSt{}},
		},
		Pager: vgPagerSt{Mode: "more", Label: "Load more", Act: "vrcg-more:members"},
	}

	membersEmpty := vrcgWorkspaceState()
	membersEmpty.WS.View = "members"
	membersEmpty.WS.Members = vgMembersSt{CardTitle: "Members - 0 loaded", State: "empty",
		Msg: "No members visible", Rows: []vgMemberRowSt{}, Pager: vgPagerSt{}}

	membersCap := vrcgWorkspaceState()
	membersCap.WS.View = "members"
	membersCap.WS.Members = vgMembersSt{CardTitle: "Members - 1000 loaded", State: "loading",
		Msg: "Loading members…", Rows: []vgMemberRowSt{}, Pager: vgPagerSt{Mode: "cap", Msg: "Showing first 1000."}}

	requests := vrcgWorkspaceState()
	requests.WS.View = "requests"
	requests.WS.Users = vgUsersSt{
		CardTitle: "Join requests", State: "rows", Empty: "No pending join requests.", Head: []vgBtnSt{},
		Rows: []vgUserRowSt{{Name: "friend1", Sub: "2026-07-01 12:00 · pending",
			Acts: []vgBtnSt{{"Accept", "go", "vrcg-req-a:0"}, {"Reject", "warn", "vrcg-req-r:0"}}}},
		Pager: vgPagerSt{Mode: "loading", Msg: "Loading…"},
	}

	invites := vrcgWorkspaceState()
	invites.WS.View = "invites"
	invites.WS.Users = vgUsersSt{
		CardTitle: "Invites", State: "notloaded", Msg: "Not loaded - Refresh to retry.",
		Empty: "No outstanding invites.", Head: []vgBtnSt{{"Invite user…", "primary", "vrcg-invite"}},
		Rows: []vgUserRowSt{}, Pager: vgPagerSt{},
	}

	bans := vrcgWorkspaceState()
	bans.WS.View = "bans"
	bans.WS.Users = vgUsersSt{CardTitle: "Bans", State: "empty", Empty: "No banned users.",
		Head: []vgBtnSt{}, Rows: []vgUserRowSt{}, Pager: vgPagerSt{}}

	posts := vrcgWorkspaceState()
	posts.WS.View = "posts"
	posts.WS.Posts = vgPostsSt{
		AnnTitle: "Current announcement", AnnTip: `<span class=tt data-label="tt-vrchat-announcement">?</span>`,
		HasAnn: true, AnnHead: "Doors 22:00", AnnWhen: "2026-07-20 18:00", AnnText: "Set times inside <the> post & more",
		AnnEmptyMsg: "No announcement set.", CanAnn: true,
		NewAnnTitle: "New announcement", NewPostTitle: "New post",
		FTitle: "Title", FText: "Text", FImage: "Image ID (optional)", FNotify: "Send notification to members",
		AnnSubmit:  "Post announcement",
		AnnHint:    "Replaces the group's current announcement; VRChat enforces its own length limits.",
		PostSubmit: "Create post", PostHint: "Adds to the group's post feed (doesn't replace the announcement).",
		CardTitle: "Posts - 2 loaded", State: "rows", Empty: "No posts yet.",
		Rows: []vgPostRowSt{
			{Title: "Lineup", Meta: "2026-07-19 10:00 · public · usr_1", Text: "A & B <b>", Del: []vgBtnSt{{"Delete", "destructive", "vrcg-post-del:0"}}},
			{Title: "Notes", Meta: "2026-07-18 10:00", Text: "", Del: []vgBtnSt{}},
		},
		Pager: vgPagerSt{Mode: "more", Label: "Load more", Act: "vrcg-more:posts"},
	}

	postsNoPerm := vrcgWorkspaceState()
	postsNoPerm.WS.View = "posts"
	postsNoPerm.WS.Posts = vgPostsSt{
		AnnTitle: "Current announcement", AnnTip: "", AnnEmpty: true, AnnEmptyMsg: "No announcement set.",
		CanAnn: false, CardTitle: "Posts - 0 loaded", State: "empty", Empty: "No posts yet.",
		Rows: []vgPostRowSt{}, Pager: vgPagerSt{},
	}

	audit := vrcgWorkspaceState()
	audit.WS.View = "audit"
	audit.WS.Audit = vgAuditSt{
		CardTitle: "Audit log - 1 loaded", NoPerm: true,
		NoPermMsg: "Viewing usually requires the group-audit-view permission.",
		State:     "rows", Empty: "No audit entries.", RawSummary: "raw entry",
		Rows: []vgAuditRowSt{{When: "2026-07-20 18:00", Event: "group.member.role.assign", Actor: "dymattic",
			Desc: `assigned <Admin> & more`, Raw: "{\n  \"a\": \"<b>&\"\n}"}},
		Pager: vgPagerSt{Mode: "cap", Msg: "Showing first 500."},
	}

	auditEmpty := vrcgWorkspaceState()
	auditEmpty.WS.View = "audit"
	auditEmpty.WS.Audit = vgAuditSt{CardTitle: "Audit log - 0 loaded", State: "empty",
		Empty: "No audit entries.", RawSummary: "raw entry", Rows: []vgAuditRowSt{}, Pager: vgPagerSt{}}

	longS := strings.Repeat("very-long-", 120)
	long := vrcgWorkspaceState()
	long.WS.Title = longS
	long.WS.Overview.Desc = longS
	long.WS.Overview.Rules = longS
	long.WS.Overview.Roles = []vgRoleSt{{Name: longS, Tags: []vgBadgeSt{}, Order: "Order 100000",
		Desc: longS, PermSum: "500 permissions", Perms: []string{strings.Repeat("p", 300)}}}

	unicode := vrcgWorkspaceState()
	unicode.WS.Title = "Кириллица 中文 🎛️"
	unicode.WS.Overview.Desc = "größer ラヴ"
	unicode.WS.Overview.KVs = []vgKVSt{{Label: "Владелец", DL: "владелец", Value: "Вы"}}
	unicode.WS.Overview.Roles = []vgRoleSt{{Name: "管理者", Tags: []vgBadgeSt{{"管理", "warning"}},
		Order: "順序 1", Desc: "説明", PermSum: "1 権限", Perms: []string{"権限"}}}

	escaping := vrcgEscapingState()

	return map[string]vrcgState{
		"unavailable":     unavailable,
		"signedOut":       signedOut,
		"empty":           empty,
		"loading":         loading,
		"picker":          vrcgFixtureState(),
		"overview":        overview,
		"overviewPerms":   overviewPerms,
		"overviewLoading": overviewLoading,
		"overviewMissing": overviewMissing,
		"members":         members,
		"membersEmpty":    membersEmpty,
		"membersCap":      membersCap,
		"requests":        requests,
		"invites":         invites,
		"bans":            bans,
		"posts":           posts,
		"postsNoPerm":     postsNoPerm,
		"audit":           audit,
		"auditEmpty":      auditEmpty,
		"escaping":        escaping,
		"long":            long,
		"unicode":         unicode,
	}
}

func TestZigVRChatGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range vrcFixtures() {
		t.Run(name, func(t *testing.T) {
			assertZigEqual(t, "full", vrchatHTML(st), stateJSON(st), zigui.RenderVRChat)
			assertZigEqual(t, "status", vrcStatusHTML(st.Status), stateJSON(st.Status), zigui.RenderVRChatStatus)
			assertZigEqual(t, "editor", vrcEditorRenderHTML(st.Editor), stateJSON(st.Editor), zigui.RenderVRChatEditor)
			assertZigEqual(t, "campaths", vrcCampathsHTML(st.CamPaths), stateJSON(st.CamPaths), zigui.RenderVRChatCampaths)
			assertZigEqual(t, "photos", vrcPhotosHTML(st.Photos), stateJSON(st.Photos), zigui.RenderVRChatPhotos)
			assertZigEqual(t, "groups", vrcgBodyHTML(st.Groups), stateJSON(st.Groups), zigui.RenderVRCGroups)
		})
	}
}

func TestZigVRCGroupsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range vrcgFixtures() {
		t.Run(name, func(t *testing.T) {
			assertZigEqual(t, "body", vrcgBodyHTML(st), stateJSON(st), zigui.RenderVRCGroups)
		})
	}
}

// assertZigEqual compares a Zig renderer against its Go reference. An empty Go render means the
// fragment renders nothing (Zig returns !ok - the bridge falls back to the same empty string).
func assertZigEqual(t *testing.T, what, want string, js []byte, f func([]byte) (string, bool)) {
	t.Helper()
	if js == nil {
		t.Fatalf("%s: state marshal failed", what)
	}
	got, ok := f(js)
	if !ok {
		if want == "" {
			return
		}
		t.Fatalf("%s: zig render failed", what)
	}
	assertBytesEqual(t, what, want, got)
}
