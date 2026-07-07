package webui

import "rave.page/mate/internal/i18n"

// Logs-tab filter actions. Registered in init() so onAction routes them via dispatch.

func init() {
	onPrefix("logs-bus:", func(u *UI, m actMsg) { u.logSetBus(m.arg("logs-bus:")) })
	onExact("logs-level", func(u *UI, m actMsg) { u.logSetLevel(m.Val) })
	onExact("logs-source", func(u *UI, m actMsg) { u.logSetSource(m.Val) })
	onExact("logs-search", func(u *UI, m actMsg) { u.logSetSearch(m.Val) })
	onExact("logs-autoscroll", func(u *UI, m actMsg) { u.logSetAutoscroll(m.Val == "true") })
	onExact("logs-copy", func(u *UI, _ actMsg) { u.logCopyAll() })
}

// logSetBus switches the active bus; resets the source filter (each bus has its own sources)
// and re-renders the whole tab (new subtab + fresh source options).
func (u *UI) logSetBus(bus string) {
	u.logMu.Lock()
	u.logBus = bus
	u.logSource = ""
	u.logMu.Unlock()
	u.patchMain()
}

func (u *UI) logSetLevel(v string) {
	u.logMu.Lock()
	u.logLevel = v
	u.logMu.Unlock()
	u.patchLogView()
}

func (u *UI) logSetSource(v string) {
	u.logMu.Lock()
	u.logSource = v
	u.logMu.Unlock()
	u.patchLogView()
}

func (u *UI) logSetSearch(v string) {
	u.logMu.Lock()
	u.logSearch = v
	u.logMu.Unlock()
	u.patchLogView()
}

func (u *UI) logSetAutoscroll(on bool) {
	u.logMu.Lock()
	u.logAutoscroll = on
	u.logMu.Unlock()
}

func (u *UI) logCopyAll() {
	u.eval("navigator.clipboard&&navigator.clipboard.writeText(" + jsQuote(u.logPlainText()) + ")")
	u.toast(i18n.T("logs.copied"))
}

// patchLogView replaces #log-view with the filtered lines; scrolls to bottom when autoscroll is on.
func (u *UI) patchLogView() {
	u.eval("var lv=document.getElementById('log-view');if(lv){lv.innerHTML=" +
		jsQuote(u.logLinesHTML(logTailN)) + ";if(" + u.logAutoscrollJS() + ")lv.scrollTop=lv.scrollHeight;}")
}

// logAutoscrollJS returns "true"/"false" for splicing into an eval'd expression.
func (u *UI) logAutoscrollJS() string {
	u.logMu.Lock()
	defer u.logMu.Unlock()
	if u.logAutoscroll {
		return "true"
	}
	return "false"
}
