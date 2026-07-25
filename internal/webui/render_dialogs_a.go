package webui

// Wave-4 dialog sweep A: the publish/transcode dialog family as state + PURE renderers,
// mirrored in native/zigui/src/dialogs_a.zig. Same recipe as a tab, plus the two modal
// specifics: the markup ends with components.go modal(title, body, footer), and the exported
// entry point keeps its signature so every u.openModal(u.<x>…) call site is untouched.
//
// The confirm / format-picker / row-context-menu dialogs all share ONE shape (message +
// button list, buttons in the footer or in the body), so they share ONE renderer
// (dlgChoiceHTMLOf ↔ components.zig choiceDialog) instead of six near-copy ports.
//
// Raw (trusted) pass-throughs, matching what Go splices UNESCAPED:
//   - the shared loudness block (components.go loudnessFields) inside the preset editor,
//   - clock readouts + row numbers in the time-fix preview (Go time.Format / fmt.Sprint),
//   - hand-written English literals that wrap an already-escaped value (dlgChoiceSt.MsgRaw).
//
// No dialog here has a live sub-patch: every control change re-opens the whole dialog
// (pub-txt-*, mp-pf:, ce-pat-*), so there is no `_frag` export to add.

import (
	"html"
	"strings"

	"rave.page/mate/internal/zigui"
)

// ── shared: message + buttons dialog ──

// dlgChoiceSt is a confirm / picker / context-menu dialog. HasMsg is explicit because a
// blank message still emits an empty .np-artist (a blank i18n string must not silently
// switch arms). MsgRaw marks a message Go splices UNESCAPED - the hand-written English
// literals that quote an already-escaped value. InBody puts the btn-row inside
// .modal-body, so the footer becomes Go's default Close button.
type dlgChoiceSt struct {
	Title  string  `json:"title"`
	Msg    string  `json:"msg,omitempty"`
	MsgRaw bool    `json:"msgRaw,omitempty"`
	HasMsg bool    `json:"hasMsg,omitempty"`
	Btns   []uiBtn `json:"btns"`
	InBody bool    `json:"inBody,omitempty"`
}

// dlgChoiceHTML renders a choice dialog through Zig when available.
func dlgChoiceHTML(st dlgChoiceSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderDlgChoice(stateJSON(st)); ok {
			return h
		}
	}
	return dlgChoiceHTMLOf(st)
}

// dlgChoiceHTMLOf is the pure renderer.
func dlgChoiceHTMLOf(st dlgChoiceSt) string {
	body := ""
	if st.HasMsg {
		m := st.Msg
		if !st.MsgRaw {
			m = html.EscapeString(m)
		}
		body = `<div class=np-artist>` + m + `</div>`
	}
	footer := ""
	if st.InBody {
		body += uiBtnRow(st.Btns)
	} else {
		footer = uiBtnRow(st.Btns)
	}
	return modal(st.Title, body, footer)
}

// ── publish: text-export style dialog ──

// pubTxtDlgSt is the tracklist text-export dialog (style preset, line template, header
// switch, placeholder legend, live preview).
type pubTxtDlgSt struct {
	Title    string   `json:"title"`
	Sel      selState `json:"sel"`
	Tmpl     uiField  `json:"tmpl"`
	Header   uiToggle `json:"header"`
	Place    string   `json:"place"`
	Content  string   `json:"content"`
	CopyLbl  string   `json:"copyLbl"`
	CloseLbl string   `json:"closeLbl"`
}

func pubTxtDlgHTML(st pubTxtDlgSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderDlgTxtExport(stateJSON(st)); ok {
			return h
		}
	}
	return pubTxtDlgHTMLOf(st)
}

func pubTxtDlgHTMLOf(st pubTxtDlgSt) string {
	body := `<div class=pub-txt-opts>` +
		`<span class=pub-txt-presel>` + selHTML(st.Sel) + `</span>` +
		st.Tmpl.html() +
		st.Header.html() +
		`</div>` +
		`<p class=page-sub>` + html.EscapeString(st.Place) + `</p>` +
		`<textarea class=pub-export-ta readonly rows=12>` + html.EscapeString(st.Content) + `</textarea>`
	footer := dlgCopyBtn(st.CopyLbl, st.Content) + btn(st.CloseLbl, "outline", "modal-close", "")
	return modal(st.Title, body, footer)
}

// dlgCopyBtn is the hand-rolled primary "copy this text" button both export dialogs carry
// (data-val holds the whole payload).
func dlgCopyBtn(label, content string) string {
	return `<button class="rp-btn rp-btn--primary" data-act="copy" data-val="` +
		html.EscapeString(content) + `">` + html.EscapeString(label) + `</button>`
}

