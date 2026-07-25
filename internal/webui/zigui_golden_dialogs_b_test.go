//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/zigui"
)

// Wave-4 dialog sweep B golden gate: the Zig dialog renderers must be BYTE-IDENTICAL to the Go
// ones for every VRChat ▸ Groups / Worlds / Automations dialog surface.
// Run: bash scripts/build-zig.sh && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

func vgRoleBodyFixtures() map[string]vgRoleBodySt {
	return map[string]vgRoleBodySt{
		"empty":       {},
		"unavailable": {HasHint: true, HintTone: "warn", HintText: "Member list changed - close and reopen."},
		"notLoaded":   {HasHint: true, HintTone: "info", HintText: "Roles not loaded yet - Refresh the group."},
		"populated": {Rows: []vgRoleRowSt{
			{Label: "Moderator (management)", Desc: "Can kick and ban", BtnLabel: "Remove", BtnVar: "warn", Act: "vrcg-role-del:0"},
			{Label: "Resident", Desc: "", BtnLabel: "Add", BtnVar: "go", Act: "vrcg-role-add:1"},
		}},
		"escaping": {Rows: []vgRoleRowSt{
			{Label: `Crew & "VJ" <x>'`, Desc: `desc & "d" <y>'`, BtnLabel: `Add & "now"'`, BtnVar: "go", Act: "vrcg-role-add:0"},
		}},
		"escapingHint": {HasHint: true, HintTone: "warn", HintText: `changed & "gone" <x>'`},
		"long": {Rows: []vgRoleRowSt{
			{Label: strings.Repeat("role name ", 60), Desc: strings.Repeat("d ", 300), BtnLabel: "Add", BtnVar: "go", Act: "vrcg-role-add:0"},
		}},
		"unicode": {Rows: []vgRoleRowSt{
			{Label: "モデレーター · Модератор 🎧", Desc: "größer", BtnLabel: "Add", BtnVar: "go", Act: "vrcg-role-add:0"},
		}},
	}
}

func vgInviteListFixtures() map[string]vgInviteListSt {
	rows := func(n int) []vgInviteRowSt {
		out := make([]vgInviteRowSt, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, vgInviteRowSt{Name: "friend", Status: "active", Act: "vrcg-inv-pick:0"})
		}
		return out
	}
	return map[string]vgInviteListSt{
		"empty":       {},
		"unavailable": {Empty: true, EmptyMsg: "No friends match."},
		"loading":     {Loading: true, LoadingMsg: "Loading friends…"},
		"populated": {Rows: []vgInviteRowSt{
			{Name: "DJ Nova", Status: "active", Act: "vrcg-inv-pick:0"},
			{Name: "Kollektiv", Status: "", Act: "vrcg-inv-pick:1"},
		}},
		"capped":   {Rows: rows(100), HasMore: true, MoreMsg: "…more matches - filter to narrow down"},
		"escaping": {Rows: []vgInviteRowSt{{Name: `DJ & "Nova" <x>'`, Status: `join me & "now"'`, Act: "vrcg-inv-pick:0"}}},
		"long":     {Rows: []vgInviteRowSt{{Name: strings.Repeat("very long friend name ", 40), Status: strings.Repeat("s ", 200), Act: "vrcg-inv-pick:0"}}},
		"unicode":  {Rows: []vgInviteRowSt{{Name: "ゆき · Юки 🎧", Status: "größer", Act: "vrcg-inv-pick:0"}}},
	}
}

func vgMemberConfirmFixtures() map[string]vgMemberConfirmSt {
	return map[string]vgMemberConfirmSt{
		"empty": {},
		"kick": {Title: "Kick member", Verb: "Kick", Name: "DJ Nova", Group: "Rave Crew",
			Note: "They can rejoin or request to join again.", Act: "vrcg-kick-y", Cancel: "Cancel"},
		"ban": {Title: "Ban member", Verb: "Ban", Name: "DJ Nova", Group: "Rave Crew",
			Note: "They are removed and cannot rejoin until unbanned.", Act: "vrcg-ban-y", Cancel: "Cancel"},
		"escaping": {Title: `Kick & "member" <x>'`, Verb: "Kick", Name: `DJ & "Nova" <x>'`,
			Group: `Rave & "Crew" <y>'`, Note: `note & "n" <z>'`, Act: "vrcg-kick-y", Cancel: `Cancel & "x"'`},
		"long": {Title: "Ban member", Verb: "Ban", Name: strings.Repeat("name ", 80),
			Group: strings.Repeat("group ", 80), Note: strings.Repeat("note ", 200), Act: "vrcg-ban-y", Cancel: "Cancel"},
		"unicode": {Title: "追放", Verb: "Ban", Name: "ゆき 🎧", Group: "Клуб", Note: "größer · невозможно", Act: "vrcg-ban-y", Cancel: "Отмена"},
	}
}

