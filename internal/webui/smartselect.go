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
	ssMu.Lock()
	if ssSts[id] == nil {
		ssSts[id] = &ssSt{}
	}
	ssOpts[id], ssActs[id], ssCurs[id] = opts, act, cur
	ssMu.Unlock()
	lbl := ""
	if label != "" {
		lbl = `<span class=ss-label>` + html.EscapeString(label) + `</span>`
	}
	return `<div class=ss-field>` + lbl + `<div class=ss id="ss-` + html.EscapeString(id) + `">` + ssInner(id) + `</div></div>`
}

func ssInner(id string) string {
	ssMu.Lock()
	st := ssSts[id]
	opts := ssOpts[id]
	cur := ssCurs[id]
	open, filter := st.open, st.filter
	ssMu.Unlock()

	curLabel, list := cur, []ssOpt(nil)
	if opts != nil {
		list = opts()
	}
	for _, o := range list {
		if o.Val == cur {
			curLabel = o.Label
			break
		}
	}
	if curLabel == "" {
		curLabel = "(select…)"
	}
	openCls := ""
	if open {
		openCls = " open"
	}
	var b strings.Builder
	b.WriteString(`<button type=button class="ss-btn` + openCls + `" data-act="ss-tgl:` + html.EscapeString(id) + `" data-label=` + attrQ(id) + `>` +
		`<span class=ss-cur>` + html.EscapeString(curLabel) + `</span>` +
		`<svg class=ss-chev viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg></button>`)
	if open {
		b.WriteString(`<div class=ss-bd data-act="ss-tgl:` + html.EscapeString(id) + `"></div>`)
		b.WriteString(`<div class=ss-panel>`)
		b.WriteString(`<form class=ss-fw data-act="ss-first:` + html.EscapeString(id) + `">` +
			`<input class=ss-filter id="ss-f-` + html.EscapeString(id) + `" data-actinput="ss-flt:` + html.EscapeString(id) + `" data-label=` + attrQ(id+"-flt") +
			` placeholder="Type to filter…" value=` + attrQ(filter) + ` autocomplete=off></form>`)
		b.WriteString(`<div class=ss-list id="ss-l-` + html.EscapeString(id) + `">` + ssListHTML(id) + `</div>`)
		b.WriteString(`</div>`)
	}
	return b.String()
}

// ssListHTML renders the filtered option rows.
func ssListHTML(id string) string {
	ssMu.Lock()
	st := ssSts[id]
	opts := ssOpts[id]
	cur := ssCurs[id]
	filter := ""
	if st != nil {
		filter = strings.ToLower(strings.TrimSpace(st.filter))
	}
	ssMu.Unlock()
	if opts == nil {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, o := range opts() {
		if filter != "" && !strings.Contains(strings.ToLower(o.Label+" "+o.Sub+" "+o.Val), filter) {
			continue
		}
		n++
		cls := "ss-opt"
		if o.Val == cur {
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
	if n == 0 {
		return `<div class=ss-none>No matches</div>`
	}
	return b.String()
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