// ── publish: export preview (CSV/JSON; also the remote arm) ──

// pubExpDlgSt is the tracklist-export preview. Note/CopyLbl/CloseLbl are HARDCODED ENGLISH
// literals in Go (not i18n keys); Note is spliced RAW, so it stays raw here.
type pubExpDlgSt struct {
	Title    string `json:"title"`
	Note     string `json:"note"`
	Content  string `json:"content"`
	CopyLbl  string `json:"copyLbl"`
	CloseLbl string `json:"closeLbl"`
}

// pubExportState resolves the export-preview dialog. Note/CopyLbl/CloseLbl are hardcoded
// English literals in the Go original (not i18n keys) - replicated verbatim, not "fixed" on
// one side.
func pubExportState(fmtKey, content string) pubExpDlgSt {
	return pubExpDlgSt{
		Title: "Export - " + fmtKey, Note: "Select all + copy, or use Copy below.",
		Content: content, CopyLbl: "Copy", CloseLbl: "Close",
	}
}

func pubExpDlgHTML(st pubExpDlgSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderDlgExportPrev(stateJSON(st)); ok {
			return h
		}
	}
	return pubExpDlgHTMLOf(st)
}

func pubExpDlgHTMLOf(st pubExpDlgSt) string {
	body := `<div class=np-artist>` + st.Note + `</div>` +
		`<textarea class=pub-export-ta readonly rows=14>` + html.EscapeString(st.Content) + `</textarea>`
	footer := dlgCopyBtn(st.CopyLbl, st.Content) + btn(st.CloseLbl, "outline", "modal-close", "")
	return modal(st.Title, body, footer)
}

// ── publish: rename set ──

// pubRenameDlgSt is the rename-set form dialog: the value arrives in m.Form, so the input is
// NAMED rather than data-act'd. Footer = Go's default Close.
type pubRenameDlgSt struct {
	Title   string `json:"title"`
	ID      string `json:"id"`
	NameLbl string `json:"nameLbl"`
	NameDL  string `json:"nameDL"`
	Cur     string `json:"cur"`
	Submit  string `json:"submit"`
}

func pubRenameDlgHTML(st pubRenameDlgSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderDlgRename(stateJSON(st)); ok {
			return h
		}
	}
	return pubRenameDlgHTMLOf(st)
}

func pubRenameDlgHTMLOf(st pubRenameDlgSt) string {
	body := `<form data-act=pub-rename-do class=mform>` +
		hiddenField("id", st.ID) +
		labeledInputDL("name", st.NameLbl, st.NameDL, st.Cur) +
		`<button class="rp-btn rp-btn--primary" type=submit>` + html.EscapeString(st.Submit) + `</button></form>`
	return modal(st.Title, body, "")
}

// ── publish: capture-aligned time fix preview ──

// pubFixRowSt is one previewed tracklist row. Num/Off/NewOff are Go-formatted
// (fmt.Sprint of an int, pubClock) and spliced RAW both sides.
type pubFixRowSt struct {
	Num     string `json:"num"`
	Off     string `json:"off"`
	NewOff  string `json:"newOff,omitempty"`
	Removed bool   `json:"removed,omitempty"`
	Label   string `json:"label"`
}

// pubFixDlgSt is the "Fix start times" preview. HasOpener = the heuristic plan offered more
// than one candidate opener (a fader-history plan is exact and shows no picker). StartT/NewT
// are Go time.Format("15:04:05") strings, raw.
type pubFixDlgSt struct {
	Title       string        `json:"title"`
	Desc        string        `json:"desc"`
	HasOpener   bool          `json:"hasOpener,omitempty"`
	Opener      selState      `json:"opener"`
	SetStartLbl string        `json:"setStartLbl"`
	StartT      string        `json:"startT"`
	NewT        string        `json:"newT"`
	Rows        []pubFixRowSt `json:"rows,omitempty"`
	RemovedTx   string        `json:"removedTx"`
	ApplyLbl    string        `json:"applyLbl"`
	ApplyAct    string        `json:"applyAct"`
	CancelLbl   string        `json:"cancelLbl"`
}

func pubFixDlgHTML(st pubFixDlgSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderDlgFix(stateJSON(st)); ok {
			return h
		}
	}
	return pubFixDlgHTMLOf(st)
}