func vgPostConfirmFixtures() map[string]vgPostConfirmSt {
	return map[string]vgPostConfirmSt{
		"empty":     {},
		"populated": {Title: "Delete post", Post: "Set times", Group: "Rave Crew", Confirm: "Delete post", Cancel: "Cancel"},
		"escaping": {Title: `Delete & "post" <x>'`, Post: `Set & "times" <y>'`, Group: `Crew & "B" <z>'`,
			Confirm: `Delete & "it"'`, Cancel: "Cancel"},
		"long":    {Title: "Delete post", Post: strings.Repeat("title ", 120), Group: strings.Repeat("g ", 120), Confirm: "Delete post", Cancel: "Cancel"},
		"unicode": {Title: "投稿を削除", Post: "セット · Сет 🎧", Group: "Клуб", Confirm: "削除", Cancel: "Отмена"},
	}
}

func TestZigVgDialogsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	for name, st := range vgRoleBodyFixtures() {
		t.Run("roleBody/"+name, func(t *testing.T) {
			zigGolden(t, "vgRoleBody", st, vgRoleBodyHTMLOf(st), zigui.RenderVgRoleBody)
			m := vgRolesModalSt{Title: `Roles - Rave & "Crew"`, Body: st}
			zigGolden(t, "vgRolesModal", m, vgRolesModalHTMLOf(m), zigui.RenderVgRolesModal)
		})
	}
	for name, st := range vgInviteListFixtures() {
		t.Run("inviteList/"+name, func(t *testing.T) {
			zigGolden(t, "vgInviteList", st, vgInviteListHTMLOf(st), zigui.RenderVgInviteList)
			m := vgInviteModalSt{Title: `Invite to Rave & "Crew"`, SearchPh: "Filter friends… (Enter)",
				IDPh: "usr_… (invite by user ID)", IDBtn: "Invite ID", List: st}
			zigGolden(t, "vgInviteModal", m, vgInviteModalHTMLOf(m), zigui.RenderVgInviteModal)
		})
	}
	for name, st := range vgMemberConfirmFixtures() {
		t.Run("memberConfirm/"+name, func(t *testing.T) {
			zigGolden(t, "vgMemberConfirm", st, vgMemberConfirmHTMLOf(st), zigui.RenderVgMemberConfirm)
		})
	}
	for name, st := range vgPostConfirmFixtures() {
		t.Run("postConfirm/"+name, func(t *testing.T) {
			zigGolden(t, "vgPostConfirm", st, vgPostConfirmHTMLOf(st), zigui.RenderVgPostConfirm)
		})
	}
}

// ── Worlds dialogs ──

func wsListEditorFixtures() map[string]wsListEditorSt {
	base := wsListEditorSt{
		Title: "Edit list: Crew", Help: "Role grants publish that role's member names to the gist (unlisted but public URL). Only whole-group/role member names are listed - never user ids.",
		EmptyMsg: "Empty list - add friends or group roles", DelLabel: "Delete",
		AddPh: "exact VRChat display name", AddBtn: "Add name",
		FriendBtn: "Add friend…", FriendAct: "world-friends:list-1",
		GroupBtn: "Add group role…", GroupAct: "world-groups:list-1",
	}
	empty := base
	empty.Empty = true

	pop := base
	pop.Entries = []wsEntryRowSt{
		{Label: "User: DJ Nova", Act: "world-ent-del:0"},
		{Label: "Group role: Rave Crew - all members", Act: "world-ent-del:1"},
	}

	esc := base
	esc.Title = `Edit list: Crew & "B" <x>'`
	esc.Entries = []wsEntryRowSt{{Label: `User: DJ & "Nova" <x>'`, Act: "world-ent-del:0"}}

	long := base
	long.Entries = []wsEntryRowSt{{Label: strings.Repeat("User: long display name ", 40), Act: "world-ent-del:0"}}

	uni := base
	uni.Title = "Edit list: クルー"
	uni.Entries = []wsEntryRowSt{{Label: "User: ゆき · Юки 🎧", Act: "world-ent-del:0"}}

	return map[string]wsListEditorSt{
		"empty": {}, "unavailable": empty, "populated": pop,
		"escaping": esc, "long": long, "unicode": uni,
	}
}

