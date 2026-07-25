package webui

// SmartSelect - filterable dropdown replacing chip walls + long native selects.
// Go renders everything (open/filter state per id); picking forwards the consumer's
// act through dispatch, so any selectBox/fchip act adopts unchanged. Rich rows carry
// an optional sub-line + badge (preset pickers, device lists).
//
// ctl: button carries data-label=<id> (read shows current), filter input
// data-label=<id>-flt (ctl set filters), option rows are data-act (ctl click by text).
// id must be a single colon-free token.

import (
	"html"
	"strings"
	"sync"
)

type ssOpt struct {
	Val, Label string
	Sub        string // muted second line (rich rows)
	Badge      string // small trailing pill
}

type ssSt struct {
	open   bool
	filter string
}

var (
	ssMu   sync.Mutex
	ssSts  = map[string]*ssSt{}
	ssOpts = map[string]func() []ssOpt{}
	ssActs = map[string]string{}
	ssCurs = map[string]string{}
)

func init() {
	onPrefix("ss-tgl:", func(u *UI, m actMsg) { u.ssToggle(strings.TrimPrefix(m.Act, "ss-tgl:")) })
	onPrefix("ss-flt:", func(u *UI, m actMsg) { u.ssFilter(strings.TrimPrefix(m.Act, "ss-flt:"), m.Val) })
	onPrefix("ss-pick:", func(u *UI, m actMsg) { u.ssPick(strings.TrimPrefix(m.Act, "ss-pick:"), m.Val) })
	onPrefix("ss-first:", func(u *UI, m actMsg) { u.ssFirst(strings.TrimPrefix(m.Act, "ss-first:")) })
}

// smartSelect renders the control. label "" = bare (caller renders its own).
// act receives the picked Val exactly like a selectBox change would.
func smartSelect(id, label, act, cur string, opts func() []ssOpt) string {
	lbl := ""
	if label != "" {
		lbl = `<span class=ss-label>` + html.EscapeString(label) + `</span>`
	}
	return smartSelectRaw(id, lbl, act, cur, opts)
}

// smartSelectRaw is smartSelect with a pre-rendered label (caller escapes) - lets a
// tooltip/badge sit beside the label text.
func smartSelectRaw(id, labelHTML, act, cur string, opts func() []ssOpt) string {
	ssRegister(id, act, cur, opts)
	return `<div class=ss-field>` + labelHTML + `<div class=ss id="ss-` + html.EscapeString(id) + `">` + ssInner(id) + `</div></div>`
}

// ssRegister records id's opts/act/cur (ensuring state exists) - the render-time side
// effect shared by smartSelectRaw and the Zig-state resolvers.
func ssRegister(id, act, cur string, opts func() []ssOpt) {
	ssMu.Lock()
	if ssSts[id] == nil {
		ssSts[id] = &ssSt{}
	}
	ssOpts[id], ssActs[id], ssCurs[id] = opts, act, cur
	ssMu.Unlock()
}

// ── resolved render state (JSON → Zig; selHTML/selInnerHTML stay the golden reference) ──

// selRow is one filter-passing option row.
type selRow struct {
	Val   string `json:"val"`
	Label string `json:"label"`
	Sub   string `json:"sub"`
	Badge string `json:"badge"`
	Cur   bool   `json:"cur"`
}

// selState is a smart select resolved to pure render state: id, plain label
// (smartSelectRaw keeps rich labels on the Go path), current label, open/filter,
// filter-passing rows. Unicode filtering/lowercasing stays here in Go.
type selState struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	CurLabel string   `json:"curLabel"`
	Open     bool     `json:"open"`
	Filter   string   `json:"filter"`
	Rows     []selRow `json:"rows"`
}

