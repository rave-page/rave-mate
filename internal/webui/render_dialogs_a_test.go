package webui

// Fixtures for the wave-4 dialog-sweep-A dialogs. UNTAGGED on purpose: the byte-parity
// harness and the tagged Zig golden gate (zigui_golden_dialogs_a_test.go) share ONE fixture
// set, so a state axis added here is exercised by both.

import (
	"strings"
	"testing"
)

const dlgAdv = `a&b"<x>'` // adversarial: every character html.EscapeString touches

// dlgSel builds a resolved smart-select fixture without touching the live ss registry.
// Rows is never nil (a nil slice marshals to JSON null, which the Zig parser rejects).
func dlgSel(id, label, cur string, open bool, filter string, rows ...selRow) selState {
	if rows == nil {
		rows = []selRow{}
	}
	return selState{ID: id, Label: label, CurLabel: cur, Open: open, Filter: filter, Rows: rows}
}

// dlgTxtFx: the text-export dialog over closed / open+filtered / open+no-match selects, the
// custom-template arm, header off, escaping, long and unicode content.
func dlgTxtFx() []pubTxtDlgSt {
	base := func() pubTxtDlgSt {
		return pubTxtDlgSt{
			Title: "closed",
			Sel: dlgSel("pub-txt-preset", "Line style", "Classic", false, "",
				selRow{Val: "classic", Label: "Classic", Cur: true},
				selRow{Val: "youtube", Label: "YouTube"},
				selRow{Val: "custom", Label: "Custom"}),
			Tmpl:     newField("Line template", "pub-txt-line:r1", "{n}. [{offset}] {track}", "text"),
			Header:   newToggle("Include header", "pub-txt-header:r1", true),
			Place:    "Placeholders: {n} {nn} {offset} {artist} {title} {track} {album} {key} {bpm} {deck}",
			Content:  "Live set\n1. [0:00] A - B\n2. [4:12] C - D",
			CopyLbl:  "Copy",
			CloseLbl: "Close",
		}
	}
	open := base()
	open.Title = "openFiltered"
	open.Sel = dlgSel("pub-txt-preset", "Line style", "YouTube", true, "you",
		selRow{Val: "youtube", Label: "YouTube", Cur: true})

	none := base()
	none.Title = "openNoMatch"
	none.Sel = dlgSel("pub-txt-preset", "Line style", "Classic", true, "zzz")

	custom := base()
	custom.Title = "customTemplate"
	custom.Tmpl = newField("Line template", "pub-txt-line:r1", "{offset} {artist} — {title}", "text")
	custom.Header = newToggle("Include header", "pub-txt-header:r1", false)

	empty := base()
	empty.Title = "emptyPreview"
	empty.Content, empty.Place = "", ""

	esc := base()
	esc.Title = dlgAdv
	esc.Sel = dlgSel("pub-txt-preset", `St&yle "x"`, `Cla&ssic <1>`, true, `f&"lt`,
		selRow{Val: `v&1`, Label: `L&bl "1"`, Sub: `s&ub`, Badge: `b&dg`, Cur: true})
	esc.Tmpl = newField(`Templ&ate "T"`, "pub-txt-line:"+dlgAdv, dlgAdv, "text")
	esc.Header = newToggle(`He&ader "H"`, "pub-txt-header:"+dlgAdv, true)
	esc.Place, esc.Content = dlgAdv, dlgAdv
	esc.CopyLbl, esc.CloseLbl = `Co&py`, `Clo&se "x"`

	long := base()
	long.Title = "long"
	long.Content = strings.Repeat("12. [1:02:03] Artist - Title\n", 400)
	long.Place = strings.Repeat("placeholder ", 200)

	uni := base()
	uni.Title = "unicode"
	uni.Sel = dlgSel("pub-txt-preset", "スタイル", "クラシック", false, "")
	uni.Tmpl = newField("Шаблон рядка", "pub-txt-line:r☂1", "{offset} {track} · グルーヴ", "text")
	uni.Content = "Живий сет 🎧\n1. [0:00] größer — Über"

	return []pubTxtDlgSt{base(), open, none, custom, empty, esc, long, uni}
}

