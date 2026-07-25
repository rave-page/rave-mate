package webui

import (
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/transcode"
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
	return toggleRowDL(label, strings.ToLower(label), act, on)
}

// toggleRowDL is toggleRow with a caller-resolved data-label (the Zig state path keeps
// Unicode ToLower in Go).
func toggleRowDL(label, dataLabel, act string, on bool) string {
	checked := ""
	if on {
		checked = " checked"
	}
	return fmt.Sprintf(`<label class=row data-label=%s><span class=row-label>%s</span>`+
		`<span class=switch><input type=checkbox%s data-act=%s data-value=%s>`+
		`<span class=switch-track></span></span></label>`,
		attrQ(dataLabel), html.EscapeString(label), checked, attrQ(act), attrQ(boolStr(on)))
}

// toggleRowGated renders a disabled switch + a warn hint naming what to install to
// unlock it. Same rule as btnGated: gated controls stay visible, greyed, explained.
func toggleRowGated(label string, on bool, gateHint string) string {
	return toggleRowGatedDL(label, strings.ToLower(label), on, gateHint)
}

// toggleRowTip is toggleRow with a tooltip (pre-rendered, e.g. tipTopic) beside the label.
func toggleRowTip(label, act string, on bool, tipHTML string) string {
	return toggleRowTipDL(label, strings.ToLower(label), act, on, tipHTML)
}

// toggleRowTipDL is toggleRowTip with a caller-resolved data-label (Zig state path).
func toggleRowTipDL(label, dataLabel, act string, on bool, tipHTML string) string {
	checked := ""
	if on {
		checked = " checked"
	}
	return fmt.Sprintf(`<label class=row data-label=%s><span class=row-label>%s%s</span>`+
		`<span class=switch><input type=checkbox%s data-act=%s data-value=%s>`+
		`<span class=switch-track></span></span></label>`,
		attrQ(dataLabel), html.EscapeString(label), tipHTML, checked, attrQ(act), attrQ(boolStr(on)))
}

// fieldEx is the general labelled text/number input (dispatch on change via act): optional
// placeholder + optional pre-rendered tooltip beside the label. field/fieldPH/fieldTip are the
// shorthands - extend HERE rather than growing a fourth near-copy.
func fieldEx(label, act, value, inputType, placeholder, tipHTML string) string {
	return fieldExDL(label, strings.ToLower(label), act, value, inputType, placeholder, tipHTML)
}

// field renders a labelled text/number input inside a form-less row (dispatch on change via act).
func field(label, act, value, inputType string) string {
	return fieldEx(label, act, value, inputType, "", "")
}

// fieldPH is field with a placeholder (shown greyed when the value is empty - e.g. a detected
// default path the user can accept by leaving the field blank).
func fieldPH(label, act, value, inputType, placeholder string) string {
	return fieldEx(label, act, value, inputType, placeholder, "")
}

// fieldTip is field with a tooltip (pre-rendered, e.g. tipTopic) beside the label.
func fieldTip(label, act, value, inputType, tipHTML string) string {
	return fieldEx(label, act, value, inputType, "", tipHTML)
}

// ssLabelSt is a smart-select ss-label as state: escaped label text + its structured tooltip
// (tooltip.go tipSt). THE one markup source for the label span - the Go renderer below and
// components.zig ssLabel mirror it. Was pre-rendered markup crossing as a raw string until B-1b.
type ssLabelSt struct {
	Text string `json:"text"`
	Tip  *tipSt `json:"tip,omitempty"`
}

func (l ssLabelSt) html() string {
	return `<span class=ss-label>` + html.EscapeString(l.Text) + tipOr(l.Tip, "") + `</span>`
}

// ssSelHTML renders a resolved smart select with the ss-label dual-field bridge: a STRUCTURED
// label wins, else a legacy pre-rendered one, else the plain label the select state carries.
// Zig twin: components.zig selectBoxTipOf / selectBoxRaw / selectBox, dispatched the same way.
func ssSelHTML(s selState, lbl *ssLabelSt, labelHTML string) string {
	if lbl != nil {
		return selHTMLRaw(s, lbl.html())
	}
	if labelHTML != "" {
		return selHTMLRaw(s, labelHTML)
	}
	return selHTML(s)
}

// ssOptsOf maps a [][2]string option table to the smart-select opts fn.
func ssOptsOf(options [][2]string) func() []ssOpt {
	return func() []ssOpt {
		out := make([]ssOpt, 0, len(options))
		for _, op := range options {
			out = append(out, ssOpt{Val: op[0], Label: op[1]})
		}
		return out
	}
}