// ssResolve snapshots id's live smart-select state into pure render state.
func ssResolve(id string) selState {
	ssMu.Lock()
	st := ssSts[id]
	opts := ssOpts[id]
	cur := ssCurs[id]
	ssMu.Unlock()
	s := selState{ID: id, CurLabel: cur, Rows: []selRow{}}
	if st != nil {
		s.Open, s.Filter = st.open, st.filter
	}
	var list []ssOpt
	if opts != nil {
		list = opts()
	}
	for _, o := range list {
		if o.Val == cur {
			s.CurLabel = o.Label
			break
		}
	}
	if s.CurLabel == "" {
		s.CurLabel = "(select…)"
	}
	f := strings.ToLower(strings.TrimSpace(s.Filter))
	for _, o := range list {
		if f != "" && !strings.Contains(strings.ToLower(o.Label+" "+o.Sub+" "+o.Val), f) {
			continue
		}
		s.Rows = append(s.Rows, selRow{Val: o.Val, Label: o.Label, Sub: o.Sub, Badge: o.Badge, Cur: o.Val == cur})
	}
	return s
}

// resolveSelectBox registers + resolves a selectBox (plain label + [][val,label]
// options) into pure render state for a Zig-migrated tab.
func resolveSelectBox(label, act string, options [][2]string, current string) selState {
	id := strings.NewReplacer(":", "-", "/", "-", " ", "-").Replace(act)
	ssRegister(id, act, current, func() []ssOpt {
		out := make([]ssOpt, 0, len(options))
		for _, op := range options {
			out = append(out, ssOpt{Val: op[0], Label: op[1]})
		}
		return out
	})
	s := ssResolve(id)
	s.Label = label
	return s
}

// selHTML renders the full ss-field control from resolved state (plain label).
func selHTML(s selState) string {
	lbl := ""
	if s.Label != "" {
		lbl = `<span class=ss-label>` + html.EscapeString(s.Label) + `</span>`
	}
	return `<div class=ss-field>` + lbl + `<div class=ss id="ss-` + html.EscapeString(s.ID) + `">` + selInnerHTML(s) + `</div></div>`
}

// selInnerHTML renders the <div class=ss> inner markup from resolved state.
func selInnerHTML(s selState) string {
	openCls := ""
	if s.Open {
		openCls = " open"
	}
	var b strings.Builder
	b.WriteString(`<button type=button class="ss-btn` + openCls + `" data-act="ss-tgl:` + html.EscapeString(s.ID) + `" data-label=` + attrQ(s.ID) + `>` +
		`<span class=ss-cur>` + html.EscapeString(s.CurLabel) + `</span>` +
		`<svg class=ss-chev viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg></button>`)
	if s.Open {
		b.WriteString(`<div class=ss-bd data-act="ss-tgl:` + html.EscapeString(s.ID) + `"></div>`)
		b.WriteString(`<div class=ss-panel>`)
		b.WriteString(`<form class=ss-fw data-act="ss-first:` + html.EscapeString(s.ID) + `">` +
			`<input class=ss-filter id="ss-f-` + html.EscapeString(s.ID) + `" data-actinput="ss-flt:` + html.EscapeString(s.ID) + `" data-label=` + attrQ(s.ID+"-flt") +
			` placeholder="Type to filter…" value=` + attrQ(s.Filter) + ` autocomplete=off></form>`)
		b.WriteString(`<div class=ss-list id="ss-l-` + html.EscapeString(s.ID) + `">` + selListHTML(s.ID, s.Rows) + `</div>`)
		b.WriteString(`</div>`)
	}
	return b.String()
}