// dlgFixFx: the time-fix preview across fader-exact (no opener picker) / heuristic
// (picker) / removed rows / nothing-changed / escaping / long / unicode.
func dlgFixFx() map[string]pubFixDlgSt {
	base := func() pubFixDlgSt {
		return pubFixDlgSt{
			Title: "Fix start times", Desc: "set.ogg · leading silence 0:07",
			Opener: emptySel(), SetStartLbl: "Set start",
			StartT: "21:00:00", NewT: "21:00:07", RemovedTx: "removed (before the audio starts)",
			ApplyLbl: "Apply", ApplyAct: "pub-fixtimes-do:r1", CancelLbl: "Cancel",
			Rows: []pubFixRowSt{
				{Num: "1", Off: "0:00", NewOff: "0:00", Label: "Opener - Warmup"},
				{Num: "2", Off: "4:12", NewOff: "4:05", Label: "Kollektiv - Rausch"},
			},
		}
	}
	noRows := base()
	noRows.Rows = nil

	removed := base()
	removed.Rows = []pubFixRowSt{
		{Num: "1", Off: "0:00", Removed: true, Label: "Soundcheck"},
		{Num: "2", Off: "0:30", Removed: true, Label: "Pre-roll"},
		{Num: "3", Off: "5:00", NewOff: "4:53", Label: "Real opener"},
	}

	opener := base()
	opener.Opener = dlgSel("pub-fix-opener", "Which track opens the recording?", "2. Kollektiv - Rausch", false, "",
		selRow{Val: "0", Label: "1. Opener - Warmup"},
		selRow{Val: "1", Label: "2. Kollektiv - Rausch", Cur: true})
	opener.HasOpener = true

	openerOpen := opener
	openerOpen.Opener = dlgSel("pub-fix-opener", "Which track opens the recording?", "2. Kollektiv - Rausch", true, "koll",
		selRow{Val: "1", Label: "2. Kollektiv - Rausch", Cur: true})

	openerNoMatch := opener
	openerNoMatch.Opener = dlgSel("pub-fix-opener", "Which track opens the recording?", "2. Kollektiv - Rausch", true, "zzz")

	esc := base()
	esc.Title, esc.Desc, esc.SetStartLbl, esc.RemovedTx = dlgAdv, dlgAdv, dlgAdv, dlgAdv
	esc.ApplyLbl, esc.CancelLbl = dlgAdv, dlgAdv
	esc.ApplyAct = "pub-fixtimes-do:" + dlgAdv // spliced through btn's attr escaping
	esc.HasOpener = true
	esc.Opener = dlgSel("pub-fix-opener", dlgAdv, dlgAdv, true, dlgAdv, selRow{Val: dlgAdv, Label: dlgAdv, Cur: true})
	esc.Rows = []pubFixRowSt{
		{Num: "1", Off: "0:00", Removed: true, Label: dlgAdv},
		{Num: "2", Off: "1:00", NewOff: "0:53", Label: dlgAdv},
	}

	long := base()
	long.Desc = strings.Repeat("a very long capture file name ", 40)
	long.Rows = nil
	for i := 0; i < 120; i++ {
		long.Rows = append(long.Rows, pubFixRowSt{Num: "9", Off: "1:02:03", NewOff: "1:01:56",
			Label: strings.Repeat("Artist - Title ", 8)})
	}

	uni := base()
	uni.Title, uni.Desc = "開始時刻の修正", "セット.ogg · 無音 0:07"
	uni.SetStartLbl, uni.RemovedTx = "Початок сету", "видалено"
	uni.Rows = []pubFixRowSt{{Num: "1", Off: "0:00", NewOff: "0:00", Label: "グルーヴ — größer"}}

	return map[string]pubFixDlgSt{
		"populated": base(), "noRows": noRows, "removed": removed, "opener": opener,
		"openerOpen": openerOpen, "openerNoMatch": openerNoMatch,
		"escaping": esc, "long": long, "unicode": uni,
	}
}

