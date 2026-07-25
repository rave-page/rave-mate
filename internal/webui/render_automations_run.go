package webui

import "strings"

// Automations "Run now" DIALOG state + pure renderer (wave-4 dialog sweep B). The impure half
// stays in automations_runnow.go (the modal's own chain copy under s.mu, run-in-flight tracking,
// i18n, tipTopic markup); this renderer is mirrored in native/zigui/src/dialogs_b.zig
// (gate: zigui_golden_dialogs_b_test.go).
//
// Erases is explicit (autoChainDeletes resolved Go-side) and gates BOTH the acknowledgement block
// and the footer wording - never inferred from an empty string.

// arFootSt is the resolved footer. Gated = the Run button is disabled with Why in its title
// (btnGated: name the missing precondition, never hide the control).
type arFootSt struct {
	Gated   bool   `json:"gated,omitempty"`
	Label   string `json:"label"`
	Why     string `json:"why,omitempty"`
	Variant string `json:"variant,omitempty"` // live button variant (primary / destructive)
	Cancel  string `json:"cancel"`
}

// arModalSt is the run-now dialog.
type arModalSt struct {
	Title        string     `json:"title"`
	HasErr       bool       `json:"hasErr,omitempty"`
	Err          string     `json:"err,omitempty"`
	Auto         uiKV       `json:"auto"`
	Watch        uiKV       `json:"watch"`
	Chain        uiKV       `json:"chain"`
	IgnoresMatch string     `json:"ignoresMatch"`
	File         dlgFieldSt `json:"file"`
	Browse       uiBtn      `json:"browse"`
	Erases       bool       `json:"erases,omitempty"` // the chain ends by deleting the file
	DeleteWarn   string     `json:"deleteWarn,omitempty"`
	DeleteScope  string     `json:"deleteScope,omitempty"`
	DeleteTip    string     `json:"deleteTip,omitempty"` // RAW tipTopic markup
	Ack          uiToggle   `json:"ack,omitempty"`
	Foot         arFootSt   `json:"foot"`
}

// arModalHTMLOf is the pure run-now renderer.
func arModalHTMLOf(st arModalSt) string {
	var b strings.Builder
	if st.HasErr {
		b.WriteString(`<div class=ae-err>` + hint("bad", st.Err) + `</div>`)
	}
	b.WriteString(st.Auto.html())
	b.WriteString(st.Watch.html())
	b.WriteString(st.Chain.html())
	// The rule that does NOT apply here, stated before the file is chosen.
	b.WriteString(hint("info", st.IgnoresMatch))
	b.WriteString(`<div class=lib-toolbar>` + st.File.html() + st.Browse.html() + `</div>`)
	if st.Erases {
		// The chain ends by erasing the file. Run now skips the match rules, so nothing else stands
		// between this button and a permanent delete - make the user say it out loud.
		b.WriteString(hint("bad", st.DeleteWarn))
		b.WriteString(`<div class=pb-hint>` + htmlEscape(st.DeleteScope) + st.DeleteTip + `</div>`)
		b.WriteString(st.Ack.html())
	}
	return modal(st.Title, b.String(), arFooterHTMLOf(st.Foot))
}

// arFooterHTMLOf renders the gated-or-live Run button plus Cancel.
func arFooterHTMLOf(f arFootSt) string {
	cancel := btn(f.Cancel, "ghost", "modal-close", "")
	if f.Gated {
		return btnRow(btnGated(f.Label, f.Why), cancel)
	}
	return btnRow(btn(f.Label, f.Variant, "auto-run-go", ""), cancel)
}
