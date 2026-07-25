//go:build zigui

package webui

import (
	"strings"
	"testing"

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