func wsPosterEditorFixtures() map[string]wsPosterEditorSt {
	base := wsPosterEditorState(2, config.WorldPoster{})
	pop := wsPosterEditorState(0, config.WorldPoster{Img: "https://i.imgur.com/a.png", Caption: "Main stage", Link: "https://rave.page/e/1"})
	warn := wsPosterEditorSt{}
	warn = base
	warn.Img, warn.HasWarn, warn.Warn = "https://evil.example/x.png", true, "Host not on VRChat's image allowlist - prefab shows text only"
	esc := base
	esc.Img, esc.Caption, esc.Link = `https://x/?a=1&b="2"`, `cap & "c" <x>'`, `https://y/?q=a&b='c'`
	long := base
	long.Caption = strings.Repeat("caption ", 120)
	uni := base
	uni.Caption = "メイン · Главная 🎧"
	return map[string]wsPosterEditorSt{
		"empty": {}, "unavailable": base, "populated": pop, "warn": warn,
		"escaping": esc, "long": long, "unicode": uni,
	}
}

func wsFriendListFixtures() map[string]wsFriendListSt {
	rows := func(n int) []wsPickRowSt {
		out := make([]wsPickRowSt, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, wsPickRowSt{Label: "friend", Act: "world-fr-pick:0"})
		}
		return out
	}
	return map[string]wsFriendListSt{
		"empty":       {},
		"loading":     {Loading: true, LoadingMsg: "Loading friends…"},
		"unavailable": {Empty: true, EmptyMsg: "No friends found"},
		"noMatch":     {Empty: true, EmptyMsg: "No match"},
		"populated": {AddLabel: "Add", Rows: []wsPickRowSt{
			{Label: "DJ Nova", Act: "world-fr-pick:0"}, {Label: "Kollektiv", Act: "world-fr-pick:3"},
		}},
		"capped":   {AddLabel: "Add", Rows: rows(60), HasMore: true, MoreMsg: "… refine the filter to see more"},
		"escaping": {AddLabel: `Add & "one"'`, Rows: []wsPickRowSt{{Label: `DJ & "Nova" <x>'`, Act: "world-fr-pick:0"}}},
		"long":     {AddLabel: "Add", Rows: []wsPickRowSt{{Label: strings.Repeat("friend name ", 60), Act: "world-fr-pick:0"}}},
		"unicode":  {AddLabel: "追加", Rows: []wsPickRowSt{{Label: "ゆき · Юки 🎧", Act: "world-fr-pick:0"}}},
	}
}

func wsGroupListFixtures() map[string]wsGroupListSt {
	row := func(n string) wsGroupRowSt {
		return wsGroupRowSt{Label: n, FavLabel: "☆ Pin", FavAct: "world-fav:0", RolesAct: "world-roles:0"}
	}
	return map[string]wsGroupListSt{
		"empty":       {},
		"unavailable": {Empty: true, EmptyMsg: "No groups - search above"},
		"loading":     {Loading: true, LoadingMsg: "Loading your groups…", RolesLabel: "Roles…"},
		"populated": {RolesLabel: "Roles…", Sections: []wsGroupSecSt{
			{Caption: "Favorites", Rows: []wsGroupRowSt{{Label: "Rave Crew", FavLabel: "★ Unpin", FavAct: "world-fav:0", RolesAct: "world-roles:0"}}},
			{Caption: "Your groups", Rows: []wsGroupRowSt{row("Studio (12 members)")}},
			{Caption: "Search results"},
		}},
		"loadingWithRows": {Loading: true, LoadingMsg: "Loading your groups…", RolesLabel: "Roles…",
			Sections: []wsGroupSecSt{{Caption: "Favorites", Rows: []wsGroupRowSt{row("Pinned")}}}},
		"escaping": {RolesLabel: `Roles & "x"…`, Sections: []wsGroupSecSt{
			{Caption: "Favorites", Rows: []wsGroupRowSt{{Label: `Crew & "B" <x>' (3 members)`, FavLabel: `★ Un&pin`, FavAct: "world-fav:0", RolesAct: "world-roles:0"}}},
		}},
		"long": {RolesLabel: "Roles…", Sections: []wsGroupSecSt{
			{Caption: "Your groups", Rows: []wsGroupRowSt{row(strings.Repeat("group name ", 60))}},
		}},
		"unicode": {RolesLabel: "ロール…", Sections: []wsGroupSecSt{
			{Caption: "お気に入り", Rows: []wsGroupRowSt{row("クルー · Клуб 🎧")}},
		}},
	}
}

