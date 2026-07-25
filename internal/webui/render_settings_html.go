package webui

import (
	"html"
	"strings"
)

// Settings render state + the PURE renderers over it (golden reference for the Zig port in
// native/zigui/src/settings.zig — byte-identical, zigui_golden_settings_test.go). The impure
// half (config/service/probe reads, i18n.T, smart-select registration) lives in
// render_settings.go and hands the resolved state down.
//
// Card bodies are BLOCK LISTS, not HTML: every block renders through the shared components.go
// primitives (field/toggleRow/selectBox/btn/kv/itemRow/hint/emptyState), so the markup has
// exactly ONE Go source and the Zig renderer mirrors the same block walk. Bodies owned by
// other files (gridfix, gridfix model, account bridge, the update flow) ride as trusted raw
// HTML blocks — see the wave-3 seam list in the header of settings.zig.
//
// Tooltips are STRUCTURED (*tipSt, tooltip.go) since phase B1b; the raw `Tip` string stays as the
// dual-field bridge (tipOr) so un-migrated builders keep working.

// setStatusSt is a card's live status (stv resolved for JSON): variant + terse state line.
type setStatusSt struct {
	V string `json:"v"`
	T string `json:"t"`
}

// setInput is one raw named <input> inside a set-dlgform (the field() helper emits no name
// attribute, so these forms hand-roll their inputs). Type "" = no type attribute.
type setInput struct {
	Type string `json:"type,omitempty"`
	Name string `json:"name"`
	PH   string `json:"ph"`
}

// setKid is one child control of a composite block (fpair / btn-row / item-row trailing).
// K ∈ field|select|amenu|btn.
type setKid struct {
	K       string     `json:"k"`
	Fld     *uiField   `json:"fld,omitempty"`
	Tip     string     `json:"tip,omitempty"`      // legacy pre-rendered tooltip markup (bridge)
	TipS    *tipSt     `json:"tipSt,omitempty"`    // structured tooltip - wins over Tip
	Sel     *selState  `json:"sel,omitempty"`      // select/amenu kids
	SelLbl  string     `json:"selLbl,omitempty"`   // legacy pre-rendered ss-label (bridge)
	SelLblS *ssLabelSt `json:"selLblSt,omitempty"` // structured ss-label - wins over SelLbl
	Btn     *uiBtn     `json:"btn,omitempty"`
}

// setBlock is one card-body block. K selects the renderer; only that kind's fields are read.
type setBlock struct {
	K       string     `json:"k"`
	Text    string     `json:"text,omitempty"`
	HTML    string     `json:"html,omitempty"` // trusted raw markup (raw/noteRaw/region)
	Tone    string     `json:"tone,omitempty"` // hint tone
	ID      string     `json:"id,omitempty"`   // trusted literal id (install key, region id, form act)
	Title   string     `json:"title,omitempty"`
	Sub     string     `json:"sub,omitempty"`
	Fld     *uiField   `json:"fld,omitempty"`
	Tip     string     `json:"tip,omitempty"`   // legacy pre-rendered tooltip markup (bridge)
	TipS    *tipSt     `json:"tipSt,omitempty"` // structured tooltip (field/toggle) - wins over Tip
	Tgl     *uiToggle  `json:"tgl,omitempty"`
	Gate    string     `json:"gate,omitempty"` // toggle kind: non-empty = gated (disabled + hint)
	KV      *uiKV      `json:"kv,omitempty"`
	Sel     *selState  `json:"sel,omitempty"`
	SelLbl  string     `json:"selLbl,omitempty"`   // legacy pre-rendered ss-label (bridge)
	SelLblS *ssLabelSt `json:"selLblSt,omitempty"` // structured ss-label - wins over SelLbl
	Btn     *uiBtn     `json:"btn,omitempty"`      // path-row Browse button
	Kids    []setKid   `json:"kids,omitempty"`
	Inputs  []setInput `json:"inputs,omitempty"`
	Submit  string     `json:"submit,omitempty"` // form: literal type=submit button label
	SubVar  string     `json:"subVar,omitempty"` // form: that button's rp-btn variant
	// Sub-view bodies owned by other files, crossing as structured state (render_settings_sub_html.go):
	// K gridfix | gridfixmodel | bridge | updregion (the last wraps GF-style state in <div id=ID>).
	GF  *gfCardSt  `json:"gf,omitempty"`
	GFM *gfModelSt `json:"gfm,omitempty"`
	Brg *bridgeSt  `json:"brg,omitempty"`
	Upd *updFlowSt `json:"upd,omitempty"`
}

