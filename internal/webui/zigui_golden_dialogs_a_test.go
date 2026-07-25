//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Dialog-sweep-A golden gate: every dialog in the publish/transcode family must render
// BYTE-IDENTICALLY in Zig and Go. Fixtures come from render_dialogs_a_test.go (untagged, so
// the byte-parity harness and this gate share one set).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// dlgChoiceFixtures covers the shared confirm/picker/context-menu shape over its own axes:
// footer-vs-body buttons, raw-vs-escaped message, no message, empty button list, caps.
func dlgChoiceFixtures() map[string]dlgChoiceSt {
	confirm := dlgChoiceSt{
		Title: "Delete set", HasMsg: true, Msg: `Delete "Live set" and its tracklist?`,
		Btns: []uiBtn{
			{Label: "Delete", Variant: "destructive", Act: "pub-del-do:r1"},
			{Label: "Delete + 3 files", Variant: "destructive", Act: "pub-del-do:r1\x1ffiles"},
			{Label: "Cancel", Variant: "ghost", Act: "modal-close"},
		},
	}
	rawMsg := dlgChoiceSt{
		Title: "Remove capture", HasMsg: true, MsgRaw: true,
		Msg: `Remove the capture "set &amp; loud.ogg" from the library?`,
		Btns: []uiBtn{
			{Label: "Remove", Variant: "outline", Act: "pub-capdel-do:c1"},
			{Label: "Cancel", Variant: "ghost", Act: "modal-close"},
		},
	}
	picker := dlgChoiceSt{
		Title: "Export tracklist", HasMsg: true, MsgRaw: true,
		Msg: "Choose a format for the tracklist export.", InBody: true,
		Btns: []uiBtn{
			{Label: "Text (.txt)", Variant: "primary", Act: "pub-exportfmt:r1\x1ftxt"},
			{Label: "CSV (.csv)", Variant: "outline", Act: "pub-exportfmt:r1\x1fcsv"},
			{Label: "JSON (.json)", Variant: "outline", Act: "pub-exportfmt:r1\x1fjson"},
		},
	}
	ctx := dlgChoiceSt{
		Title: "track.mp3", InBody: true,
		Btns: []uiBtn{
			{Label: "Mark 2 as working together", Variant: "primary", Act: "lib-compat-mark:pub"},
			{Label: "Find compatible", Variant: "outline", Act: `lib-compat-find:P:\a & b.mp3`},
			{Label: "Copy path", Variant: "ghost", Act: "copy", Val: `P:\a & b.mp3`},
			{Label: "Remove from tracklist", Variant: "destructive", Act: "pub-trm:r1\x1f3"},
		},
	}
	long := confirm
	long.Msg = strings.Repeat("a very long set name ", 60)
	long.Btns = nil
	for i := 0; i < 40; i++ {
		long.Btns = append(long.Btns, uiBtn{Label: strings.Repeat("btn ", 10), Variant: "outline", Act: "x"})
	}

	return map[string]dlgChoiceSt{
		"confirm":  confirm,
		"rawMsg":   rawMsg,
		"picker":   picker,
		"ctxMenu":  ctx,
		"noMsg":    {Title: "Track 4", InBody: true, Btns: []uiBtn{{Label: "Remove", Variant: "destructive", Act: "pub-trm:r1\x1f3"}}},
		"noBtns":   {Title: "Nothing to do", HasMsg: true, Msg: "No actions available", Btns: []uiBtn{}},
		"blankMsg": {Title: "Blank", HasMsg: true, Msg: "", Btns: []uiBtn{}},
		"escaping": {Title: dlgAdv, HasMsg: true, Msg: dlgAdv, Btns: []uiBtn{{Label: dlgAdv, Variant: "destructive", Act: "pub-del-do:" + dlgAdv, Val: dlgAdv}}},
		"escRaw":   {Title: dlgAdv, HasMsg: true, MsgRaw: true, Msg: `<b>raw &amp; trusted</b>`, InBody: true, Btns: []uiBtn{}},
		"long":     long,
		"unicode":  {Title: "セットを削除", HasMsg: true, Msg: "Видалити «Живий сет»?", Btns: []uiBtn{{Label: "Видалити", Variant: "destructive", Act: "pub-del-do:r☂1"}}},
	}
}