// dlgPatFx: the pattern manager across store-gone / empty / gated-overwrite / live-overwrite
// / escaping / long / unicode.
func dlgPatFx() map[string]cePatMgrSt {
	gated := cePatMgrSt{
		Title: "Saved cue patterns", RenameLbl: "Rename", Note: "Patterns apply to any track",
		Pats: []cePatRowSt{
			{ID: "p1", Name: "Intro build", Meta: "4 cues · from Kollektiv - Rausch",
				OwGated: true, OwLbl: "Overwrite", OwWhy: "Select cues on the waveform first", DelLbl: "Delete"},
			{ID: "p2", Name: "Drop set", Meta: "2 cues", OwGated: true, OwLbl: "Overwrite",
				OwWhy: "Select cues on the waveform first", DelLbl: "Delete"},
		},
	}
	live := gated
	live.Pats = []cePatRowSt{
		{ID: "p1", Name: "Intro build", Meta: "4 cues", OwLbl: "Overwrite with 3 cues", DelLbl: "Delete"},
	}

	esc := cePatMgrSt{
		Title: dlgAdv, RenameLbl: dlgAdv, Note: dlgAdv,
		Pats: []cePatRowSt{{ID: dlgAdv, Name: dlgAdv, Meta: dlgAdv, OwLbl: dlgAdv, DelLbl: dlgAdv}},
	}
	escGated := esc
	escGated.Pats = []cePatRowSt{{ID: dlgAdv, Name: dlgAdv, Meta: dlgAdv, OwGated: true, OwLbl: dlgAdv, OwWhy: dlgAdv, DelLbl: dlgAdv}}

	long := gated
	long.Pats = nil
	for i := 0; i < 60; i++ {
		long.Pats = append(long.Pats, cePatRowSt{ID: "p", Name: strings.Repeat("pattern name ", 10),
			Meta: strings.Repeat("meta ", 40), OwLbl: "Overwrite with 1 cue", DelLbl: "Delete"})
	}

	uni := gated
	uni.Title, uni.RenameLbl, uni.Note = "保存済みキューパターン", "Перейменувати", "Шаблони спільні"
	uni.Pats = []cePatRowSt{{ID: "p☂", Name: "イントロ 🎛️", Meta: "4 キュー · größer", OwLbl: "Перезаписати", DelLbl: "Видалити"}}

	return map[string]cePatMgrSt{
		"gone":     {Title: "Saved cue patterns", Gone: true, GoneTx: "Pattern store unavailable"},
		"goneEsc":  {Title: dlgAdv, Gone: true, GoneTx: dlgAdv},
		"empty":    {Title: "Saved cue patterns", HasEmpty: true, EmptyTx: "No patterns saved yet", RenameLbl: "Rename", Note: "Save a selection to start"},
		"gated":    gated,
		"live":     live,
		"escaping": esc,
		"escGated": escGated,
		"long":     long,
		"unicode":  uni,
	}
}