// setSwitchSt is a card header's feature switch. Gate non-empty = the dependency is missing and
// the feature is off: the switch renders disabled with a warn hint naming what to install.
type setSwitchSt struct {
	Label string `json:"label"`
	On    bool   `json:"on"`
	Gate  string `json:"gate,omitempty"`
}

// setCardSt is one settings card: header (title + tooltip + switch), live status region, body.
type setCardSt struct {
	ID     string       `json:"id"` // trusted literal (stset-<id>, toggle:<id>)
	Title  string       `json:"title"`
	Tip    string       `json:"tip,omitempty"`   // legacy pre-rendered tipTopic markup (bridge)
	TipS   *tipSt       `json:"tipSt,omitempty"` // structured tooltip - wins over Tip
	Desc   string       `json:"desc,omitempty"`
	St     setStatusSt  `json:"st"`
	Tgl    *setSwitchSt `json:"tgl,omitempty"` // nil = card has no feature toggle
	Blocks []setBlock   `json:"blocks,omitempty"`
}

// setNavSt is one sub-tab pill: section id (trusted literal), title, aggregate status variant.
type setNavSt struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Agg    string `json:"agg"`
	Active bool   `json:"active"`
}

// setSecSt is one rendered section: Title is used in search mode (section header), ID+Desc in
// sub-tab mode (the #set-<id> card grid under the section description).
type setSecSt struct {
	ID    string      `json:"id"`
	Title string      `json:"title"`
	Desc  string      `json:"desc"`
	Cards []setCardSt `json:"cards"`
}

// setContentSt is the #set-content pane: sub-tab pills + the active section, or (searching)
// the matching cards grouped by section.
type setContentSt struct {
	Searching bool       `json:"searching"`
	NoResults string     `json:"noResults"` // shown when searching with zero matches
	Nav       []setNavSt `json:"nav,omitempty"`
	Secs      []setSecSt `json:"secs,omitempty"`
}

// setState is the resolved render state for the whole Settings view.
type setState struct {
	Title       string       `json:"title"`
	Sub         string       `json:"sub"`
	Available   bool         `json:"available"` // Cfg resolved
	Unavailable string       `json:"unavailable"`
	Query       string       `json:"query"`
	Placeholder string       `json:"placeholder"`
	Content     setContentSt `json:"content"`
}

// ── pure renderers ──

// setStatusHTML renders the dot + line fragment patched by the settings tick (#stset-<id>).
func setStatusHTML(s setStatusSt) string {
	return `<span class="dot dot--` + s.V + `"></span><span data-value="` + html.EscapeString(s.T) + `">` + html.EscapeString(s.T) + `</span>`
}

// settingsHTML renders the full view: header + global search box + the patchable content pane.
func settingsHTML(st setState) string {
	if !st.Available {
		return panel(st.Title, "") + emptyState(st.Unavailable)
	}
	return `<div id=settings-body>` + panel(st.Title, st.Sub) +
		`<div class=set-search data-label="settings-search"><input id=set-q class=field-input type=search value=` + attrQ(st.Query) +
		` placeholder=` + attrQ(st.Placeholder) +
		` data-actinput=settings-search autocomplete=off spellcheck=false></div>` +
		`<div id=set-content>` + setContentHTML(st.Content) + `</div></div>`
}

