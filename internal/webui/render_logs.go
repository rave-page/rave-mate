package webui

import (
	"fmt"
	"html"
	"sort"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/zigui"
)

const logTailN = 400 // max lines rendered into #log-view

// Logs is a Zig-rendered tab (native/zigui/src/logs.zig): Go resolves all state
// (filters + i18n + the filtered tail) into logsState, the Zig lib renders HTML
// byte-identical to the Go renderers below (fallback + golden reference,
// zigui_golden_logs_test.go).

// logsTab is one bus-selector item.
type logsTab struct {
	Val   string `json:"val"`
	Label string `json:"label"`
}

// logsEntry is one resolved log line.
type logsEntry struct {
	Time   string `json:"time"`
	Lvl    string `json:"lvl"` // upper-cased level text (unpadded; renderers pad to 5)
	Cls    string `json:"cls"` // classLetters(Lvl) CSS suffix
	Src    string `json:"src"`
	Msg    string `json:"msg"`
	Fields string `json:"fields"` // fmt.Sprint(e.Fields); "" = none
}

// logsLines is the #log-view inner state (filter/tick patch target).
type logsLines struct {
	Wired     bool        `json:"wired"` // selected bus monitor is wired
	NoBus     string      `json:"noBus"`
	NoEntries string      `json:"noEntries"`
	Entries   []logsEntry `json:"entries"`
}

// logsState is the resolved render state for the Logs view (JSON → Zig).
type logsState struct {
	Title       string    `json:"title"`
	Sub         string    `json:"sub"`
	ShowBus     bool      `json:"showBus"` // >1 wired bus
	BusActive   string    `json:"busActive"`
	BusItems    []logsTab `json:"busItems"`
	Level       selState  `json:"level"`
	Source      selState  `json:"source"`
	SearchLabel string    `json:"searchLabel"`
	SearchPH    string    `json:"searchPh"`
	SearchVal   string    `json:"searchVal"`
	AutoLabel   string    `json:"autoLabel"`
	AutoDL      string    `json:"autoDl"` // strings.ToLower(AutoLabel)
	AutoOn      bool      `json:"autoOn"`
	Copy        string    `json:"copy"`
	Clear       string    `json:"clear"`
	Tailing     string    `json:"tailing"`
	Lines       logsLines `json:"lines"`
}

// logsState resolves filter state + wired buses + i18n into render state.
func (u *UI) logsState() logsState {
	u.logMu.Lock()
	bus, lvl, src, q, as := u.logBus, u.logLevel, u.logSource, u.logSearch, u.logAutoscroll
	u.logMu.Unlock()
	if bus == "" {
		bus = "app"
	}
	if lvl == "" {
		lvl = "all"
	}
	items := []logsTab{{Val: "app", Label: i18n.T("logs.busApp")}}
	if u.svc.MIDIMon != nil {
		items = append(items, logsTab{Val: "midi", Label: i18n.T("logs.busMidi")})
	}
	if u.svc.TraktorMon != nil {
		items = append(items, logsTab{Val: "traktor", Label: i18n.T("logs.busTraktor")})
	}
	if u.svc.SessionMon != nil {
		items = append(items, logsTab{Val: "session", Label: i18n.T("logs.busSession")})
	}
	levelOpts := [][2]string{
		{"all", i18n.T("logs.levelAll")},
		{"info", i18n.T("logs.levelInfo")},
		{"warn", i18n.T("logs.levelWarn")},
		{"error", i18n.T("logs.levelError")},
	}
	autoLbl := i18n.T("logs.autoscroll")
	return logsState{
		Title: i18n.T("tab.logs"), Sub: i18n.T("navtitle.logs"),
		ShowBus: len(items) > 1, BusActive: bus, BusItems: items,
		Level:       resolveSelectBox(i18n.T("logs.levelLabel"), "logs-level", levelOpts, lvl),
		Source:      resolveSelectBox(i18n.T("logs.sourceLabel"), "logs-source", u.logSources(), src),
		SearchLabel: i18n.T("logs.searchLabel"), SearchPH: i18n.T("logs.searchPlaceholder"), SearchVal: q,
		AutoLabel: autoLbl, AutoDL: strings.ToLower(autoLbl), AutoOn: as,
		Copy: i18n.T("logs.copyAll"), Clear: i18n.T("logs.clearView"), Tailing: i18n.T("logs.tailing"),
		Lines: u.logsLinesState(logTailN),
	}
}

