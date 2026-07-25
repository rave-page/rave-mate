package webui

import (
	"strconv"
	"strings"
)

// Automation-editor DIALOG state + pure renderers (wave-4 dialog sweep B). The impure half stays
// in automations_editor.go (working copy under s.mu, preset merge, engine validators, smart-select
// registration, tipTopic markup); everything below is pure and mirrored in
// native/zigui/src/dialogs_b.zig (gate: zigui_golden_dialogs_b_test.go).
//
// The form body is a BLOCK LIST (same shape as the settings port's setBlock): each per-type step
// body is data, not HTML, so both renderers walk the same tree through the same components.go
// primitives. Depth is 1 by construction - a block carries at most two fields plus one button.
//
// Trusted raw fields: every `tip` (tipTopic markup, tooltip.go) and a step's `raw` block, which is
// another renderer's output (components.go loudnessFields - the shared loudness override block,
// float-formatted Go-side like every other pre-rendered fragment in the fleet).

// dlgFieldSt is a fieldEx() call as state; json tags match components.zig `Field` exactly
// (uiField in render_media_shared.go carries no tip, and that shape is the media batch's).
type dlgFieldSt struct {
	Label string `json:"label"`
	DL    string `json:"dl"`
	Act   string `json:"act"`
	Value string `json:"value"`
	Type  string `json:"inputType"`
	PH    string `json:"ph"`
	Tip   string `json:"tip"` // RAW pre-rendered tooltip markup
}

func newDlgField(label, act, value, inputType, ph, tip string) dlgFieldSt {
	return dlgFieldSt{Label: label, DL: strings.ToLower(label), Act: act, Value: value,
		Type: inputType, PH: ph, Tip: tip}
}

func (f dlgFieldSt) html() string {
	return fieldExDL(f.Label, f.DL, f.Act, f.Value, f.Type, f.PH, f.Tip)
}

// Block kinds. A form block renders exactly one components.go primitive (or one small wrapper
// around a pair), so the two renderers can never diverge on layout.
const (
	aeBlkField   = "field"   // fieldEx
	aeBlkFPair   = "fpair"   // fpair(field, field)
	aeBlkToolbar = "toolbar" // <div class=lib-toolbar> field + Browse button
	aeBlkToggle  = "toggle"  // toggleRow
	aeBlkSelect  = "select"  // selHTML (smart select, resolved)
	aeBlkHint    = "hint"    // hint(tone, text)
	aeBlkPBHint  = "pbhint"  // <div class=pb-hint> escaped text + raw tip
	aeBlkRaw     = "raw"     // another renderer's markup (loudness block)
)

// aeBlockSt is one form block. Only the fields its Kind names are populated.
type aeBlockSt struct {
	Kind   string     `json:"kind"`
	Field  dlgFieldSt `json:"field,omitempty"`
	Field2 dlgFieldSt `json:"field2,omitempty"`
	Btn    uiBtn      `json:"btn,omitempty"`
	Toggle uiToggle   `json:"toggle,omitempty"`
	Sel    selState   `json:"sel,omitempty"`
	Tone   string     `json:"tone,omitempty"`
	Text   string     `json:"text,omitempty"`
	Tip    string     `json:"tip,omitempty"` // RAW
	Raw    string     `json:"raw,omitempty"` // RAW
}

// aeStepSt is one chain-step card: header (order + type label + reorder/remove) then its body.
type aeStepSt struct {
	Title  string      `json:"title"` // "<n>. <type label>"
	Trail  []uiBtn     `json:"trail,omitempty"`
	Desc   string      `json:"desc"`
	Blocks []aeBlockSt `json:"blocks,omitempty"`
}

// aeModalSt is the whole automation-editor dialog.
type aeModalSt struct {
	Title      string      `json:"title"`
	HasErr     bool        `json:"hasErr,omitempty"`
	Err        string      `json:"err,omitempty"`
	Ident      []aeBlockSt `json:"ident,omitempty"`
	SecMatch   string      `json:"secMatch"`
	Match      []aeBlockSt `json:"match,omitempty"`
	SecActions string      `json:"secActions"`
	NoSteps    bool        `json:"noSteps,omitempty"`
	NoStepsMsg string      `json:"noStepsMsg,omitempty"`
	Steps      []aeStepSt  `json:"steps,omitempty"`
	Add        []uiBtn     `json:"add,omitempty"`
	HasVerdict bool        `json:"hasVerdict,omitempty"` // engine validators rejected the chain
	Verdict    string      `json:"verdict,omitempty"`
	Save       string      `json:"save"`
	Cancel     string      `json:"cancel"`
}

// aeBlockHTML renders one form block through the components.go primitive its Kind names.
func aeBlockHTML(b aeBlockSt) string {
	switch b.Kind {
	case aeBlkField:
		return b.Field.html()
	case aeBlkFPair:
		return fpair(b.Field.html(), b.Field2.html())
	case aeBlkToolbar:
		return `<div class=lib-toolbar>` + b.Field.html() + b.Btn.html() + `</div>`
	case aeBlkToggle:
		return b.Toggle.html()
	case aeBlkSelect:
		return selHTML(b.Sel)
	case aeBlkHint:
		return hint(b.Tone, b.Text)
	case aeBlkPBHint:
		return `<div class=pb-hint>` + htmlEscape(b.Text) + b.Tip + `</div>`
	case aeBlkRaw:
		return b.Raw
	}
	return ""
}

// aeBlocksHTML renders a block list in order.
func aeBlocksHTML(bs []aeBlockSt) string {
	var b strings.Builder
	for _, x := range bs {
		b.WriteString(aeBlockHTML(x))
	}
	return b.String()
}

// aeStepHTMLOf renders one step card: type description then the per-type body.
func aeStepHTMLOf(st aeStepSt) string {
	trail := ""
	for _, t := range st.Trail {
		trail += t.html()
	}
	return card(st.Title, trail, `<div class=np-artist>`+htmlEscape(st.Desc)+`</div>`+aeBlocksHTML(st.Blocks))
}

// aeChainHTMLOf renders the ordered steps + add palette + the live engine verdict.
func aeChainHTMLOf(st aeModalSt) string {
	var b strings.Builder
	if st.NoSteps {
		b.WriteString(emptyState(st.NoStepsMsg))
	}
	for _, s := range st.Steps {
		b.WriteString(aeStepHTMLOf(s))
	}
	b.WriteString(uiBtnRow(st.Add))
	if st.HasVerdict {
		b.WriteString(hint("bad", st.Verdict))
	}
	return b.String()
}

// aeModalHTMLOf is the pure automation-editor renderer.
func aeModalHTMLOf(st aeModalSt) string {
	var b strings.Builder
	if st.HasErr {
		b.WriteString(`<div class=ae-err>` + hint("bad", st.Err) + `</div>`)
	}
	b.WriteString(aeBlocksHTML(st.Ident))
	b.WriteString(section(st.SecMatch, aeBlocksHTML(st.Match)))
	b.WriteString(section(st.SecActions, aeChainHTMLOf(st)))
	footer := btnRow(btn(st.Save, "primary", "auto-ed-save", ""), btn(st.Cancel, "ghost", "modal-close", ""))
	return modal(st.Title, b.String(), footer)
}

// aeItoa is strconv.Itoa for the step-key acts (uint64 keys, Go %d parity).
func aeItoa(v uint64) string { return strconv.FormatUint(v, 10) }