// setContentHTML renders the pane below the search box (#set-content).
func setContentHTML(c setContentSt) string {
	var b strings.Builder
	if c.Searching {
		for _, s := range c.Secs {
			b.WriteString(section(s.Title, `<div class=set-sec>`+setCardsHTML(s.Cards)+`</div>`))
		}
		if len(c.Secs) == 0 {
			b.WriteString(emptyState(c.NoResults))
		}
		return b.String()
	}
	b.WriteString(`<nav class=set-nav>`)
	for _, n := range c.Nav {
		cls := "set-navpill"
		if n.Active {
			cls += " active"
		}
		b.WriteString(`<button class="` + cls + `" data-act=settings-sec data-val="` + n.ID + `">` +
			`<span id=stnav-` + n.ID + `><span class="dot dot--` + n.Agg + `"></span></span>` +
			html.EscapeString(n.Title) + `</button>`)
	}
	b.WriteString(`</nav>`)
	for _, s := range c.Secs {
		b.WriteString(`<p class=page-sub>` + html.EscapeString(s.Desc) + `</p>`)
		b.WriteString(`<div id=set-` + s.ID + ` class=set-sec>` + setCardsHTML(s.Cards) + `</div>`)
	}
	return b.String()
}

func setCardsHTML(cards []setCardSt) string {
	var b strings.Builder
	for _, c := range cards {
		b.WriteString(setCardHTML(c))
	}
	return b.String()
}

// setCardHTML renders one feature card: header (title + tooltip + switch) + status row + body.
func setCardHTML(c setCardSt) string {
	head := `<span class=set-title>` + html.EscapeString(c.Title) + `</span>` + tipOr(c.TipS, c.Tip)
	gateHTML := ""
	if t := c.Tgl; t != nil {
		if t.Gate != "" {
			// dependency missing: grey the enable switch + name what to install
			// (an already-on feature is never gated - it must stay turn-off-able)
			head += `<label class=switch title="` + html.EscapeString(t.Gate) + `"><input type=checkbox disabled><span class=switch-track></span></label>`
			gateHTML = `<div class=set-gate>` + hint("warn", t.Gate) + `</div>`
		} else {
			checked := ""
			if t.On {
				checked = " checked"
			}
			head += `<label class=switch title="` + html.EscapeString(t.Label) + `"><input type=checkbox` + checked +
				` data-act="toggle:` + c.ID + `" data-value="` + boolStr(t.On) + `"><span class=switch-track></span></label>`
		}
	}
	descHTML := ""
	if c.Desc != "" {
		descHTML = `<div class=set-note>` + html.EscapeString(c.Desc) + `</div>`
	}
	return `<div class="rp-card"><div class=set-cardhead>` + head + `</div>` +
		`<div class=set-st id=stset-` + c.ID + `>` + setStatusHTML(c.St) + `</div>` +
		gateHTML + descHTML + setBlocksHTML(c.Blocks) + `</div>`
}

// setBlocksHTML renders a card body's block list.
func setBlocksHTML(bs []setBlock) string {
	var b strings.Builder
	for _, blk := range bs {
		b.WriteString(setBlockHTML(blk))
	}
	return b.String()
}