func wsRoleListFixtures() map[string]wsRoleListSt {
	return map[string]wsRoleListSt{
		"empty":     {},
		"loading":   {Loading: true, LoadingMsg: "Loading roles…"},
		"allOnly":   {AllLabel: "All members", GrantLabel: "Grant"},
		"populated": {AllLabel: "All members", GrantLabel: "Grant", Rows: []wsPickRowSt{{Label: "Moderator (management)", Act: "world-role-pick:0"}, {Label: "Resident", Act: "world-role-pick:1"}}},
		"escaping":  {AllLabel: `All & "members"'`, GrantLabel: `Grant & "it"'`, Rows: []wsPickRowSt{{Label: `Mod & "crew" <x>'`, Act: "world-role-pick:0"}}},
		"long":      {AllLabel: "All members", GrantLabel: "Grant", Rows: []wsPickRowSt{{Label: strings.Repeat("role ", 120), Act: "world-role-pick:0"}}},
		"unicode":   {AllLabel: "全メンバー", GrantLabel: "付与", Rows: []wsPickRowSt{{Label: "モデレーター · Модератор 🎧", Act: "world-role-pick:0"}}},
	}
}

func wsDeviceFixtures() map[string]wsDeviceSt {
	base := wsDeviceSt{
		Title: "Link GitHub", Help: "Open the activation page and enter this code, then approve in your browser:",
		Code: "ABCD-1234", CopyLbl: "Copy code", OpenLbl: "Open activation page", URI: "https://github.com/login/device",
	}
	esc := base
	esc.Code, esc.URI = `A&B "C"<d>'`, `https://x/?a=1&b="2"`
	long := base
	long.Code = strings.Repeat("CODE-", 60)
	uni := base
	uni.CopyLbl, uni.OpenLbl = "コピー", "開く"
	return map[string]wsDeviceSt{"empty": {}, "populated": base, "escaping": esc, "long": long, "unicode": uni}
}

func TestZigWsDialogsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	for name, st := range wsListEditorFixtures() {
		t.Run("listEditor/"+name, func(t *testing.T) {
			zigGolden(t, "wsListEditor", st, wsListEditorHTMLOf(st), zigui.RenderWsListEditor)
		})
	}
	for name, st := range wsPosterEditorFixtures() {
		t.Run("posterEditor/"+name, func(t *testing.T) {
			zigGolden(t, "wsPosterEditor", st, wsPosterEditorHTMLOf(st), zigui.RenderWsPosterEditor)
		})
	}
	for name, st := range wsFriendListFixtures() {
		t.Run("friendList/"+name, func(t *testing.T) {
			zigGolden(t, "wsFriendList", st, wsFriendListHTMLOf(st), zigui.RenderWsFriendList)
			p := wsFriendPickerSt{Title: "Add friend", SearchPh: "filter friends…",
				BackLbl: "Back to list", BackAct: "world-list-edit:list-1", List: st}
			zigGolden(t, "wsFriendPicker", p, wsFriendPickerHTMLOf(p), zigui.RenderWsFriendPicker)
		})
	}
	for name, st := range wsGroupListFixtures() {
		t.Run("groupList/"+name, func(t *testing.T) {
			zigGolden(t, "wsGroupList", st, wsGroupListHTMLOf(st), zigui.RenderWsGroupList)
			p := wsGroupPickerSt{Title: "Add group role", SearchPh: "search all groups…", SearchBtn: "Search",
				Help:    "Grant a whole group or a role. Member expansion only works where the member list is visible (public groups); private groups keep their last good expansion.",
				BackLbl: "Back to list", BackAct: "world-list-edit:list-1", List: st}
			zigGolden(t, "wsGroupPicker", p, wsGroupPickerHTMLOf(p), zigui.RenderWsGroupPicker)
		})
	}
	for name, st := range wsRoleListFixtures() {
		t.Run("roleList/"+name, func(t *testing.T) {
			zigGolden(t, "wsRoleList", st, wsRoleListHTMLOf(st), zigui.RenderWsRoleList)
			p := wsRolePickerSt{Title: `Roles of Crew & "B"`, BackLbl: "Back to groups",
				BackAct: "world-groups:list-1", List: st}
			zigGolden(t, "wsRolePicker", p, wsRolePickerHTMLOf(p), zigui.RenderWsRolePicker)
		})
	}
	for name, st := range wsDeviceFixtures() {
		t.Run("device/"+name, func(t *testing.T) {
			zigGolden(t, "wsDevice", st, wsDeviceHTMLOf(st), zigui.RenderWsDevice)
		})
	}
}

