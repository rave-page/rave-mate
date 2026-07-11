package webui

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// Reusable HTML component helpers built on the rave.page .rp-* kit. Every tab renderer uses these
// so styling + action wiring stay consistent. Dynamic text is always escaped.

// btn renders an rp-btn. variant ∈ {primary,go,explore,warn,destructive,outline,secondary,ghost}.
// act/val become data-act/data-val (dispatched to Go); empty act = a plain (non-action) button.
func btn(label, variant, act, val string) string {
	v := variant
	if v == "" {
		v = "outline"
	}
	attrs := ""
	if act != "" {
		// html.EscapeString (NOT %q) so compound args round-trip through the DOM: %q renders a
		// 0x1f separator / backslash / quote as literal escaped text that getAttribute can't undo.
		attrs = ` data-act="` + html.EscapeString(act) + `"`
		if val != "" {
			attrs += ` data-val="` + html.EscapeString(val) + `"`
		}
	}
	return fmt.Sprintf(`<button class="rp-btn rp-btn--%s"%s>%s</button>`, v, attrs, html.EscapeString(label))
}

// btnGated renders a disabled button whose title names the missing dependency. Use for
// controls gated on a component install - grey them out with a hint, never hide them.
func btnGated(label, why string) string {
	return `<button class="rp-btn rp-btn--outline" disabled title=` + attrQ(why) + `>` +
		html.EscapeString(label) + `</button>`
}

// badge renders an rp-badge. variant ∈ {default,success,info,warning,error,secondary,outline}.
func badge(text, variant string) string {
	if variant == "" {
		variant = "secondary"
	}
	return fmt.Sprintf(`<span class="rp-badge rp-badge--%s">%s</span>`, variant, html.EscapeString(text))
}

// dot renders a small status dot (color via variant → CSS var).
func dot(variant string) string {
	return fmt.Sprintf(`<span class="dot dot--%s"></span>`, variant)
}

// section wraps a titled block.
func section(title, bodyHTML string) string {
	return `<section class=sec><h2 class=sec-title>` + html.EscapeString(title) + `</h2>` + bodyHTML + `</section>`
}

// panel is a titled page header (page-title + optional subtitle).
func panel(title, sub string) string {
	s := `<h1 class=page-title>` + html.EscapeString(title) + `</h1>`
	if sub != "" {
		s += `<p class=page-sub>` + html.EscapeString(sub) + `</p>`
	}
	return s
}

// toggleRow renders a labelled switch. on = current state; on 'change' the runtime dispatches act
// with val = the checkbox's new bool ("true"/"false"). data-label makes it ctl-readable/settable.
func toggleRow(label, act string, on bool) string {
	checked := ""
	if on {
		checked = " checked"
	}
	return fmt.Sprintf(`<label class=row data-label=%s><span class=row-label>%s</span>`+
		`<span class=switch><input type=checkbox%s data-act=%s data-value=%s>`+
		`<span class=switch-track></span></span></label>`,
		attrQ(strings.ToLower(label)), html.EscapeString(label), checked, attrQ(act), attrQ(boolStr(on)))
}

// toggleRowGated renders a disabled switch + a warn hint naming what to install to
// unlock it. Same rule as btnGated: gated controls stay visible, greyed, explained.
func toggleRowGated(label string, on bool, gateHint string) string {
	checked := ""
	if on {
		checked = " checked"
	}
	return fmt.Sprintf(`<label class="row row--gated" data-label=%s><span class=row-label>%s</span>`+
		`<span class=switch><input type=checkbox%s disabled><span class=switch-track></span></span></label>`,
		attrQ(strings.ToLower(label)), html.EscapeString(label), checked) +
		`<div class=set-gate>` + hint("warn", gateHint) + `</div>`
}

// toggleRowTip is toggleRow with a tooltip (pre-rendered, e.g. tipTopic) beside the label.
func toggleRowTip(label, act string, on bool, tipHTML string) string {
	checked := ""
	if on {
		checked = " checked"
	}
	return fmt.Sprintf(`<label class=row data-label=%s><span class=row-label>%s%s</span>`+
		`<span class=switch><input type=checkbox%s data-act=%s data-value=%s>`+
		`<span class=switch-track></span></span></label>`,
		attrQ(strings.ToLower(label)), html.EscapeString(label), tipHTML, checked, attrQ(act), attrQ(boolStr(on)))
}

// field renders a labelled text/number input inside a form-less row (dispatch on change via act).
func field(label, act, value, inputType string) string {
	if inputType == "" {
		inputType = "text"
	}
	return fmt.Sprintf(`<label class=field data-label=%s><span class=field-label>%s</span>`+
		`<input class=field-input type=%s value=%s data-value=%s data-act=%s></label>`,
		attrQ(strings.ToLower(label)), html.EscapeString(label), inputType, attrQ(value), attrQ(value), attrQ(act))
}