// selListHTML renders resolved option rows (ss-none when nothing passed the filter).
func selListHTML(id string, rows []selRow) string {
	if len(rows) == 0 {
		return `<div class=ss-none>No matches</div>`
	}
	var b strings.Builder
	for _, o := range rows {
		cls := "ss-opt"
		if o.Cur {
			cls += " cur"
		}
		b.WriteString(`<div class="` + cls + `" data-act="ss-pick:` + html.EscapeString(id) + `" data-val=` + attrQ(o.Val) + `>`)
		b.WriteString(`<span class=ss-main><span class=ss-ol>` + html.EscapeString(o.Label) + `</span>`)
		if o.Sub != "" {
			b.WriteString(`<span class=ss-sub>` + html.EscapeString(o.Sub) + `</span>`)
		}
		b.WriteString(`</span>`)
		if o.Badge != "" {
			b.WriteString(`<span class=ss-badge>` + html.EscapeString(o.Badge) + `</span>`)
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

// ssFilter returns id's live filter text - opts fns over huge lists (track pickers)
// pre-filter + cap server-side so an unfiltered open never renders thousands of rows.
func ssFilter(id string) string {
	ssMu.Lock()
	defer ssMu.Unlock()
	if st := ssSts[id]; st != nil {
		return st.filter
	}
	return ""
}

// ssInner renders the live control's inner markup via the resolved-state renderer.
func ssInner(id string) string { return selInnerHTML(ssResolve(id)) }

// ssListHTML renders the filtered option rows ("" when id was never registered).
func ssListHTML(id string) string {
	ssMu.Lock()
	opts := ssOpts[id]
	ssMu.Unlock()
	if opts == nil {
		return ""
	}
	return selListHTML(id, ssResolve(id).Rows)
}

func (u *UI) ssToggle(id string) {
	ssMu.Lock()
	st := ssSts[id]
	if st == nil {
		ssMu.Unlock()
		return
	}
	st.open = !st.open
	if st.open {
		st.filter = ""
	}
	open := st.open
	ssMu.Unlock()
	u.ssPatch(id)
	if open {
		u.eval("var f=document.getElementById(" + jsQuote("ss-f-"+id) + ");if(f)f.focus();")
	}
}

func (u *UI) ssFilter(id, v string) {
	ssMu.Lock()
	st := ssSts[id]
	if st != nil {
		st.filter = v
	}
	ssMu.Unlock()
	if st == nil { // unknown id (never rendered) - don't touch the DOM
		return
	}
	u.eval("window.__patch(" + jsQuote("ss-l-"+id) + "," + jsQuote(ssListHTML(id)) + ")")
}

// ssPick closes + forwards the consumer act (the consumer's own re-render usually
// replaces the control; the extra ssPatch is a no-op guard when it doesn't).
// act "foo:" (trailing colon) = suffix convention: dispatches "foo:<val>" like an
// fchip would; otherwise val rides actMsg.Val like a selectBox change.
func (u *UI) ssPick(id, val string) {
	ssMu.Lock()
	st, act := ssSts[id], ssActs[id]
	if st != nil {
		st.open = false
	}
	ssMu.Unlock()
	if st == nil { // unknown id - no state, no act, no DOM
		return
	}
	switch {
	case act == "":
	case strings.HasSuffix(act, ":"):
		u.dispatch(actMsg{Act: act + val})
	default:
		u.dispatch(actMsg{Act: act, Val: val})
	}
	u.ssPatch(id)
}

// ssFirst picks the first filtered option (Enter in the filter box).
func (u *UI) ssFirst(id string) {
	ssMu.Lock()
	st := ssSts[id]
	opts := ssOpts[id]
	filter := ""
	if st != nil {
		filter = strings.ToLower(strings.TrimSpace(st.filter))
	}
	ssMu.Unlock()
	if opts == nil {
		return
	}
	for _, o := range opts() {
		if filter != "" && !strings.Contains(strings.ToLower(o.Label+" "+o.Sub+" "+o.Val), filter) {
			continue
		}
		u.ssPick(id, o.Val)
		return
	}
}

func (u *UI) ssPatch(id string) {
	ssMu.Lock()
	known := ssSts[id] != nil
	ssMu.Unlock()
	if !known {
		return
	}
	u.eval("window.__patch(" + jsQuote("ss-"+id) + "," + jsQuote(ssInner(id)) + ")")
}