// setBlockHTML renders one body block through the shared primitives.
func setBlockHTML(b setBlock) string {
	switch b.K {
	case "note":
		return setNote(b.Text)
	case "noteRaw":
		return `<div class=set-note>` + b.HTML + `</div>`
	case "hint":
		return hint(b.Tone, b.Text)
	case "empty":
		return emptyState(b.Text)
	case "field":
		return setFldHTML(b.Fld, tipOr(b.TipS, b.Tip))
	case "toggle":
		if b.Gate != "" {
			return toggleRowGatedDL(b.Tgl.Label, b.Tgl.DL, b.Tgl.On, b.Gate)
		}
		return toggleRowTipDL(b.Tgl.Label, b.Tgl.DL, b.Tgl.Act, b.Tgl.On, tipOr(b.TipS, b.Tip))
	case "select":
		return setSelHTML(b.Sel, b.SelLblS, b.SelLbl)
	case "amenu":
		return `<span class=amenu>` + setSelHTML(b.Sel, nil, "") + `</span>`
	case "kv":
		return b.KV.html()
	case "fpair":
		return `<div class=fpair>` + setKidsHTML(b.Kids) + `</div>`
	case "btnrow":
		return `<div class=btn-row>` + setKidsHTML(b.Kids) + `</div>`
	case "pathrow":
		return `<div class=set-pathrow>` + setFldHTML(b.Fld, "") + b.Btn.html() + `</div>`
	case "itemrow":
		return itemRow(b.Title, b.Sub, setKidsHTML(b.Kids))
	case "install":
		return `<div class=set-install>` + setNote(b.Text) +
			`<div class=btn-row>` + setKidsHTML(b.Kids) + `</div>` +
			`<div id=inst-` + b.ID + `></div></div>`
	case "installNote":
		return `<div class=set-install>` + setNote(b.Text) + `</div>`
	case "region":
		return `<div id=` + b.ID + `>` + b.HTML + `</div>`
	case "form":
		return setFormHTML(b)
	case "raw":
		return b.HTML
	case "gridfix":
		return gfCardHTML(*b.GF)
	case "gridfixmodel":
		return gfModelHTML(*b.GFM)
	case "bridge":
		return bridgeCardHTML(*b.Brg)
	case "updregion":
		return `<div id=` + b.ID + `>` + updFlowHTMLOf(*b.Upd) + `</div>`
	}
	return ""
}

func setKidsHTML(ks []setKid) string {
	var b strings.Builder
	for _, k := range ks {
		switch k.K {
		case "field":
			b.WriteString(setFldHTML(k.Fld, tipOr(k.TipS, k.Tip)))
		case "select":
			b.WriteString(setSelHTML(k.Sel, k.SelLblS, k.SelLbl))
		case "amenu":
			b.WriteString(`<span class=amenu>` + setSelHTML(k.Sel, nil, "") + `</span>`)
		case "btn":
			b.WriteString(k.Btn.html())
		}
	}
	return b.String()
}

// setNote is the muted help/notes line every settings card uses.
func setNote(text string) string { return `<div class=set-note>` + html.EscapeString(text) + `</div>` }

// setFldHTML renders a uiField with an optional pre-rendered tooltip beside the label.
func setFldHTML(f *uiField, tip string) string {
	return fieldExDL(f.Label, f.DL, f.Act, f.Value, f.Type, f.PH, tip)
}

// setSelHTML renders a resolved smart select through the shared ss-label bridge (components.go).
func setSelHTML(s *selState, lbl *ssLabelSt, labelHTML string) string {
	return ssSelHTML(*s, lbl, labelHTML)
}

// setFormHTML renders a set-dlgform: raw named inputs, then button kids, then the optional
// literal type=submit button (parseForm reads the named inputs; see bridgeGateBody).
func setFormHTML(b setBlock) string {
	var s strings.Builder
	s.WriteString(`<form class=set-dlgform data-act=` + b.ID + `>`)
	for _, in := range b.Inputs {
		s.WriteString(`<input class=field-input`)
		if in.Type != "" {
			s.WriteString(` type=` + in.Type)
		}
		s.WriteString(` name=` + in.Name + ` placeholder=` + attrQ(in.PH) + `autocomplete=off>`)
	}
	s.WriteString(setKidsHTML(b.Kids))
	if b.Submit != "" {
		s.WriteString(`<button class="rp-btn rp-btn--` + b.SubVar + `" type=submit>` + html.EscapeString(b.Submit) + `</button>`)
	}
	s.WriteString(`</form>`)
	return s.String()
}
