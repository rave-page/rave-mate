//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Logs golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states. Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// logsFixtures: unavailable (unwired bus), empty, populated, escaping edge, long values, unicode.
func logsFixtures() map[string]logsState {
	base := func() logsState {
		return logsState{
			Title: "Logs", Sub: "Live daemon logs",
			BusActive: "app", BusItems: []logsTab{{Val: "app", Label: "App"}},
			Level: selState{ID: "logs-level", Label: "Level", CurLabel: "All",
				Rows: []selRow{{Val: "all", Label: "All", Cur: true}, {Val: "info", Label: "Info"}, {Val: "warn", Label: "Warnings"}, {Val: "error", Label: "Errors"}}},
			Source: selState{ID: "logs-source", Label: "Source", CurLabel: "All sources",
				Rows: []selRow{{Val: "", Label: "All sources", Cur: true}}},
			SearchLabel: "Search", SearchPH: "Filter text…", SearchVal: "",
			AutoLabel: "Autoscroll", AutoDL: "autoscroll", AutoOn: true,
			Copy: "Copy all", Clear: "Clear view", Tailing: "Tailing newest 400 lines",
			Lines: logsLines{Wired: true, NoBus: "No log bus", NoEntries: "No entries", Entries: []logsEntry{}},
		}
	}
	unavailable := base()
	unavailable.Lines = logsLines{NoBus: "No log bus wired", NoEntries: "No entries", Entries: []logsEntry{}}

	empty := base()

	populated := base()
	populated.ShowBus = true
	populated.BusActive = "midi"
	populated.BusItems = []logsTab{{Val: "app", Label: "App"}, {Val: "midi", Label: "MIDI"}, {Val: "traktor", Label: "Traktor"}}
	populated.Source = selState{ID: "logs-source", Label: "Source", CurLabel: "traktor", Open: true, Filter: "tra",
		Rows: []selRow{{Val: "traktor", Label: "traktor", Cur: true}}}
	populated.SearchVal = "deck"
	populated.AutoOn = false
	populated.Lines.Entries = []logsEntry{
		{Time: "09:15:01.250", Lvl: "DEBUG", Cls: "DEBUG", Src: "session", Msg: "merge tick"},
		{Time: "09:15:02.000", Lvl: "INFO", Cls: "INFO", Src: "traktor", Msg: "deck A load", Fields: "map[bpm:128]"},
		{Time: "09:15:03.700", Lvl: "WARN", Cls: "WARN", Src: "obs", Msg: "reconnect"},
		{Time: "09:15:04.100", Lvl: "ERROR", Cls: "ERROR", Src: "stream", Msg: "ingest 503"},
	}

	escaping := base()
	escaping.Title = `R&B <"Logs"> 'live'`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.ShowBus = true
	escaping.BusItems = []logsTab{{Val: "app", Label: `A&pp <"x">`}, {Val: "midi", Label: `M'IDI&`}}
	escaping.Level = selState{ID: "logs-level", Label: `Le&vel<">`, CurLabel: `A"ll&'`, Open: true, Filter: `f&"x'<`,
		Rows: []selRow{{Val: `v&"1'<>`, Label: `L&"1'<>`, Sub: `s&"u'<>`, Badge: `b&"a'<>`, Cur: true}}}
	escaping.SearchLabel = `Se&arch"`
	escaping.SearchPH = `p<h>&"'`
	escaping.SearchVal = `q&"v'<>`
	escaping.AutoLabel = `Auto&"scroll'<>`
	escaping.AutoDL = `auto&"scroll'<>`
	escaping.Copy = `C&opy"`
	escaping.Clear = `C<lear>'`
	escaping.Tailing = `t&ail"<>'`
	escaping.Lines.Entries = []logsEntry{
		{Time: "10:00:00.000", Lvl: "INFO", Cls: "INFO", Src: `sr&c<">`, Msg: `msg &<>"' end`, Fields: `map[k:<&"'>]`},
	}

	long := base()
	longS := strings.Repeat("very-long-", 120)
	long.Lines.Entries = []logsEntry{
		{Time: "11:11:11.111", Lvl: "WARNING", Cls: "WARNING", Src: strings.Repeat("s", 300), Msg: longS, Fields: "map[" + strings.Repeat("k", 500) + ":1]"},
	}
	long.SearchVal = longS
	long.Source = selState{ID: "logs-source", Label: "Source", CurLabel: strings.Repeat("x", 400), Open: true,
		Rows: []selRow{{Val: strings.Repeat("v", 300), Label: strings.Repeat("x", 400), Cur: true}}}

	unicode := base()
	unicode.Title = "ログ 🎧"
	unicode.Sub = "größer Журнал"
	unicode.ShowBus = true
	unicode.BusItems = []logsTab{{Val: "app", Label: "Приложение"}, {Val: "midi", Label: "ミディ"}}
	unicode.BusActive = "app"
	unicode.AutoLabel = "Автопрокрутка"
	unicode.AutoDL = "автопрокрутка"
	unicode.Lines.Entries = []logsEntry{
		{Time: "12:00:00.000", Lvl: "INFO", Cls: "INFO", Src: "сессия", Msg: "中文 emoji 🎛️ ラヴ", Fields: "map[ключ:значение]"},
	}
	return map[string]logsState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigLogsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range logsFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderLogs(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", logsHTML(st), zig)

			ljs := stateJSON(st.Lines)
			if ljs == nil {
				t.Fatal("lines marshal failed")
			}
			zigLines, ok := zigui.RenderLogsLines(ljs)
			if !ok {
				t.Fatal("zig lines render failed")
			}
			assertBytesEqual(t, "lines", logsLinesHTML(st.Lines), zigLines)
		})
	}
}