// fieldPH is field with a placeholder (shown greyed when the value is empty - e.g. a detected
// default path the user can accept by leaving the field blank).
func fieldPH(label, act, value, inputType, placeholder string) string {
	if inputType == "" {
		inputType = "text"
	}
	ph := ""
	if placeholder != "" {
		ph = ` placeholder=` + attrQ(placeholder)
	}
	return fmt.Sprintf(`<label class=field data-label=%s><span class=field-label>%s</span>`+
		`<input class=field-input type=%s value=%s data-value=%s data-act=%s%s></label>`,
		attrQ(strings.ToLower(label)), html.EscapeString(label), inputType, attrQ(value), attrQ(value), attrQ(act), ph)
}

// fieldTip is field with a tooltip (pre-rendered, e.g. tipTopic) beside the label.
func fieldTip(label, act, value, inputType, tipHTML string) string {
	if inputType == "" {
		inputType = "text"
	}
	return fmt.Sprintf(`<label class=field data-label=%s><span class=field-label>%s%s</span>`+
		`<input class=field-input type=%s value=%s data-value=%s data-act=%s></label>`,
		attrQ(strings.ToLower(label)), html.EscapeString(label), tipHTML, inputType, attrQ(value), attrQ(value), attrQ(act))
}

// selectBoxTip is selectBox with a tooltip (topic id) beside the label.
func selectBoxTip(label, act string, options [][2]string, current, topic string) string {
	id := strings.NewReplacer(":", "-", "/", "-", " ", "-").Replace(act)
	lbl := `<span class=ss-label>` + html.EscapeString(label) + tipTopic(topic) + `</span>`
	return smartSelectRaw(id, lbl, act, current, func() []ssOpt {
		out := make([]ssOpt, 0, len(options))
		for _, op := range options {
			out = append(out, ssOpt{Val: op[0], Label: op[1]})
		}
		return out
	})
}

// emptyState renders the rp-empty placeholder.
func emptyState(msg string) string {
	return `<div class="rp-empty"><div class="rp-empty__title">` + html.EscapeString(msg) + `</div></div>`
}

// attrQ quotes s for use as an HTML attribute value: explicit double quotes +
// HTML-escaped content. NEVER use fmt %q for attributes - %q escapes for Go
// source (`"`→`\"`), which HTML ignores, so a quote in s breaks out of the
// attribute (XSS).
func attrQ(s string) string {
	return `"` + html.EscapeString(s) + `"`
}

// kv renders a key/value line (label muted, value emphasised, ctl-readable).
func kv(label, value string) string {
	return `<div class=kv><span class=kv-k>` + html.EscapeString(label) + `</span>` +
		`<span class=kv-v data-label=` + attrQ(strings.ToLower(label)) + ` data-value=` + attrQ(value) + `>` + html.EscapeString(value) + `</span></div>`
}

// btnRow groups buttons horizontally.
func btnRow(buttons ...string) string {
	return `<div class=btn-row>` + strings.Join(buttons, "") + `</div>`
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ── shared primitives (parity kit; every tab renderer builds on these) ──

// selectBox renders a filterable smart select (was a native <select>; same contract:
// picking dispatches act with val = selected value). ctl: read the button by its
// act-derived data-label; set = click open → set <id>-flt → click option.
func selectBox(label, act string, options [][2]string, current string) string {
	id := strings.NewReplacer(":", "-", "/", "-", " ", "-").Replace(act)
	return smartSelect(id, label, act, current, func() []ssOpt {
		out := make([]ssOpt, 0, len(options))
		for _, op := range options {
			out = append(out, ssOpt{Val: op[0], Label: op[1]})
		}
		return out
	})
}

// slider renders a labelled range input. Live value display updates during drag (inline, display
// only - no business logic); the value dispatches to Go on release via change. unit = display suffix.
func slider(label, act string, min, max, step, val float64, unit string) string {
	oninput := `oninput='var b=this.parentNode.querySelector(".slider-val");if(b)b.textContent=this.value+` + jsQuote(unit) + `'`
	return fmt.Sprintf(`<label class=slider data-label=%s><span class=field-label>%s <b class=slider-val>%s%s</b></span>`+
		`<input class=slider-input type=range min=%s max=%s step=%s value=%s data-act=%s data-value=%s %s></label>`,
		attrQ(strings.ToLower(label)), html.EscapeString(label), trimNum(val), html.EscapeString(unit),
		trimNum(min), trimNum(max), trimNum(step), trimNum(val), attrQ(act), attrQ(trimNum(val)), oninput)
}

// progressBar renders a 0..1 fill with an optional caption (defaults to the percentage).
func progressBar(frac float64, caption string) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	pct := fmt.Sprintf("%.1f%%", frac*100)
	cap := caption
	if cap == "" {
		cap = pct
	}
	return `<div class=pbar><div class=pbar-fill style="width:` + pct + `"></div><span class=pbar-cap>` + html.EscapeString(cap) + `</span></div>`
}

// statusRow renders a status dot + label + muted sub-line (the per-card live status pattern).
func statusRow(variant, label, line string) string {
	return `<div class=strow>` + dot(variant) + `<div class=strow-tx><div class=strow-l data-label=` +
		attrQ(strings.ToLower(label)) + `>` + html.EscapeString(label) + `</div>` +
		`<div class=strow-s data-value=` + attrQ(line) + `>` + html.EscapeString(line) + `</div></div></div>`
}