// dlgRenameFixtures: create-vs-edit style axes for the rename form (blank vs prefilled name,
// escaping, unicode, long).
func dlgRenameFixtures() map[string]pubRenameDlgSt {
	of := func(id, lbl, cur, submit string) pubRenameDlgSt {
		return pubRenameDlgSt{Title: submit, ID: id, NameLbl: lbl, NameDL: strings.ToLower(lbl), Cur: cur, Submit: submit}
	}
	return map[string]pubRenameDlgSt{
		"blank":    of("r1", "Set name", "", "Rename set"),
		"prefill":  of("r1", "Set name", "Friday warmup", "Rename set"),
		"escaping": of("r&1\x1fx", `Set n"ame <x>'`, dlgAdv, dlgAdv),
		"long":     of("r1", "Set name", strings.Repeat("name ", 200), "Rename set"),
		"unicode":  of("r☂1", "セット名", "Живий сет 🎧", "Перейменувати"),
	}
}

// dlgExportFixtures: the CSV/JSON preview across empty / populated / escaping / long /
// unicode payloads.
func dlgExportFixtures() map[string]pubExpDlgSt {
	return map[string]pubExpDlgSt{
		"empty":     pubExportState("csv", ""),
		"csv":       pubExportState("csv", "n,offset,artist,title\n1,0:00,A,B\n2,4:12,C,D"),
		"json":      pubExportState("json", `[{"n":1,"track":"A - B"}]`),
		"escaping":  pubExportState(dlgAdv, dlgAdv),
		"long":      pubExportState("csv", strings.Repeat("1,0:00,Artist,Title\n", 800)),
		"unicode":   pubExportState("json", `[{"track":"アーティスト — größer"}]`),
		"fmtQuoted": pubExportState(`csv" onmouseover="x`, "a,b"),
	}
}

func TestZigDialogsAGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range dlgChoiceFixtures() {
		t.Run("choice/"+name, func(t *testing.T) {
			zigGolden(t, "choice", st, dlgChoiceHTMLOf(st), zigui.RenderDlgChoice)
		})
	}
	for _, st := range dlgTxtFx() {
		t.Run("txtExport/"+st.Title, func(t *testing.T) {
			zigGolden(t, "txtExport", st, pubTxtDlgHTMLOf(st), zigui.RenderDlgTxtExport)
		})
	}
	for name, st := range dlgExportFixtures() {
		t.Run("exportPrev/"+name, func(t *testing.T) {
			zigGolden(t, "exportPrev", st, pubExpDlgHTMLOf(st), zigui.RenderDlgExportPrev)
		})
	}
	for name, st := range dlgRenameFixtures() {
		t.Run("rename/"+name, func(t *testing.T) {
			zigGolden(t, "rename", st, pubRenameDlgHTMLOf(st), zigui.RenderDlgRename)
		})
	}
	for name, st := range dlgFixFx() {
		t.Run("fix/"+name, func(t *testing.T) {
			zigGolden(t, "fix", st, pubFixDlgHTMLOf(st), zigui.RenderDlgFix)
		})
	}
	for name, st := range dlgPresetFx() {
		t.Run("preset/"+name, func(t *testing.T) {
			zigGolden(t, "preset", st, mpPresetDlgHTMLOf(st), zigui.RenderDlgPreset)
		})
	}
	for name, st := range dlgPatFx() {
		t.Run("patMgr/"+name, func(t *testing.T) {
			zigGolden(t, "patMgr", st, cePatMgrHTMLOf(st), zigui.RenderDlgPatMgr)
		})
	}
}
