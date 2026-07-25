//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Cue-editor golden gate: the deepest library subview (#ce-topbar readout strip, the
// full-width wave strip, the editor rail) must render byte-identically in Zig.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// ceSelFixture builds a resolved smart select for the fixtures.
func ceSelFixture(id, label, cur string, open bool, rows ...selRow) selState {
	s := selState{ID: id, Label: label, CurLabel: cur, Open: open, Rows: []selRow{}}
	s.Rows = append(s.Rows, rows...)
	return s
}

// ceTopbarFixtures: off, local (drops + verifiable), verified, rce-dirty + no file tag,
// escaping, long, unicode.
func ceTopbarFixtures() map[string]ceTopbarSt {
	base := func() ceTopbarSt {
		return ceTopbarSt{Show: true, Eyebrow: "Cue prep", Title: "Amn3sia - Hard Drive",
			Meta: "128.0 BPM", Cursor: "1:23", BarLbl: "Bar", BarBeat: "42.3", Jump: "Jump 4 beats",
			Census: "6 cues", VerifyAct: "gf-verify:C:\\Music\\a.flac",
			VerifiedTip: "Grid verified", VerifiedLbl: "verified", VerifyTip: "Mark this grid verified",
			VerifyLbl: "Mark verified", Tip: `<span class=tt data-topic="cue-edit">?</span>`,
			Close: uiBtn{Label: "✕ Close", Variant: "ghost", Act: "ce-close"},
			Drops: []ceTbDropSt{
				{Act: "ce-goto:15000.000000", Lbl: "1", When: "0:15"},
				{Act: "ce-goto:96500.500000", Lbl: "2", When: "1:36"},
				{Act: "ce-goto:184000.000000", Lbl: "X", When: "3:04"},
			}}
	}
	verified := base()
	verified.Verified = true
	verifiable := base()
	verifiable.Verifiable = true
	noTag := base()
	noTag.NoTag, noTag.NoTagTip = true, "This format cannot store drop tags"
	rce := base()
	rce.HasRce, rce.RceMeta = true, "on Studio PC"
	rce.Dirty, rce.DirtyTip = true, "Unsaved changes"
	rce.Meta = "174.0 BPM · 11A"
	rce.NoTag, rce.NoTagTip = true, "no tag support"

	return map[string]ceTopbarSt{
		// editor off: both renderers emit "" (Zig returns NULL, Go the same empty string)
		"off":        {},
		"local":      base(),
		"verifiable": verifiable,
		"verified":   verified,
		"noFileTag":  noTag,
		"rceDirty":   rce,
		"noDrops": func() ceTopbarSt {
			s := base()
			s.Drops = nil
			s.Meta = ""
			return s
		}(),
		"escaping": {Show: true, Eyebrow: `Cue & "prep" <x>`, Title: `A&B - <T> "mix" it's`,
			HasRce: true, RceMeta: `on "Studio" & <PC>`, Dirty: true, DirtyTip: `unsaved & "dirty"`,
			Meta: `128.0 BPM · <8A>`, Cursor: "1:23", BarLbl: `Bar & "beat"`, BarBeat: "42.3",
			Jump: `Jump 4 & "beats"`, Census: `6 & "cues"`,
			Drops: []ceTbDropSt{{Act: `ce-goto:1.5` + `"&<>'`, Lbl: "1", When: "0:01"}},
			NoTag: true, NoTagTip: `no & "tag" <support>`,
			Verified: true, VerifyAct: `gf-verify:C:\M&"u"<s>ic\a'.flac`,
			VerifiedTip: `Grid & "verified"`, VerifiedLbl: `ver&ified<>`,
			Tip:   `<span class=tt data-topic="cue-edit">&amp;?</span>`,
			Close: uiBtn{Label: `✕ Cl&ose "x"`, Variant: "ghost", Act: "ce-close"}},
		"long": func() ceTopbarSt {
			s := base()
			s.Title = strings.Repeat("Very Long Artist - Very Long Title ", 60)
			s.Eyebrow = strings.Repeat("Cue prep ", 80)
			s.Census = strings.Repeat("cues ", 200)
			s.Drops = nil
			for i := 0; i < 40; i++ {
				s.Drops = append(s.Drops, ceTbDropSt{
					Act: "ce-goto:1234567.891234", Lbl: strings.Repeat("9", 6), When: "342:56"})
			}
			return s
		}(),
		"unicode": {Show: true, Eyebrow: "Підготовка", Title: "Ушкуйник — 中文タイトル 🎧",
			HasRce: true, RceMeta: "на Студія 中文 🎛️", Dirty: true, DirtyTip: "Незбережено",
			Meta: "174.0 BPM · 11A", Cursor: "1:23", BarLbl: "Такт", BarBeat: "42.3",
			Jump: "Стрибок 4", Census: "6 записів", Verifiable: true,
			VerifyAct: "gf-verify:D:\\Музика\\трек☂.flac", VerifyTip: "Позначити", VerifyLbl: "Позначити ✓",
			Drops: []ceTbDropSt{{Act: "ce-goto:15000.000000", Lbl: "X", When: "0:15"}},
			Close: uiBtn{Label: "✕ Закрити", Variant: "ghost", Act: "ce-close"}},
	}
}