// ── Automations ▸ editor ──

// aeModalFixtures resolves real editor states through the real state builder (tips, loudness
// blocks and preset selects included) plus the adversarial text axes.
func aeModalFixtures(t *testing.T) map[string]aeModalSt {
	t.Helper()
	u, _ := newTestHeadless(t)
	mk := func(a automation.Automation) aeModalSt {
		u.ae.mu.Lock()
		defer u.ae.mu.Unlock()
		u.ae.load(a)
		return u.aeModalState(&u.ae)
	}
	all := mk(automation.Automation{
		ID: "a1", Label: "Post-set pipeline", WatchDir: `D:\captures`, Enabled: true,
		Match: automation.Match{Extensions: []string{".wav", ".flac"}, MinSizeBytes: 8 * 1024 * 1024,
			FilenamePattern: `^set_.*`, MinAgeDays: 0},
		Actions: []automation.Action{
			{Type: automation.ActionRename, BufferMinutes: 120, Template: "{YYYY-MM-DD}_{eventSlug}{ext}"},
			{Type: automation.ActionTrimSilence, ThresholdDb: -48.5, MinSilenceSeconds: 1.5, PresetID: "remux"},
			{Type: automation.ActionTranscode, PresetID: "mp3-320", LoudnessOn: true, LoudnessI: -14, LoudnessTP: -1},
			{Type: automation.ActionMove, OutputDir: `E:\archive`},
			{Type: automation.ActionCopy, OutputDir: `E:\mirror`},
			{Type: automation.ActionDelete},
		},
	})
	minAge := mk(automation.Automation{Label: "Aged", Match: automation.Match{MinAgeDays: 3}})
	invalid := mk(automation.Automation{Actions: []automation.Action{
		{Type: automation.ActionDelete}, {Type: automation.ActionTranscode, PresetID: "remux"},
	}})
	esc := mk(automation.Automation{
		ID: "a2", Label: `Set & "night" <x>'`, WatchDir: `D:\a&b\"c"`,
		Match:   automation.Match{Extensions: []string{".w&v"}, FilenamePattern: `^a&"b"<c>'`},
		Actions: []automation.Action{{Type: automation.ActionRename, Template: `{eventSlug} & "x" <y>'`}},
	})
	long := mk(automation.Automation{Label: strings.Repeat("long automation label ", 40),
		WatchDir: strings.Repeat(`D:\deep\`, 60)})
	uni := mk(automation.Automation{Label: "セット後 · Автоматизация 🎧", WatchDir: `D:\größer\запись`,
		Actions: []automation.Action{{Type: automation.ActionCopy, OutputDir: `E:\コピー`}}})

	withErr := all
	withErr.HasErr, withErr.Err = true, `save failed: bad pattern & "x" <y>'`

	return map[string]aeModalSt{
		"empty": {}, "unavailable": mk(automation.Automation{Enabled: true}),
		"populated": all, "minAgeWarn": minAge, "invalidChain": invalid, "errBanner": withErr,
		"escaping": esc, "long": long, "unicode": uni,
	}
}

func TestZigAutoEditorGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `bash scripts/build-zig.sh` first")
	}
	for name, st := range aeModalFixtures(t) {
		t.Run(name, func(t *testing.T) {
			zigGolden(t, "aeModal", st, aeModalHTMLOf(st), zigui.RenderAutoEditor)
		})
	}
}
