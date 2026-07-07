package webui

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
)

const logTailN = 400 // max lines rendered into #log-view

// renderLogs is the live daemon log viewer (mirrors the Fyne Logs tab). Multi-bus subTabs
// (App + MIDI/Traktor/Session when wired) + level/source/search filters + autoscroll + copy.
// livePush patches #log-view ~1 Hz, preserving scroll unless the user is already at the bottom.
func (u *UI) renderLogs() string {
	var b strings.Builder
	b.WriteString(panel(i18n.T("tab.logs"), i18n.T("navtitle.logs")))
	b.WriteString(u.logBusTabs())
	b.WriteString(u.logFiltersHTML())
	b.WriteString(`<div id=log-view class=log-view>` + u.logLinesHTML(logTailN) + `</div>`)
	return b.String()
}

// logBusTabs renders the bus selector (only buses that are wired). Hidden when App is the only bus.
func (u *UI) logBusTabs() string {
	items := [][2]string{{"app", i18n.T("logs.busApp")}}
	if u.svc.MIDIMon != nil {
		items = append(items, [2]string{"midi", i18n.T("logs.busMidi")})
	}
	if u.svc.TraktorMon != nil {
		items = append(items, [2]string{"traktor", i18n.T("logs.busTraktor")})
	}
	if u.svc.SessionMon != nil {
		items = append(items, [2]string{"session", i18n.T("logs.busSession")})
	}
	if len(items) == 1 {
		return ""
	}
	u.logMu.Lock()
	active := u.logBus
	u.logMu.Unlock()
	if active == "" {
		active = "app"
	}
	return subTabs("logs-bus:", active, items...)
}

// logFiltersHTML renders the level + source selects, the search box, autoscroll toggle, and actions.
func (u *UI) logFiltersHTML() string {
	u.logMu.Lock()
	lvl, src, q, as := u.logLevel, u.logSource, u.logSearch, u.logAutoscroll
	u.logMu.Unlock()
	if lvl == "" {
		lvl = "all"
	}
	levelOpts := [][2]string{
		{"all", i18n.T("logs.levelAll")},
		{"info", i18n.T("logs.levelInfo")},
		{"warn", i18n.T("logs.levelWarn")},
		{"error", i18n.T("logs.levelError")},
	}
	search := `<label class="field log-search" data-label="logs-search"><span class=field-label>` +
		html.EscapeString(i18n.T("logs.searchLabel")) + `</span>` +
		`<input class=field-input type=text placeholder=` + attrQ(i18n.T("logs.searchPlaceholder")) +
		` value=` + attrQ(q) + ` data-actinput="logs-search"></label>`

	var b strings.Builder
	b.WriteString(`<div class=log-filters>`)
	b.WriteString(selectBox(i18n.T("logs.levelLabel"), "logs-level", levelOpts, lvl))
	b.WriteString(selectBox(i18n.T("logs.sourceLabel"), "logs-source", u.logSources(), src))
	b.WriteString(search)
	b.WriteString(toggleRow(i18n.T("logs.autoscroll"), "logs-autoscroll", as))
	b.WriteString(`</div>`)
	b.WriteString(`<div class=log-toolbar>` +
		btn(i18n.T("logs.copyAll"), "outline", "logs-copy", "") +
		btn(i18n.T("logs.clearView"), "outline", "logs-clear", "") +
		`<span class=page-sub style="margin:0">` + html.EscapeString(i18n.T("logs.tailing")) + `</span></div>`)
	return b.String()
}

// logSources lists the distinct sources in the active bus (plus an "all" default), for the filter.
func (u *UI) logSources() [][2]string {
	opts := [][2]string{{"", i18n.T("logs.allSources")}}
	u.logMu.Lock()
	sel := u.logBus
	u.logMu.Unlock()
	bus := u.busFor(sel)
	if bus == nil {
		return opts
	}
	seen := map[string]bool{}
	var srcs []string
	for _, e := range bus.Snapshot() {
		if e.Source != "" && !seen[e.Source] {
			seen[e.Source] = true
			srcs = append(srcs, e.Source)
		}
	}
	sort.Strings(srcs)
	for _, s := range srcs {
		opts = append(opts, [2]string{s, s})
	}
	return opts
}