// ceRailFixtures: off, minimal (no drops / no store), populated (drops + open defaults +
// selection + batch + report hints + write-back), escaping, long, unicode.
func ceRailFixtures() map[string]ceRailSt {
	patRow := func(id, name, sub, badge string) selRow {
		return selRow{Val: id, Label: name, Sub: sub, Badge: badge}
	}
	assignSel := func(i int, cur string, open bool) selState {
		id := "ce-assign-0"
		if i > 0 {
			id = "ce-assign-" + string(rune('0'+i))
		}
		return ceSelFixture(id, "", cur, open,
			selRow{Val: "", Label: "—"},
			patRow("p1", "Build 8", "8 cues", "Amn3sia - Hard Drive"),
			patRow("p2", "Drop stab", "3 cues", ""))
	}
	rows := func(n int, withSel bool) []ceARowSt {
		out := make([]ceARowSt, 0, n)
		for i := 0; i < n; i++ {
			r := ceARowSt{Tag: "DROP " + ceDropLabel(i), Sel: emptySel()}
			if i < 2 {
				r.Placed = true
				r.Act, r.When = "ce-goto:15000.000000", "0:15"
			} else {
				r.UnplacedTip, r.UnplacedLbl = "Drop a marker first", "unplaced"
			}
			if withSel {
				r.HasSel = true
				r.Sel = assignSel(i, "Build 8", i == 1)
			}
			out = append(out, r)
		}
		return out
	}
	minimal := ceRailSt{Show: true, Eyebrow: "Cue prep", Title: "Amn3sia - Hard Drive",
		Mode: ceSelFixture("ce-mode", "Target software", "All software", false,
			selRow{Val: "", Label: "All software", Sub: "cues carry no app tag", Cur: true},
			selRow{Val: "traktor", Label: "Traktor", Sub: "scope to Traktor", Badge: "detected"}),
		Defaults: ceDefaultsSt{Arrow: "▸", Title: "Defaults (all software)", Pads: emptySel()},
		PrepSel:  `<div class=ss-field><span class=ss-label>Prep playlist</span><div class=ss id="ss-prep-rail"></div></div>`,
		PrepHint: "P adds the open track, hold P to remove it.",
		Assign: ceAssignSt{Title: "Patterns per drop", Rows: rows(5, false),
			ShowNoDrops: true, NoDropsHint: "Drop a marker (T) to assign a pattern."},
		AddDrop:    uiBtn{Label: "Add drop", Variant: "outline", Act: "ce-drop-add"},
		DelDrop:    uiBtn{Label: "Remove drop", Variant: "ghost", Act: "ce-drop-del"},
		PromoteAll: uiBtn{Label: "Promote all", Variant: "ghost", Act: "ce-promote"},
		ConvertAll: uiBtn{Label: "Convert all", Variant: "ghost", Act: "ce-convert"},
		ClearOne:   uiBtn{Label: "Clear cues", Variant: "ghost", Act: "ce-clear"},
		Close:      uiBtn{Label: "Close", Variant: "ghost", Act: "ce-close"}}

	full := minimal
	full.Defaults = ceDefaultsSt{Arrow: "▾", Title: "Defaults (Traktor)", Open: true,
		Pads:       ceSelFixture("ce-pref-pads", "Pad budget", "8", false, selRow{Val: "8", Label: "8", Cur: true}),
		Ow:         newToggle("Overwrite cues on apply", "ce-pref-ow", true),
		Split:      newToggle("Split evenly", "ce-pref-split", true),
		HasPromote: true, Promote: newToggle("Promote memory cues (Traktor)", "ce-pref-promote", true),
		HasGrid: true, Grid: newToggle("Anchor the grid on the first hotcue", "ce-pref-grid", false),
		Note: "Defaults apply to newly created cues only."}
	full.Assign = ceAssignSt{Title: "Patterns per drop", Rows: rows(6, true)}
	full.HasSel, full.SelLbl = true, "3 cues selected"
	full.PatNamePH = "Pattern name"
	full.SavePat = uiBtn{Label: "Save as pattern", Variant: "outline", Act: "ce-pat-save"}
	full.HasDSel, full.DSelLbl = true, "2 drops selected"
	full.ShowDelHint, full.DelHint = true, "Del removes the selection."
	full.HasPats = true
	full.Manage = uiBtn{Label: "Manage patterns", Variant: "ghost", Act: "ce-pat-manage"}
	full.HasDrops = true
	full.ApplyHot = uiBtn{Label: "Apply to hotcues", Variant: "primary", Act: "ce-apply:hot"}
	full.ApplyMem = uiBtn{Label: "Apply to memory cues", Variant: "outline", Act: "ce-apply:mem"}
	full.ShowOwNote, full.OwNote = true, "12 cues in scope will be replaced."
	full.Hints = []libHintSt{
		{Tone: "ok", Text: "+8 added · 2 cut · 1 skipped · 0 demoted"},
		{Tone: "info", Text: "3 cues replaced"},
		{Tone: "bad", Text: "write failed: permission denied"},
	}
	full.Batch = ceBatchSt{Show: true, Header: "Batch over 24 checked tracks",
		ApplyHot:   uiBtn{Label: "Apply hotcues to 24", Variant: "outline", Act: "ce-apply-sel:hot"},
		ApplyMem:   uiBtn{Label: "Apply memory cues to 24", Variant: "outline", Act: "ce-apply-sel:mem"},
		PromoteSel: uiBtn{Label: "Promote 24", Variant: "ghost", Act: "ce-promote-sel"},
		ConvertSel: uiBtn{Label: "Convert 24", Variant: "ghost", Act: "ce-convert-sel"},
		ClearSel:   uiBtn{Label: "Clear 24", Variant: "ghost", Act: "ce-clear-sel"},
		Note:       "Batch actions skip tracks without drops."}
	full.WriteBack = `<div class=pb-label>Write back</div><span class="hint hint--ok">Wrote 8 cues to Traktor</span>`

	esc := ceRailSt{Show: true, Eyebrow: `Cue & "prep" <x>`, Title: `A&B - <T> it's`,
		Mode: ceSelFixture(`ce&mode"<>`, `Target & "software"`, `All & <software>`, true,
			selRow{Val: `tr&"aktor`, Label: `Trakt&or <"4">`, Sub: `scope & "it"`, Badge: `det&ected`, Cur: true}),
		Defaults: ceDefaultsSt{Arrow: "▾", Title: `Defaults & "(Traktor)" <x>`, Open: true,
			Pads:  ceSelFixture(`ce-pref-pads&"`, `Pad & "budget"`, `8&"`, false),
			Ow:    newToggle(`Overwrite & "cues"`, "ce-pref-ow", true),
			Split: newToggle(`Split & <evenly>`, "ce-pref-split", false),
			Note:  `Defaults & "note" <x>`},
		PrepSel:  `<div class=ss-field>&amp; raw pass-through</div>`,
		PrepHint: `P adds & "removes" <it>`,
		Assign: ceAssignSt{Title: `Patterns & "per" drop`, Rows: []ceARowSt{
			{Placed: true, Tag: "DROP 1", Act: `ce-goto:1.5&"<>`, When: "0:01", Sel: emptySel()},
			{Tag: "DROP 2", UnplacedTip: `Drop & "a" marker`, UnplacedLbl: `unpl&aced<>`, Sel: emptySel()},
		}, ShowNoDrops: true, NoDropsHint: `Drop & "a" marker (T)`},
		AddDrop: uiBtn{Label: `Add & "drop"`, Variant: "outline", Act: "ce-drop-add"},
		DelDrop: uiBtn{Label: `Remove & <drop>`, Variant: "ghost", Act: "ce-drop-del"},
		HasSel:  true, SelLbl: `3 & "cues" selected`, PatNamePH: `Pattern & "name" <x>`,
		SavePat: uiBtn{Label: `Save & "as" pattern`, Variant: "outline", Act: "ce-pat-save"},
		HasDSel: true, DSelLbl: `2 & "drops"`,
		ShowDelHint: true, DelHint: `Del & "removes" <it>`,
		HasPats: true, Manage: uiBtn{Label: `Manage & "patterns"`, Variant: "ghost", Act: "ce-pat-manage"},
		HasDrops:   true,
		ApplyHot:   uiBtn{Label: `Apply & "hot"`, Variant: "primary", Act: "ce-apply:hot"},
		ApplyMem:   uiBtn{Label: `Apply & "mem"`, Variant: "outline", Act: "ce-apply:mem"},
		ShowOwNote: true, OwNote: `12 & "cues" <replaced>`,
		PromoteAll: uiBtn{Label: `Promote & "all"`, Variant: "ghost", Act: "ce-promote"},
		ConvertAll: uiBtn{Label: `Convert & "all"`, Variant: "ghost", Act: "ce-convert"},
		ClearOne:   uiBtn{Label: `Clear & "cues"`, Variant: "ghost", Act: "ce-clear"},
		Hints:      []libHintSt{{Tone: "bad", Text: `write failed: C:\a&"b"<c>'s`}},
		Batch: ceBatchSt{Show: true, Header: `Batch & "over" <24>`,
			ApplyHot:   uiBtn{Label: `Apply & hot`, Variant: "outline", Act: "ce-apply-sel:hot"},
			ApplyMem:   uiBtn{Label: `Apply & mem`, Variant: "outline", Act: "ce-apply-sel:mem"},
			PromoteSel: uiBtn{Label: `Promote & "24"`, Variant: "ghost", Act: "ce-promote-sel"},
			ConvertSel: uiBtn{Label: `Convert & "24"`, Variant: "ghost", Act: "ce-convert-sel"},
			ClearSel:   uiBtn{Label: `Clear & "24"`, Variant: "ghost", Act: "ce-clear-sel"},
			Note:       `Batch & "note" <x>`},
		WriteBack: `<span class="hint hint--bad">raw &amp; trusted</span>`,
		Close:     uiBtn{Label: `Close & "it"`, Variant: "ghost", Act: "ce-close"}}

	long := full
	long.Title = strings.Repeat("Very Long Artist - Very Long Title ", 60)
	long.PrepSel = "<div>" + strings.Repeat("raw ", 2000) + "</div>"
	long.WriteBack = "<div>" + strings.Repeat("wb ", 3000) + "</div>"
	long.OwNote = strings.Repeat("replaced ", 400)
	long.Assign = ceAssignSt{Title: "Patterns per drop", Rows: rows(48, true)}

	uni := full
	uni.Eyebrow, uni.Title = "Підготовка", "Ушкуйник — 中文タイトル 🎧"
	uni.Mode = ceSelFixture("ce-mode", "Цільова програма", "Усі 中文 🎛️", true,
		selRow{Val: "traktor", Label: "Тракто́р 中文", Sub: "обмежити", Badge: "виявлено", Cur: true})
	uni.PrepHint = "P додає трек, утримуйте P щоб видалити."
	uni.Hints = []libHintSt{{Tone: "ok", Text: "+8 додано · 2 вирізано ☂"}}
	uni.WriteBack = `<span class="hint hint--ok">Записано ✓ 中文</span>`

	return map[string]ceRailSt{
		"off":      {},
		"minimal":  minimal,
		"full":     full,
		"escaping": esc,
		"long":     long,
		"unicode":  uni,
	}
}

func TestZigCueEditTopbarGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range ceTopbarFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			want := ceTopbarHTMLOf(st)
			zig, ok := zigui.RenderCueEditTopbar(js)
			if !ok {
				// The editor-off fragment is legitimately empty ⇒ NULL; Go must agree.
				if want != "" {
					t.Fatalf("zig render failed but Go rendered %d bytes", len(want))
				}
				return
			}
			assertBytesEqual(t, "ce-topbar", want, zig)
		})
	}
}

func TestZigCueEditWaveGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	player := `<div id=mp-library><div id=mp-library-ph style="left:12.50%"></div>` +
		`<svg viewBox="0 0 1000 120"><path d="M0.00,60.00 L1.25,58.75"/></svg></div>`
	for name, tb := range ceTopbarFixtures() {
		st := ceWaveSt{Topbar: tb, Player: player}
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			want := ceWaveHTMLOf(st)
			zig, ok := zigui.RenderCueEditWave(js)
			if !ok {
				t.Fatalf("zig render failed but Go rendered %d bytes", len(want))
			}
			assertBytesEqual(t, "ce-wave", want, zig)
		})
	}
	// no player bound yet (analysing): the wrapper alone must still match
	t.Run("noPlayer", func(t *testing.T) {
		st := ceWaveSt{Topbar: ceTopbarFixtures()["local"]}
		zig, ok := zigui.RenderCueEditWave(stateJSON(st))
		if !ok {
			t.Fatal("zig render failed")
		}
		assertBytesEqual(t, "ce-wave", ceWaveHTMLOf(st), zig)
	})
}

func TestZigCueEditRailGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range ceRailFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			want := ceRailHTMLOf(st)
			zig, ok := zigui.RenderCueEditRail(js)
			if !ok {
				// Editor off ⇒ empty rail ⇒ NULL; the Go fallback renders the same "".
				if want != "" {
					t.Fatalf("zig render failed but Go rendered %d bytes", len(want))
				}
				return
			}
			assertBytesEqual(t, "ce-rail", want, zig)
		})
	}
}

// The rail rides inside libDetailSt as trusted raw markup (kind "raw"), exactly like the
// gridfix cockpit - this pins that seam so a library-side change cannot silently drop it.
func TestZigCueEditRailInDetail(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	rail := ceRailHTMLOf(ceRailFixtures()["full"])
	st := libDetailSt{Kind: libDetailRaw, Raw: rail}
	zig, ok := zigui.RenderLibraryDetail(stateJSON(st))
	if !ok {
		t.Fatal("zig render failed")
	}
	assertBytesEqual(t, "lib-detail(ce)", libDetailHTMLOf(st), zig)
	if !strings.Contains(zig, "ce-agrid") {
		t.Fatal("cue-edit rail markup missing from the detail pane")
	}
}