// selectBoxTip is selectBox with a tooltip (topic id) beside the label.
func selectBoxTip(label, act string, options [][2]string, current, topic string) string {
	s, lbl := resolveSelectBoxTip(label, act, options, current, topic)
	return selHTMLRaw(s, lbl.html())
}

// resolveSelectBoxTip registers + resolves a selectBoxTip into pure render state plus its
// structured ss-label (label text + tooltip) for a Zig-migrated tab. selHTMLRaw pairs them.
func resolveSelectBoxTip(label, act string, options [][2]string, current, topic string) (selState, ssLabelSt) {
	id := strings.NewReplacer(":", "-", "/", "-", " ", "-").Replace(act)
	ssRegister(id, act, current, ssOptsOf(options))
	return ssResolve(id), ssLabelSt{Text: label, Tip: tipTopicSt(topic)}
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
func kv(label, value string) string { return kvDL(label, strings.ToLower(label), value) }

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
	return progressBarStr(progressPct(frac), caption)
}

// progressPct clamps frac to 0..1 and formats progressBar's fill width ("%.1f%%").
// (render_live.go's pbarPct is a different contract: 0..100, "%.2f%%".)
func progressPct(frac float64) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	return fmt.Sprintf("%.1f%%", frac*100)
}

// progressBarStr is progressBar with a pre-formatted fill width - the resolved-state twin
// for Zig-migrated tabs (floats never cross the ABI). ONE markup source for both paths.
func progressBarStr(pct, caption string) string {
	cap := caption
	if cap == "" {
		cap = pct
	}
	return `<div class=pbar><div class=pbar-fill style="width:` + pct + `"></div><span class=pbar-cap>` + html.EscapeString(cap) + `</span></div>`
}

