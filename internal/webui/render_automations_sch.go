package webui

import "strings"

// Automations SCHEDULE-editor dialog state + pure renderer (wave-4 dialog sweep B). The impure
// half stays in automations_schedules.go (working copy under s.mu, the automation snapshot, the
// engine cron validator, smart-select registration, tipTopic markup, the GOOS platform checks);
// this renderer is mirrored in native/zigui/src/dialogs_b.zig
// (gate: zigui_golden_dialogs_b_test.go).
//
// The three parts are block lists (same aeBlockSt kit as the automation editor), so trigger kinds
// that read different fields stay data rather than branching markup.

// asModalSt is the schedule-editor dialog.
type asModalSt struct {
	Title      string      `json:"title"`
	HasErr     bool        `json:"hasErr,omitempty"`
	Err        string      `json:"err,omitempty"`
	Head       []aeBlockSt `json:"head,omitempty"` // label · automation picker · enabled · warnings
	SecTrigger string      `json:"secTrigger"`
	Trigger    []aeBlockSt `json:"trigger,omitempty"`
	SecGates   string      `json:"secGates"`
	Gates      []aeBlockSt `json:"gates,omitempty"`
	Save       string      `json:"save"`
	Cancel     string      `json:"cancel"`
}

// asModalHTMLOf is the pure schedule-editor renderer.
func asModalHTMLOf(st asModalSt) string {
	var b strings.Builder
	if st.HasErr {
		b.WriteString(`<div class=ae-err>` + hint("bad", st.Err) + `</div>`)
	}
	b.WriteString(aeBlocksHTML(st.Head))
	b.WriteString(section(st.SecTrigger, aeBlocksHTML(st.Trigger)))
	b.WriteString(section(st.SecGates, aeBlocksHTML(st.Gates)))
	footer := btnRow(btn(st.Save, "primary", "auto-sch-save", ""), btn(st.Cancel, "ghost", "modal-close", ""))
	return modal(st.Title, b.String(), footer)
}