// logsLinesState resolves the filtered tail into render state.
func (u *UI) logsLinesState(n int) logsLines {
	st := logsLines{NoBus: i18n.T("logs.noLogBus"), NoEntries: i18n.T("logs.noEntries"), Entries: []logsEntry{}}
	bus, es := u.logFilteredEntries(n)
	if bus == nil {
		return st
	}
	st.Wired = true
	for _, e := range es {
		lvl := strings.ToUpper(strings.TrimSpace(fmt.Sprint(e.Level)))
		fields := ""
		if len(e.Fields) > 0 {
			fields = fmt.Sprint(e.Fields)
		}
		st.Entries = append(st.Entries, logsEntry{
			Time: e.Time.Format("15:04:05.000"), Lvl: lvl, Cls: classLetters(lvl),
			Src: e.Source, Msg: e.Msg, Fields: fields,
		})
	}
	return st
}

// renderLogs is the live daemon log viewer (mirrors the Fyne Logs tab). Multi-bus subTabs
// (App + MIDI/Traktor/Session when wired) + level/source/search filters + autoscroll + copy.
// livePush patches #log-view ~1 Hz, preserving scroll unless the user is already at the bottom.
// Render path: RZW1 binary state (v2) → JSON state (v1) → the Go renderer below (see wire.go).
func (u *UI) renderLogs() string {
	st := u.logsState()
	if zigui.Available() {
		if h, ok := zigWire("RenderLogsV2", wireLogsState(st), zigui.RenderLogsV2,
			zigui.RenderLogs, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return logsHTML(st)
}

// logLinesHTML renders the #log-view inner fragment (filter changes + ~1 Hz tick).
func (u *UI) logLinesHTML(n int) string {
	st := u.logsLinesState(n)
	if zigui.Available() {
		if h, ok := zigWire("RenderLogsLinesV2", wireLogsLines(st), zigui.RenderLogsLinesV2,
			zigui.RenderLogsLines, func() []byte { return stateJSON(st) }); ok {
			return h
		}
	}
	return logsLinesHTML(st)
}

// logsHTML is the pure Go renderer (golden reference; byte-identical to Zig).
func logsHTML(st logsState) string {
	var b strings.Builder
	b.WriteString(panel(st.Title, st.Sub))
	if st.ShowBus {
		items := make([][2]string, 0, len(st.BusItems))
		for _, it := range st.BusItems {
			items = append(items, [2]string{it.Val, it.Label})
		}
		b.WriteString(subTabs("logs-bus:", st.BusActive, items...))
	}
	// one wrap-row: filters + autoscroll + copy/clear + tailing note
	b.WriteString(`<div class=log-filters>`)
	b.WriteString(selHTML(st.Level))
	b.WriteString(selHTML(st.Source))
	b.WriteString(`<label class="field log-search" data-label="logs-search"><span class=field-label>` +
		html.EscapeString(st.SearchLabel) + `</span>` +
		`<input class=field-input type=text placeholder=` + attrQ(st.SearchPH) +
		` value=` + attrQ(st.SearchVal) + ` data-actinput="logs-search"></label>`)
	b.WriteString(toggleRowDL(st.AutoLabel, st.AutoDL, "logs-autoscroll", st.AutoOn))
	b.WriteString(btn(st.Copy, "outline", "logs-copy", "") +
		btn(st.Clear, "outline", "logs-clear", "") +
		`<span class=page-sub style="margin:0">` + html.EscapeString(st.Tailing) + `</span>`)
	b.WriteString(`</div>`)
	b.WriteString(`<div id=log-view class=log-view>` + logsLinesHTML(st.Lines) + `</div>`)
	return b.String()
}

// logsLinesHTML is the pure #log-view inner renderer.
func logsLinesHTML(st logsLines) string {
	if !st.Wired {
		return `<div class=log-line>` + html.EscapeString(st.NoBus) + `</div>`
	}
	if len(st.Entries) == 0 {
		return `<div class=log-line>` + html.EscapeString(st.NoEntries) + `</div>`
	}
	var b strings.Builder
	for _, e := range st.Entries {
		b.WriteString(`<div class=log-line>`)
		b.WriteString(html.EscapeString(e.Time))
		b.WriteString(` <span class="lv-` + e.Cls + `">` + html.EscapeString(padRight(e.Lvl, 5)) + `</span>`)
		b.WriteString(` <span class=lv-src>[` + html.EscapeString(e.Src) + `]</span> `)
		b.WriteString(html.EscapeString(e.Msg))
		if e.Fields != "" {
			b.WriteString(` ` + html.EscapeString(e.Fields))
		}
		b.WriteString(`</div>`)
	}
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