// subTabs renders a segmented control. items = [][value,label]; each button's act = actPrefix+value.
func subTabs(actPrefix, active string, items ...[2]string) string {
	var b strings.Builder
	b.WriteString(`<div class=subtabs>`)
	for _, it := range items {
		cls := "subtab"
		if it[0] == active {
			cls += " active"
		}
		fmt.Fprintf(&b, `<button class="%s" data-act="%s" data-val="%s">%s</button>`, cls, html.EscapeString(actPrefix+it[0]), html.EscapeString(it[0]), html.EscapeString(it[1]))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// masterDetail lays out a scrollable list beside a detail pane (collapses to one column on mobile).
func masterDetail(listHTML, detailHTML string) string {
	return `<div class=mdsplit><div class=md-list>` + listHTML + `</div><div class=md-detail>` + detailHTML + `</div></div>`
}

// masterDetailWide flips the proportions: the list is the primary work surface
// (tables + filter rails), the detail a fixed-width right inspector. Use when the
// list carries the content and the detail is contextual (Library collection/browse).
func masterDetailWide(listHTML, detailHTML string) string {
	return `<div class="mdsplit wide"><div class=md-list>` + listHTML + `</div><div class=md-detail>` + detailHTML + `</div></div>`
}

// triPane: nav rail | primary list | detail inspector with user-draggable dividers.
// navVar/detailVar are :root CSS custom-property names the splitter JS (shell.go)
// writes + persists in localStorage, so widths survive re-renders and restarts.
func triPane(navHTML, listHTML, detailHTML, navVar, detailVar string) string {
	return `<div class="mdsplit wide tri" style="grid-template-columns:var(--` + navVar + `,220px) 6px minmax(0,1fr) 6px var(--` + detailVar + `,clamp(300px,28vw,400px))">` +
		`<div class=md-nav>` + navHTML + `</div>` +
		`<div class=split-h data-splitvar="` + navVar + `" data-splitdef=220></div>` +
		`<div class=md-list>` + listHTML + `</div>` +
		`<div class=split-h data-splitvar="` + detailVar + `" data-splitdef=340 data-splitdir=r></div>` +
		`<div class=md-detail>` + detailHTML + `</div></div>`
}

// itemRow renders a list row: title + optional sub-line on the left, trailing action buttons right.
func itemRow(title, sub string, trailing ...string) string {
	s := ""
	if sub != "" {
		s = `<div class=irow-sub>` + html.EscapeString(sub) + `</div>`
	}
	return `<div class=irow><div class=irow-main><div class=irow-title>` + html.EscapeString(title) + `</div>` + s + `</div>` +
		`<div class=irow-actions>` + strings.Join(trailing, "") + `</div></div>`
}

// hint renders a small dynamic-info chip (the electron local-studio "current media hint" pattern).
// tone ∈ {"", info, ok, warn, bad}.
func hint(tone, text string) string {
	if tone == "" {
		tone = "info"
	}
	return `<span class="hint hint--` + tone + `">` + html.EscapeString(text) + `</span>`
}

// modal renders a scrim + centered dialog card. Render it into the modal root via u.openModal.
// footerHTML defaults to a Close button; every close control uses data-act=modal-close.
func modal(title, bodyHTML, footerHTML string) string {
	f := footerHTML
	if f == "" {
		f = btn("Close", "outline", "modal-close", "")
	}
	return `<div class=modal-scrim data-act=modal-close></div>` +
		`<div class=modal role=dialog><div class=modal-head><h3 class=modal-title>` + html.EscapeString(title) + `</h3>` +
		`<button class=modal-x data-act=modal-close aria-label=Close>✕</button></div>` +
		`<div class=modal-body>` + bodyHTML + `</div><div class=modal-foot>` + f + `</div></div>`
}

// card wraps arbitrary body HTML in an rp-card with an optional uppercase title + trailing header slot.
func card(title, trailing, bodyHTML string) string {
	head := ""
	if title != "" || trailing != "" {
		head = `<div class=card-head><span class=card-h>` + html.EscapeString(title) + `</span><span class=card-trail>` + trailing + `</span></div>`
	}
	return `<div class="rp-card">` + head + bodyHTML + `</div>`
}

// trimNum formats a float with the least digits needed (no trailing zeros).
func trimNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// fpair puts two short fields/selects side by side (.fpair grid; stacks <560px).
func fpair(a, b string) string { return `<div class=fpair>` + a + b + `</div>` }

// actionMenu renders a compact "⋯ label" smart-select whose options dispatch their Val as an
// act (button-wall → dropdown). Handled by the shared "settings-menu" dispatcher; the first
// (placeholder) row is a no-op. id must be colon-free.
func actionMenu(id, label string, items []ssOpt) string {
	opts := append([]ssOpt{{Val: "", Label: "⋯ " + label}}, items...)
	return `<span class=act-menu>` + smartSelect(id, "", "settings-menu", "", func() []ssOpt { return opts }) + `</span>`
}