// statusRow renders a status dot + label + muted sub-line (the per-card live status pattern).
func statusRow(variant, label, line string) string {
	return statusRowDL(variant, label, strings.ToLower(label), line)
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

// ── loudness block (shared by every surface that edits transcode loudness) ──

// loudnessVals are the four EBU R128 knobs of transcode's loudness block.
type loudnessVals struct {
	On        bool
	I         float64 // integrated target (LUFS); 0 = transcode.DefaultLoudnessI
	TP        float64 // true-peak ceiling (dBTP); 0 = transcode.DefaultLoudnessTP
	RaiseOnly bool
}

// loudnessOpts parameterizes loudnessFields for one surface.
type loudnessOpts struct {
	act       func(string) string // field suffix ("loudon"|"loudi"|"loudtp"|"loudraise") → this surface's act token
	toggleLbl string              // the switch's label
	topic     string              // tooltip.go topic beside the switch
	vals      loudnessVals
	// override marks a surface that layers over a resolved preset instead of defining one: 0 means
	// "unset, inherit the default", so the targets render blank behind a default placeholder
	// rather than as a literal "0" the user never chose.
	override bool
	// preset is the preset the run will really use, or nil when the surface can't resolve one (a
	// louder error already says so - don't blame the codec). Normalizing needs an audio re-encode,
	// so transcode drops it for copy/none: say so rather than show targets that do nothing.
	preset *transcode.Preset
	// compact renders the dense single-surface variant: industry-target quick-pick chips
	// (act "loudtarget", val "<I>|<TP>") + inline I/TP fields + raise-only chip on one wrap
	// row - instead of the full-width stacked builder fields.
	compact bool
	// extraHTML is injected inside the block while ON (the export surface's live gain-plan
	// line + pre-listen toggle ride here so they collapse with the switch).
	extraHTML string
}

// loudChipSt is one industry-target quick-pick chip (compact layout only). Val/Title/Label are
// final strings - the "%g|%g" I|TP payload, the full target label and the compressed chip text
// are all formatted Go-side (no float crosses the ABI as text Zig would have to format).
type loudChipSt struct {
	Label  string `json:"label"`
	Val    string `json:"val"`
	Title  string `json:"title"`
	Active bool   `json:"active,omitempty"`
}

// loudSt is THE loudness block as resolved state (phase B-1a: it used to ride through 4 state
// contracts as pre-rendered raw markup). Every string is final: i18n resolved, floats trimmed,
// data-labels lowercased Go-side. Toggle.On gates the whole body - the same single source Go
// always used (o.vals.On drives both the switch and the branch). The tooltip is STRUCTURED since
// phase B-1b (TipS, dual-field bridge over the legacy raw Tip); Extra stays RAW - the caller owns
// extraHTML.
type loudSt struct {
	Compact bool         `json:"compact,omitempty"`
	Toggle  uiToggle     `json:"toggle"`
	Tip     string       `json:"tip"`             // legacy RAW pre-rendered tooltip markup (bridge)
	TipS    *tipSt       `json:"tipSt,omitempty"` // structured tooltip - wins over Tip
	ChipAct string       `json:"chipAct,omitempty"`
	Chips   []loudChipSt `json:"chips,omitempty"`
	IField  libPBFieldSt `json:"iField"`
	TPField libPBFieldSt `json:"tpField"`
	Raise   uiToggle     `json:"raise"`
	HasWarn bool         `json:"hasWarn,omitempty"`
	Warn    string       `json:"warn,omitempty"`
	Extra   string       `json:"extra,omitempty"` // RAW (caller's extraHTML)
}

// loudnessFields renders THE loudness block: the normalize switch plus integrated target,
// true-peak ceiling and raise-quiet-only behind it. Every surface that edits transcode loudness
// renders this one implementation - library preset builder, automation transcode steps, recordings
// export. Extend HERE; never fork a per-surface copy. Only markup + copy are shared: the four
// field handlers stay the caller's, reached through o.act.
func loudnessFields(o loudnessOpts) string { return newLoudSt(o).html() }

// newLoudSt resolves loudnessOpts into the structured block. Surfaces that render through Zig
// carry THIS instead of the markup; the Go renderer below is the fallback + golden reference.
func newLoudSt(o loudnessOpts) loudSt {
	// An override surface shows the default as a placeholder, so blank reads as "inherit"; a
	// surface that defines the preset has no default to fall back to - print the value.
	tx := func(f, def float64) (val, ph string) {
		if !o.override {
			return trimNum(f), ""
		}
		if f == 0 {
			return "", trimNum(def)
		}
		return trimNum(f), trimNum(def)
	}
	st := loudSt{
		Compact: o.compact,
		Toggle:  newToggle(o.toggleLbl, o.act("loudon"), o.vals.On),
		TipS:    tipTopicSt(o.topic),
		Extra:   o.extraHTML,
	}
	if !o.vals.On {
		return st
	}
	iv, iph := tx(o.vals.I, transcode.DefaultLoudnessI)
	tv, tph := tx(o.vals.TP, transcode.DefaultLoudnessTP)
	iHint := ""
	if !o.compact {
		iHint = i18n.T("library.enc.lufsHint")
	}
	st.IField = newPBField(i18n.T("library.enc.lufsTarget"), o.act("loudi"), iv, "number", iHint)
	st.IField.PH = iph
	st.TPField = newPBField(i18n.T("library.enc.truePeak"), o.act("loudtp"), tv, "number", "")
	st.TPField.PH = tph
	st.Raise = newToggle(i18n.T("library.enc.raiseQuiet"), o.act("loudraise"), o.vals.RaiseOnly)
	if o.compact {
		// quick-pick target chips: one tap sets I+TP to an industry target; the active
		// chip mirrors the current I (unset = the default −14)
		effI := o.vals.I
		if o.override && effI == 0 {
			effI = transcode.DefaultLoudnessI
		}
		st.ChipAct = o.act("loudtarget")
		for _, lt := range transcode.LoudnessTargets() {
			st.Chips = append(st.Chips, loudChipSt{
				Label:  ltChipLabel(lt),
				Val:    fmt.Sprintf("%g|%g", lt.I, lt.TP),
				Title:  lt.Label,
				Active: math.Abs(effI-lt.I) < 0.01,
			})
		}
	}
	if o.preset != nil && !transcode.LoudnessAppliesTo(o.preset.AudioCodec) {
		st.HasWarn, st.Warn = true, i18n.T("library.enc.loudNeedsReencode")
	}
	return st
}

// html renders the resolved block - ONE markup source for the Go path and the golden reference
// the Zig mirror (components.zig loudnessFields) must match byte-for-byte.
func (l loudSt) html() string {
	var b strings.Builder
	grp := "pb-grp"
	if l.Compact {
		grp = "pb-grp pb-grp--compact"
	}
	b.WriteString(`<div class="` + grp + `">`)
	b.WriteString(toggleRowTipDL(l.Toggle.Label, l.Toggle.DL, l.Toggle.Act, l.Toggle.On, tipOr(l.TipS, l.Tip)))
	if l.Toggle.On {
		if l.Compact {
			b.WriteString(`<div class=lt-chips>`)
			for _, ch := range l.Chips {
				cls := "lt-chip"
				if ch.Active {
					cls += " active"
				}
				b.WriteString(`<button class="` + cls + `" data-act=` + attrQ(l.ChipAct) +
					` data-val=` + attrQ(ch.Val) + ` title=` + attrQ(ch.Title) + `>` +
					html.EscapeString(ch.Label) + `</button>`)
			}
			b.WriteString(`</div>`)
			b.WriteString(`<div class=lt-fields>` +
				`<span class=lt-field>` + l.IField.html() + `</span>` +
				`<span class=lt-field>` + l.TPField.html() + `</span>` +
				`<span class=lt-raise>` + l.Raise.html() + `</span></div>`)
		} else {
			b.WriteString(l.IField.html())
			b.WriteString(l.TPField.html())
			b.WriteString(l.Raise.html())
		}
		if l.HasWarn {
			b.WriteString(hint("warn", l.Warn))
		}
		b.WriteString(l.Extra)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// ltChipLabel compresses a LoudnessTarget to chip size: "−14 Streaming", "−23 EBU", …
func ltChipLabel(lt transcode.LoudnessTarget) string {
	name := lt.Label
	if i := strings.IndexByte(name, ' '); i > 0 {
		name = name[:i]
	}
	switch {
	case strings.Contains(lt.Label, "Streaming"):
		name = "Streaming"
	case strings.Contains(lt.Label, "Apple"):
		name = "Apple"
	case strings.Contains(lt.Label, "Deezer"):
		name = "Deezer"
	case strings.Contains(lt.Label, "ReplayGain"):
		name = "RG2"
	case strings.Contains(lt.Label, "EBU"):
		name = "EBU"
	case strings.Contains(lt.Label, "Club"):
		name = "Club"
	}
	return fmt.Sprintf("%g %s", lt.I, name)
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

// --- vrchat/worlds (zigui port) ---
// Caller-resolved data-label variants (like toggleRowDL): the Zig render path keeps Unicode
// strings.ToLower in Go, so the pure renderers get the data-label handed to them. The
// label-lowering wrappers above delegate here - ONE markup source per component.

// kvDL is kv with a caller-resolved data-label.
func kvDL(label, dataLabel, value string) string {
	return `<div class=kv><span class=kv-k>` + html.EscapeString(label) + `</span>` +
		`<span class=kv-v data-label=` + attrQ(dataLabel) + ` data-value=` + attrQ(value) + `>` + html.EscapeString(value) + `</span></div>`
}

// statusRowDL is statusRow with a caller-resolved data-label.
func statusRowDL(variant, label, dataLabel, line string) string {
	return `<div class=strow>` + dot(variant) + `<div class=strow-tx><div class=strow-l data-label=` +
		attrQ(dataLabel) + `>` + html.EscapeString(label) + `</div>` +
		`<div class=strow-s data-value=` + attrQ(line) + `>` + html.EscapeString(line) + `</div></div></div>`
}

// --- settings (zigui port) ---

// toggleRowGatedDL is toggleRowGated with a caller-resolved data-label.
func toggleRowGatedDL(label, dataLabel string, on bool, gateHint string) string {
	checked := ""
	if on {
		checked = " checked"
	}
	return fmt.Sprintf(`<label class="row row--gated" data-label=%s><span class=row-label>%s</span>`+
		`<span class=switch><input type=checkbox%s disabled><span class=switch-track></span></span></label>`,
		attrQ(dataLabel), html.EscapeString(label), checked) +
		`<div class=set-gate>` + hint("warn", gateHint) + `</div>`
}

// fieldExDL is fieldEx with a caller-resolved data-label.
func fieldExDL(label, dataLabel, act, value, inputType, placeholder, tipHTML string) string {
	if inputType == "" {
		inputType = "text"
	}
	ph := ""
	if placeholder != "" {
		ph = ` placeholder=` + attrQ(placeholder)
	}
	return fmt.Sprintf(`<label class=field data-label=%s><span class=field-label>%s%s</span>`+
		`<input class=field-input type=%s value=%s data-value=%s data-act=%s%s></label>`,
		attrQ(dataLabel), html.EscapeString(label), tipHTML, inputType, attrQ(value), attrQ(value), attrQ(act), ph)
}