func pubFixDlgHTMLOf(st pubFixDlgSt) string {
	var b strings.Builder
	b.WriteString(`<div class=np-artist>` + html.EscapeString(st.Desc) + `</div>`)
	if st.HasOpener {
		b.WriteString(`<div class=pub-fix-opener>` + selHTML(st.Opener) + `</div>`)
	}
	b.WriteString(`<div class=pub-fix-rows>`)
	b.WriteString(`<div class=pub-fix-row><span class=pub-track-l>` + html.EscapeString(st.SetStartLbl) + `</span>` +
		`<span class=pub-track-o>` + st.StartT + ` → ` + st.NewT + `</span></div>`)
	for _, r := range st.Rows {
		if r.Removed {
			b.WriteString(`<div class="pub-fix-row pub-fix-removed"><span class=pub-track-n>` + r.Num + `.</span>` +
				`<span class=pub-track-o>[` + r.Off + `] ✕</span>` +
				`<span class=pub-track-l>` + html.EscapeString(r.Label) + ` · ` + html.EscapeString(st.RemovedTx) + `</span></div>`)
			continue
		}
		b.WriteString(`<div class=pub-fix-row><span class=pub-track-n>` + r.Num + `.</span>` +
			`<span class=pub-track-o>[` + r.Off + `] → [` + r.NewOff + `]</span>` +
			`<span class=pub-track-l>` + html.EscapeString(r.Label) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	footer := btnRow(
		btn(st.ApplyLbl, "primary", st.ApplyAct, ""),
		btn(st.CancelLbl, "ghost", "modal-close", ""),
	)
	return modal(st.Title, b.String(), footer)
}

// ── export preset editor (pbuilder.go) ──

// mpPresetDlgSt is the export preset editor. Every "is it shown" branch is an explicit flag
// (never "empty means the other arm"). Loud carries the SHARED loudness block (components.go
// loudSt) as structured state - same as the library encode builder.
type mpPresetDlgSt struct {
	Title      string       `json:"title"`
	IDField    libPBFieldSt `json:"idField"`
	LabelField libPBFieldSt `json:"labelField"`
	HasSrc     bool         `json:"hasSrc,omitempty"`
	SrcHint    string       `json:"srcHint,omitempty"`
	Container  libSelTip    `json:"container"`
	HasVideo   bool         `json:"hasVideo,omitempty"`
	VCodec     libSelTip    `json:"vcodec"`
	HasVEnc    bool         `json:"hasVEnc,omitempty"`
	Accel      selState     `json:"accel"`
	RateMode   libSelTip    `json:"rateMode"`
	RateField  libPBFieldSt `json:"rateField"`
	Res        selState     `json:"res"`
	FPS        libPBFieldSt `json:"fps"`
	ACodec     libSelTip    `json:"acodec"`
	HasLadder  bool         `json:"hasLadder,omitempty"`
	HasVBRTgl  bool         `json:"hasVbrTgl,omitempty"`
	VBR        uiToggle     `json:"vbr"`
	HasVBRQ    bool         `json:"hasVbrq,omitempty"`
	VBRQ       selState     `json:"vbrq"`
	HasChips   bool         `json:"hasChips,omitempty"`
	BitrateLbl string       `json:"bitrateLbl,omitempty"`
	Chips      []libChipSt  `json:"chips,omitempty"`
	MaxHint    string       `json:"maxHint,omitempty"`
	HasLossles bool         `json:"hasLossless,omitempty"`
	LosslessTx string       `json:"losslessTx,omitempty"`
	Channels   selState     `json:"channels"`
	SampleRate selState     `json:"samplerate"`
	Loud       loudSt       `json:"loud"`
	Warns      []libHintSt  `json:"warns,omitempty"`
	Foot       []uiBtn      `json:"foot"`
}

func mpPresetDlgHTML(st mpPresetDlgSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderDlgPreset(stateJSON(st)); ok {
			return h
		}
	}
	return mpPresetDlgHTMLOf(st)
}

func mpPresetDlgHTMLOf(st mpPresetDlgSt) string {
	var b strings.Builder
	b.WriteString(`<div class=pedit>`)
	b.WriteString(`<div class=fpair>` + st.IDField.html() + st.LabelField.html() + `</div>`)
	if st.HasSrc {
		b.WriteString(hint("info", st.SrcHint))
	}
	b.WriteString(selHTMLRaw(st.Container.Sel, st.Container.Label))
	if st.HasVideo {
		b.WriteString(`<div class=pb-grp>`)
		b.WriteString(selHTMLRaw(st.VCodec.Sel, st.VCodec.Label))
		if st.HasVEnc {
			b.WriteString(selHTML(st.Accel))
			b.WriteString(selHTMLRaw(st.RateMode.Sel, st.RateMode.Label))
			b.WriteString(st.RateField.html())
			b.WriteString(selHTML(st.Res))
			b.WriteString(st.FPS.html())
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`<div class=pb-grp>`)
	b.WriteString(selHTMLRaw(st.ACodec.Sel, st.ACodec.Label))
	if st.HasLadder {
		if st.HasVBRTgl {
			b.WriteString(st.VBR.html())
		}
		if st.HasVBRQ {
			b.WriteString(selHTML(st.VBRQ))
		} else if st.HasChips {
			b.WriteString(`<div class=pb-field><div class=pb-label>` + html.EscapeString(st.BitrateLbl) + `</div><div class=lt-chips>`)
			for _, ch := range st.Chips {
				b.WriteString(ch.html())
			}
			b.WriteString(`</div><div class=pb-hint>` + html.EscapeString(st.MaxHint) + `</div></div>`)
		}
	} else if st.HasLossles {
		b.WriteString(`<div class=pb-hint>` + html.EscapeString(st.LosslessTx) + `</div>`)
	}
	b.WriteString(fpair(selHTML(st.Channels), selHTML(st.SampleRate)))
	b.WriteString(`</div>`)
	b.WriteString(st.Loud.html())
	for _, w := range st.Warns {
		b.WriteString(hint(w.Tone, w.Text))
	}
	b.WriteString(`</div>`)
	return modal(st.Title, b.String(), uiBtnRow(st.Foot))
}

// ── cue editor: saved-pattern manager ──

// cePatRowSt is one stored cue pattern. The rename form's act and the delete/overwrite
// buttons' acts are `ce-pat-*:` ++ ID (the prefixes carry nothing escapable, so
// concatenate-then-escape matches Go's attrQ of the joined string).
type cePatRowSt struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Meta    string `json:"meta"`
	OwGated bool   `json:"owGated,omitempty"`
	OwLbl   string `json:"owLbl"`
	OwWhy   string `json:"owWhy,omitempty"`
	DelLbl  string `json:"delLbl"`
}

// cePatMgrSt is the manage-patterns dialog. Gone = the pattern store is unavailable (one bad
// hint and nothing else); HasEmpty = the store is there but holds no patterns.
type cePatMgrSt struct {
	Title     string       `json:"title"`
	Gone      bool         `json:"gone,omitempty"`
	GoneTx    string       `json:"goneTx,omitempty"`
	HasEmpty  bool         `json:"hasEmpty,omitempty"`
	EmptyTx   string       `json:"emptyTx,omitempty"`
	Pats      []cePatRowSt `json:"pats,omitempty"`
	RenameLbl string       `json:"renameLbl,omitempty"`
	Note      string       `json:"note,omitempty"`
}

func cePatMgrHTML(st cePatMgrSt) string {
	if zigui.Available() {
		if h, ok := zigui.RenderDlgPatMgr(stateJSON(st)); ok {
			return h
		}
	}
	return cePatMgrHTMLOf(st)
}

func cePatMgrHTMLOf(st cePatMgrSt) string {
	if st.Gone {
		return modal(st.Title, hint("bad", st.GoneTx), "")
	}
	var b strings.Builder
	if st.HasEmpty {
		b.WriteString(`<div class=set-note>` + esc(st.EmptyTx) + `</div>`)
	}
	for _, p := range st.Pats {
		b.WriteString(`<div class=pb-label>` + esc(p.Name) + `</div><div class=set-note>` + esc(p.Meta) + `</div>`)
		b.WriteString(`<form data-act=` + attrQ("ce-pat-rename:"+p.ID) + ` class=lib-toolbar>` +
			labeledInputDL("name", "", "", p.Name) +
			`<button class="rp-btn rp-btn--outline" type=submit>` + esc(st.RenameLbl) + `</button></form>`)
		ow := btnGated(p.OwLbl, p.OwWhy)
		if !p.OwGated {
			ow = btn(p.OwLbl, "outline", "ce-pat-ow:"+p.ID, "")
		}
		b.WriteString(btnRow(ow, btn(p.DelLbl, "destructive", "ce-pat-del:"+p.ID, "")))
	}
	b.WriteString(`<div class=set-note>` + esc(st.Note) + `</div>`)
	return modal(st.Title, b.String(), "")
}

// labeledInputDL is labeledInput with a caller-resolved data-label (the Zig render path keeps
// Unicode strings.ToLower in Go) - ONE markup source for both renderers.
func labeledInputDL(name, label, dataLabel, val string) string {
	return `<div class=pb-field data-label=` + attrQ(dataLabel) + `><div class=pb-label>` + html.EscapeString(label) + `</div>` +
		`<input class=field-input name="` + html.EscapeString(name) + `" value="` + html.EscapeString(val) + `"></div>`
}