// dlgPresetFx: the preset editor across audio-only / video-copy (no encode controls) /
// full video encode / mp3 VBR / lossless / warnings / escaping / long / unicode.
func dlgPresetFx() map[string]mpPresetDlgSt {
	tip := `<span class=tip data-act="tip:enc-container">?</span>`
	selTip := func(id, label, cur string, rows ...selRow) libSelTip {
		return libSelTip{Sel: dlgSel(id, "", cur, false, "", rows...),
			Label: `<span class=ss-label>` + label + tip + `</span>`}
	}
	foot := []uiBtn{
		{Label: "Apply once", Variant: "outline", Act: "mp-papply:publish\x1f0"},
		{Label: "Save preset", Variant: "primary", Act: "mp-psave:publish\x1f0"},
		{Label: "Cancel", Variant: "ghost", Act: "mp-pcancel:publish\x1f0"},
	}
	audio := func() mpPresetDlgSt {
		return mpPresetDlgSt{
			Title:      "Export preset",
			IDField:    newPBField("Id", "mp-pf:publish\x1f0\x1fid", "custom", "text", ""),
			LabelField: newPBField("Label", "mp-pf:publish\x1f0\x1flabel", "My FLAC", "text", ""),
			Container:  selTip("mp-pf-publish-0-container", "Container", "FLAC", selRow{Val: "flac", Label: "FLAC", Cur: true}),
			ACodec:     selTip("mp-pf-publish-0-acodec", "Audio codec", "FLAC", selRow{Val: "flac", Label: "FLAC", Cur: true}),
			Accel:      emptySel(), Res: emptySel(), VBRQ: emptySel(),
			HasLossles: true, LosslessTx: "Lossless codecs ignore a bitrate target",
			Channels:   dlgSel("mp-pf-publish-0-channels", "Channels", "Source", false, "", selRow{Val: "0", Label: "Source", Cur: true}),
			SampleRate: dlgSel("mp-pf-publish-0-samplerate", "Sample rate", "48 kHz", false, "", selRow{Val: "48000", Label: "48 kHz", Cur: true}),
			Loud:       newLoudSt(loudFx()["compactDefault"]),
			Foot:       foot,
		}
	}

	ladder := audio()
	ladder.ACodec = selTip("mp-pf-publish-0-acodec", "Audio codec", "AAC", selRow{Val: "aac", Label: "AAC", Cur: true})
	ladder.HasLossles, ladder.LosslessTx = false, ""
	ladder.HasLadder, ladder.HasChips, ladder.BitrateLbl = true, true, "Audio bitrate"
	ladder.Chips = []libChipSt{
		newChip("128k", "128", "mp-pf:publish\x1f0\x1fabitratek", false),
		newChip("192k", "192", "mp-pf:publish\x1f0\x1fabitratek", true),
		newChip("320k", "320", "mp-pf:publish\x1f0\x1fabitratek", false),
	}
	ladder.MaxHint = "Up to 320k for AAC"

	mp3 := ladder
	mp3.HasVBRTgl, mp3.VBR = true, newToggle("Variable bitrate", "mp-pf:publish\x1f0\x1favbr", false)

	vbr := mp3
	vbr.VBR = newToggle("Variable bitrate", "mp-pf:publish\x1f0\x1favbr", true)
	vbr.HasChips, vbr.Chips, vbr.BitrateLbl, vbr.MaxHint = false, nil, "", ""
	vbr.HasVBRQ = true
	vbr.VBRQ = dlgSel("mp-pf-publish-0-avbrq", "VBR quality", "V2", false, "",
		selRow{Val: "2", Label: "V2", Cur: true}, selRow{Val: "3", Label: "V3"})

	copyVid := ladder
	copyVid.HasVideo = true
	copyVid.VCodec = selTip("mp-pf-publish-0-vcodec", "Video codec", "Stream copy", selRow{Val: "copy", Label: "Stream copy", Cur: true})

	video := copyVid
	video.HasVEnc = true
	video.VCodec = selTip("mp-pf-publish-0-vcodec", "Video codec", "H.264", selRow{Val: "h264", Label: "H.264", Cur: true})
	video.Accel = dlgSel("mp-pf-publish-0-accel", "Hardware", "Auto", false, "", selRow{Val: "auto", Label: "Auto", Cur: true})
	video.RateMode = selTip("mp-pf-publish-0-ratemode", "Rate control", "Quality (CRF)", selRow{Val: "crf", Label: "Quality (CRF)", Cur: true})
	video.RateField = newPBField("CRF", "mp-pf:publish\x1f0\x1fcrf", "20", "number", "18-23 looks transparent for H.264")
	video.Res = dlgSel("mp-pf-publish-0-res", "Resolution", "1080p", false, "", selRow{Val: "1080", Label: "1080p", Cur: true})
	video.FPS = newPBField("Fps", "mp-pf:publish\x1f0\x1ffps", "29.97", "number", "")
	video.HasSrc, video.SrcHint = true, "Source: 3840x2160 H.265, 24 Mbps, 48 kHz AAC"
	video.Warns = []libHintSt{
		{Tone: "warn", Text: "Encoding above the source resolution wastes bitrate"},
		{Tone: "info", Text: "A remux would keep the video untouched"},
	}

	bitrate := video
	bitrate.RateMode = selTip("mp-pf-publish-0-ratemode", "Rate control", "Bitrate", selRow{Val: "bitrate", Label: "Bitrate", Cur: true})
	bitrate.RateField = newPBField("Bitrate", "mp-pf:publish\x1f0\x1fbitratek", "12000", "number", "kbit/s")

	openSel := video
	openSel.Container = libSelTip{
		Sel:   dlgSel("mp-pf-publish-0-container", "", "MP4", true, "mp", selRow{Val: "mp4", Label: "MP4", Cur: true}),
		Label: `<span class=ss-label>Container` + tip + `</span>`,
	}
	noMatch := video
	noMatch.Container = libSelTip{
		Sel:   dlgSel("mp-pf-publish-0-container", "", "MP4", true, "zzz"),
		Label: `<span class=ss-label>Container` + tip + `</span>`,
	}

	esc := video
	esc.Title = dlgAdv
	esc.IDField = newPBField(dlgAdv, "mp-pf:"+dlgAdv, dlgAdv, "text", dlgAdv)
	esc.LabelField = newPBField(dlgAdv, "mp-pf:"+dlgAdv, dlgAdv, "text", "")
	esc.SrcHint, esc.MaxHint, esc.BitrateLbl = dlgAdv, dlgAdv, dlgAdv
	esc.Chips = []libChipSt{newChip(dlgAdv, dlgAdv, "mp-pf:"+dlgAdv, true)}
	esc.Warns = []libHintSt{{Tone: "warn", Text: dlgAdv}}
	esc.Loud = newLoudSt(loudFx()["escaping"])
	esc.Foot = []uiBtn{
		{Label: dlgAdv, Variant: "outline", Act: "mp-papply:" + dlgAdv},
		{Label: dlgAdv, Variant: "primary", Act: "mp-psave:" + dlgAdv},
		{Label: dlgAdv, Variant: "ghost", Act: "mp-pcancel:" + dlgAdv},
	}

	long := video
	long.SrcHint = strings.Repeat("source detail ", 60)
	long.Warns = nil
	for i := 0; i < 30; i++ {
		long.Warns = append(long.Warns, libHintSt{Tone: "warn", Text: strings.Repeat("warning ", 20)})
	}

	uni := video
	uni.Title = "エクスポートプリセット"
	uni.IDField = newPBField("Ідентифікатор", "mp-pf:publish\x1f0\x1fid", "власний", "text", "")
	uni.SrcHint = "ソース: 3840x2160 · größer"

	return map[string]mpPresetDlgSt{
		"audioOnly": audio(), "ladder": ladder, "mp3": mp3, "vbr": vbr,
		"videoCopy": copyVid, "video": video, "bitrate": bitrate,
		"selOpen": openSel, "selNoMatch": noMatch,
		"escaping": esc, "long": long, "unicode": uni,
	}
}

// TestDialogsAStatesHaveNoNullSlices pins the nil-slice trap: a nil Go slice marshals to JSON
// null, which the Zig parser rejects - the dialog would silently fall back to Go.
func TestDialogsAStatesHaveNoNullSlices(t *testing.T) {
	check := func(name string, js []byte) {
		t.Helper()
		if js == nil {
			t.Fatalf("%s: state marshal failed", name)
		}
		if strings.Contains(string(js), `"rows":null`) {
			t.Errorf("%s: a select's rows marshalled null", name)
		}
	}
	for _, st := range dlgTxtFx() {
		check("txt/"+st.Title, stateJSON(st))
	}
	for n, st := range dlgFixFx() {
		check("fix/"+n, stateJSON(st))
	}
	for n, st := range dlgPatFx() {
		check("pat/"+n, stateJSON(st))
	}
	for n, st := range dlgPresetFx() {
		check("preset/"+n, stateJSON(st))
	}
}
