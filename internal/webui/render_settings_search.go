package webui

import "strings"

// Settings search over the STRUCTURED block state (phase B4d).
//
// The old matcher rendered every card to HTML for every query and matched against
// foldSearch(stripTags(setCardHTML(card))) - ~40 full card renders per keystroke on the handler
// lane, and a derived text blob kept Go-side only because building it was cheaper than walking
// state the Go renderer had to re-marshal anyway. Since wave B-2 setBlock/setKid ARE full
// structured state (tooltips included), so the walk below reads the state directly.
//
// SEARCHABLE = exactly the document's TEXT NODES, which is what stripTags left behind. Attribute
// values are NOT searchable and must not start being: a field's value/placeholder, a switch's
// title, a select's filter and every data-* live in attributes, so they never matched.
//
// Why per-piece collection is equivalent to the old one-big-string haystack: query terms come from
// strings.Fields, so no term contains whitespace, and setCardHTML emits at least one tag - hence at
// least one space after stripTags - between any two text nodes. A whitespace-free term therefore
// matches the joined haystack iff it matches inside ONE text run. Pieces are joined by '\n' here
// for the same reason: a term can never span the separator. (Differential proof over the whole
// fixture corpus: render_settings_search_test.go.)

// setSearchSep separates collected text runs. Any whitespace byte works (terms are whitespace-free);
// '\n' cannot occur inside a label often enough to matter and keeps dumps readable when debugging.
const setSearchSep = '\n'

// setCardMatches reports whether the card's visible text contains every term. Terms must already be
// folded (foldSearch) + whitespace-split, exactly as the caller does for the query.
func setCardMatches(c setCardSt, terms []string) bool {
	var b strings.Builder
	b.Grow(1024)
	appendSetCardText(&b, c)
	return matchAllTerms(foldSearch(b.String()), terms)
}

// piece appends one searchable text run.
func piece(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	b.WriteByte(setSearchSep)
	b.WriteString(s)
}

// appendSetCardText collects the text runs setCardHTML emits, in document order.
func appendSetCardText(b *strings.Builder, c setCardSt) {
	piece(b, c.Title)
	appendTipText(b, c.TipS, c.Tip)
	if t := c.Tgl; t != nil && t.Gate != "" {
		piece(b, t.Gate) // the set-gate warn hint; the switch's title attribute is not text
	}
	piece(b, c.St.T) // status line (setStatusHTML: data-value attr + the same text node)
	piece(b, c.Desc)
	for _, blk := range c.Blocks {
		appendSetBlockText(b, blk)
	}
}

// appendSetBlockText mirrors setBlockHTML's per-kind text emission.
func appendSetBlockText(b *strings.Builder, blk setBlock) {
	switch blk.K {
	case "note", "hint", "empty", "installNote":
		piece(b, blk.Text)
	case "noteRaw", "region", "raw":
		piece(b, stripTags(blk.HTML)) // trusted raw markup: no structured text to walk
	case "field":
		appendFieldText(b, blk.Fld)
		appendTipText(b, blk.TipS, blk.Tip)
	case "toggle":
		if blk.Tgl != nil {
			piece(b, blk.Tgl.Label)
		}
		if blk.Gate != "" {
			piece(b, blk.Gate) // gated: the warn hint replaces the tooltip
			return
		}
		appendTipText(b, blk.TipS, blk.Tip)
	case "select", "amenu":
		appendSelText(b, blk.Sel, blk.SelLblS, blk.SelLbl, blk.K == "amenu")
	case "kv":
		if blk.KV != nil {
			piece(b, blk.KV.Label)
			piece(b, blk.KV.Value)
		}
	case "fpair", "btnrow":
		appendSetKidsText(b, blk.Kids)
	case "pathrow":
		appendFieldText(b, blk.Fld)
		if blk.Btn != nil {
			piece(b, blk.Btn.Label)
		}
	case "itemrow":
		piece(b, blk.Title)
		piece(b, blk.Sub)
		appendSetKidsText(b, blk.Kids)
	case "install":
		piece(b, blk.Text)
		appendSetKidsText(b, blk.Kids)
	case "form":
		appendSetKidsText(b, blk.Kids) // named inputs carry text only in attributes
		piece(b, blk.Submit)
	case "gridfix":
		if blk.GF != nil {
			piece(b, stripTags(gfCardHTML(*blk.GF)))
		}
	case "gridfixmodel":
		if blk.GFM != nil {
			piece(b, stripTags(gfModelHTML(*blk.GFM)))
		}
	case "bridge":
		if blk.Brg != nil {
			piece(b, stripTags(bridgeCardHTML(*blk.Brg)))
		}
	case "updregion":
		if blk.Upd != nil {
			piece(b, stripTags(updFlowHTMLOf(*blk.Upd)))
		}
	}
}

// appendSetKidsText mirrors setKidsHTML.
func appendSetKidsText(b *strings.Builder, ks []setKid) {
	for _, k := range ks {
		switch k.K {
		case "field":
			appendFieldText(b, k.Fld)
			appendTipText(b, k.TipS, k.Tip)
		case "select", "amenu":
			appendSelText(b, k.Sel, k.SelLblS, k.SelLbl, k.K == "amenu")
		case "btn":
			if k.Btn != nil {
				piece(b, k.Btn.Label)
			}
		}
	}
}

// appendFieldText: fieldExDL renders the label as text; value + placeholder are attributes only.
func appendFieldText(b *strings.Builder, f *uiField) {
	if f != nil {
		piece(b, f.Label)
	}
}

// appendSelText mirrors ssSelHTML + selInnerHTML: the ss-label (structured, legacy raw, or the
// select's own Label) plus the current label; the option rows are in the DOM only while OPEN.
// amenu passes no label (setBlockHTML calls setSelHTML(b.Sel, nil, "")).
func appendSelText(b *strings.Builder, s *selState, lbl *ssLabelSt, labelHTML string, amenu bool) {
	if s == nil {
		return
	}
	switch {
	case amenu:
		piece(b, s.Label)
	case lbl != nil:
		piece(b, lbl.Text)
		appendTipText(b, lbl.Tip, "")
	case labelHTML != "":
		piece(b, stripTags(labelHTML))
	default:
		piece(b, s.Label)
	}
	piece(b, s.CurLabel)
	if !s.Open {
		return
	}
	if len(s.Rows) == 0 {
		piece(b, "No matches") // selListHTML's literal empty state
		return
	}
	for _, r := range s.Rows {
		piece(b, r.Label)
		piece(b, r.Sub)
		piece(b, r.Badge)
	}
}

// appendTipText mirrors tipOr + renderTipSt: structured wins, else the legacy pre-rendered string
// (stripped like any raw markup). aria-label is an attribute, so the title is one text run.
func appendTipText(b *strings.Builder, s *tipSt, raw string) {
	if s == nil {
		piece(b, stripTags(raw))
		return
	}
	piece(b, s.Title)
	for _, r := range s.Keys {
		if r.HasGroup {
			piece(b, r.Group)
		}
		for _, ch := range r.Chips {
			piece(b, ch.Text)
		}
		piece(b, r.Verb)
		piece(b, r.Rest)
	}
	for _, p := range s.Paras {
		piece(b, p)
	}
	for _, l := range s.Links {
		piece(b, l.Label+" ↗") // the link's text node carries the trailing glyph
	}
}