// busFor maps a bus selector to its logbus.Bus (nil if that monitor isn't wired). No lock.
func (u *UI) busFor(sel string) *logbus.Bus {
	switch sel {
	case "midi":
		return u.svc.MIDIMon
	case "traktor":
		return u.svc.TraktorMon
	case "session":
		return u.svc.SessionMon
	default:
		return u.svc.Log
	}
}

// logFilteredEntries returns the active bus + its entries after level/source/search filtering,
// capped to the newest n. bus==nil when the selected monitor isn't wired.
func (u *UI) logFilteredEntries(n int) (*logbus.Bus, []logbus.Entry) {
	u.logMu.Lock()
	sel, lvl, src := u.logBus, u.logLevel, u.logSource
	q := strings.ToLower(strings.TrimSpace(u.logSearch))
	u.logMu.Unlock()
	bus := u.busFor(sel)
	if bus == nil {
		return nil, nil
	}
	minLvl := logLevelMin(lvl)
	es := bus.Snapshot()
	out := make([]logbus.Entry, 0, len(es))
	for _, e := range es {
		if e.Level < minLvl {
			continue
		}
		if src != "" && e.Source != src {
			continue
		}
		if q != "" && !logMatches(e, q) {
			continue
		}
		out = append(out, e)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return bus, out
}

func (u *UI) logLinesHTML(n int) string {
	bus, es := u.logFilteredEntries(n)
	if bus == nil {
		return `<div class=log-line>` + html.EscapeString(i18n.T("logs.noLogBus")) + `</div>`
	}
	if len(es) == 0 {
		return `<div class=log-line>` + html.EscapeString(i18n.T("logs.noEntries")) + `</div>`
	}
	var b strings.Builder
	for _, e := range es {
		b.WriteString(logLineHTML(e))
	}
	return b.String()
}

// logPlainText renders the shown (filtered) lines as plain text for clipboard copy.
func (u *UI) logPlainText() string {
	_, es := u.logFilteredEntries(logTailN)
	var b strings.Builder
	for _, e := range es {
		b.WriteString(logPlainLine(e))
		b.WriteByte('\n')
	}
	return b.String()
}

func logPlainLine(e logbus.Entry) string {
	lvl := strings.ToUpper(strings.TrimSpace(fmt.Sprint(e.Level)))
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-5s  [%s]  %s", e.Time.Format("15:04:05.000"), lvl, e.Source, e.Msg)
	if len(e.Fields) > 0 {
		b.WriteString("  " + fmt.Sprint(e.Fields))
	}
	return b.String()
}

// logMatches reports whether entry e contains q (lower-cased) in msg/source/level/fields.
func logMatches(e logbus.Entry, q string) bool {
	if strings.Contains(strings.ToLower(e.Msg), q) || strings.Contains(strings.ToLower(e.Source), q) {
		return true
	}
	if strings.Contains(strings.ToLower(fmt.Sprint(e.Level)), q) {
		return true
	}
	if len(e.Fields) > 0 && strings.Contains(strings.ToLower(fmt.Sprint(e.Fields)), q) {
		return true
	}
	return false
}

// logLevelMin maps a filter selector to the minimum logbus.Level to show.
func logLevelMin(sel string) logbus.Level {
	switch sel {
	case "info":
		return logbus.Info
	case "warn":
		return logbus.Warn
	case "error":
		return logbus.Error
	default:
		return logbus.Debug
	}
}

func logLineHTML(e logbus.Entry) string {
	lvl := strings.ToUpper(strings.TrimSpace(fmt.Sprint(e.Level)))
	cls := classLetters(lvl)
	var b strings.Builder
	b.WriteString(`<div class=log-line>`)
	b.WriteString(html.EscapeString(e.Time.Format("15:04:05.000")))
	b.WriteString(` <span class="lv-` + cls + `">` + html.EscapeString(padRight(lvl, 5)) + `</span>`)
	b.WriteString(` <span class=lv-src>[` + html.EscapeString(e.Source) + `]</span> `)
	b.WriteString(html.EscapeString(e.Msg))
	if len(e.Fields) > 0 {
		b.WriteString(` ` + html.EscapeString(fmt.Sprint(e.Fields)))
	}
	b.WriteString(`</div>`)
	return b.String()
}

// classLetters keeps only A–Z (a safe CSS class suffix; empty → INFO).
func classLetters(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "INFO"
	}
	return b.String()
}

func padRight(s string, n int) string {
	if len(s) < n {
		return s + strings.Repeat(" ", n-len(s))
	}
	return s
}
