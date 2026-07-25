package webui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/zigui"
)

// MIDI monitor + ravemidi wire trace: live "what is actually on the wire" views.
// Monitor = the child's raw-message bus (which controller sent what, decoded) -
// answers "which device is which" by pressing a control and watching the row light
// up. Trace = the driver's per-port ring (IOCTL_RAVEMIDI_QUERY_TRACE) - shows each
// hop (tap raw / to-app / read-pop / from-app / feedback) for on-hardware diagnosis.
//
// Both are Zig-rendered (native/zigui/src/midimon.zig): Go resolves state (bus
// snapshot + driver ioctl + i18n), Zig renders HTML byte-identical to the pure Go
// renderers below (fallback + golden reference, zigui_golden_midimon_test.go).

const midiMonRows = 14

// midiMonRow is one monitor line.
type midiMonRow struct {
	Ago string `json:"ago"`
	Src string `json:"src"`
	Msg string `json:"msg"`
}

// midiMonLines is the #midi-monitor inner state (~1 Hz patch target).
type midiMonLines struct {
	Empty string       `json:"empty"`
	Rows  []midiMonRow `json:"rows"`
}

// midiMonState is the resolved render state for the monitor card.
type midiMonState struct {
	Card  string       `json:"card"`
	Badge string       `json:"badge"`
	Sub   string       `json:"sub"`
	Lines midiMonLines `json:"lines"`
}

// midiTraceRow is one wire-trace hop.
type midiTraceRow struct {
	DT    string `json:"dt"`
	Dir   string `json:"dir"` // numeric trace dir (CSS suffix; digits only, unescaped)
	Label string `json:"label"`
	Hex   string `json:"hex"`
	Len   string `json:"len"` // "<n>B"
	Dec   string `json:"dec"`
}

// midiTraceState is the resolved render state for the driver wire-trace block.
type midiTraceState struct {
	Hdr     string         `json:"hdr"`
	HasErr  bool           `json:"hasErr"` // ioctl failed → Err as a warn hint
	Err     string         `json:"err"`
	Empty   string         `json:"empty"`
	Rows    []midiTraceRow `json:"rows"`
	Refresh string         `json:"refresh"`
	Close   string         `json:"close"`
}

// midiMonitorState resolves the monitor card's i18n + bus tail into render state.
func (u *UI) midiMonitorState() midiMonState {
	return midiMonState{
		Card:  i18n.T("midictl.mon.card"),
		Badge: i18n.T("midictl.mon.badge"),
		Sub:   i18n.T("midictl.mon.sub"),
		Lines: u.midiMonLinesState(),
	}
}

// midiMonLinesState resolves the newest-first tail of the raw-message bus.
func (u *UI) midiMonLinesState() midiMonLines {
	st := midiMonLines{Empty: i18n.T("midictl.mon.empty"), Rows: []midiMonRow{}}
	if u.svc.MIDIMon == nil {
		return st
	}
	entries := u.svc.MIDIMon.Snapshot()
	n := 0
	for i := len(entries) - 1; i >= 0 && n < midiMonRows; i-- {
		e := entries[i]
		st.Rows = append(st.Rows, midiMonRow{Ago: agoShort(e.Time), Src: e.Source, Msg: e.Msg})
		n++
	}
	return st
}

// The monitor card + wire trace render as part of the MIDI tab's one state pass
// (midiCtlState → zigui.RenderMIDICtl); rz_ui_render_midimon / _miditrace stay exported
// as the per-fragment golden-gate entry points. Only the monitor ROWS have a live patch
// site of their own (~1 Hz tick).

// midiMonitorInner: newest-first rows of the raw-message bus.
func (u *UI) midiMonitorInner() string {
	st := u.midiMonLinesState()
	if zigui.Available() {
		if h, ok := zigui.RenderMIDIMonRows(stateJSON(st)); ok {
			return h
		}
	}
	return midiMonRowsHTML(st)
}

// midiMonHTML is the pure Go monitor-card renderer (golden reference).
func midiMonHTML(st midiMonState) string {
	body := `<p class=page-sub>` + htmlEscape(st.Sub) + `</p>` +
		`<div id=midi-monitor>` + midiMonRowsHTML(st.Lines) + `</div>`
	return card(st.Card, badge(st.Badge, "info"), body)
}

// midiMonRowsHTML is the pure #midi-monitor inner renderer.
func midiMonRowsHTML(st midiMonLines) string {
	if len(st.Rows) == 0 {
		return emptyState(st.Empty)
	}
	var b strings.Builder
	b.WriteString(`<div class=midi-monrows>`)
	for _, r := range st.Rows {
		b.WriteString(`<div class=midi-monrow><span class=midi-mont>` + htmlEscape(r.Ago) + `</span>` +
			`<span class=midi-monsrc>` + htmlEscape(r.Src) + `</span>` +
			`<span class=midi-monmsg>` + htmlEscape(r.Msg) + `</span></div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

// midiLastActivity returns the newest monitor entry per source (controller name).
func (u *UI) midiLastActivity() map[string]logbus.Entry {
	if u.svc.MIDIMon == nil {
		return nil
	}
	out := map[string]logbus.Entry{}
	for _, e := range u.svc.MIDIMon.Snapshot() {
		out[e.Source] = e // snapshot is oldest-first: last write wins
	}
	return out
}

// agoShort formats a compact relative age ("2s", "5m", "1h").
func agoShort(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Second:
		return i18n.T("midictl.mon.now")
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
}

// midiTraceDirKey maps a trace dir to its i18n label key.
func midiTraceDirKey(dir uint32) string {
	switch dir {
	case midi.TraceDirTapRaw:
		return "midictl.trace.tapraw"
	case midi.TraceDirToApp:
		return "midictl.trace.toapp"
	case midi.TraceDirReadPop:
		return "midictl.trace.readpop"
	case midi.TraceDirFromApp:
		return "midictl.trace.fromapp"
	case midi.TraceDirFeedbackOut:
		return "midictl.trace.feedback"
	case midi.TraceDirLoopDrop:
		return "midictl.trace.loopdrop"
	default:
		return "midictl.trace.unknown"
	}
}

// midiTraceStateFor queries the driver ring for portID and resolves it into render state.
func midiTraceStateFor(portID uint32) midiTraceState {
	es, err := midi.QueryDriverTrace(portID)
	st := midiTraceState{
		Hdr:     i18n.T("midictl.trace.hdr", i18n.A{"id": strconv.Itoa(int(portID))}),
		Empty:   i18n.T("midictl.trace.empty"),
		Rows:    []midiTraceRow{},
		Refresh: i18n.T("midictl.trace.refresh"),
		Close:   i18n.T("midictl.trace.close"),
	}
	if err != nil {
		st.HasErr, st.Err = true, err.Error()
		return st
	}
	var prev uint64
	for _, e := range es {
		dt := ""
		if prev != 0 && e.Time100ns >= prev {
			dt = "+" + strconv.FormatUint((e.Time100ns-prev)/10000, 10) + "ms"
		}
		prev = e.Time100ns
		hexs := make([]string, 0, len(e.Bytes))
		for _, x := range e.Bytes {
			hexs = append(hexs, fmt.Sprintf("%02X", x))
		}
		dec := ""
		// short messages decode to human text; raw/oversize events stay hex-only
		if e.Dir != midi.TraceDirTapRaw && e.Len <= 3 && len(e.Bytes) > 0 {
			m := midi.Message{Status: e.Bytes[0]}
			if len(e.Bytes) > 1 {
				m.Data1 = e.Bytes[1]
			}
			if len(e.Bytes) > 2 {
				m.Data2 = e.Bytes[2]
			}
			dec = m.Describe()
		}
		st.Rows = append(st.Rows, midiTraceRow{
			DT: dt, Dir: strconv.Itoa(int(e.Dir)), Label: i18n.T(midiTraceDirKey(e.Dir)),
			Hex: strings.Join(hexs, " "), Len: strconv.Itoa(int(e.Len)) + "B", Dec: dec,
		})
	}
	return st
}

// midiTraceHTML is the pure Go wire-trace renderer (golden reference).
func midiTraceHTML(st midiTraceState) string {
	var b strings.Builder
	b.WriteString(`<div class=midi-trace><div class=pb-label>` + htmlEscape(st.Hdr) + `</div>`)
	switch {
	case st.HasErr:
		b.WriteString(hint("warn", st.Err))
	case len(st.Rows) == 0:
		b.WriteString(emptyState(st.Empty))
	default:
		b.WriteString(`<div class=midi-tracerows>`)
		for _, r := range st.Rows {
			b.WriteString(`<div class=midi-tracerow><span class=midi-tracedt>` + htmlEscape(r.DT) + `</span>` +
				`<span class="midi-tracedir midi-tracedir--` + r.Dir + `">` +
				htmlEscape(r.Label) + `</span>` +
				`<span class=midi-tracehex>` + htmlEscape(r.Hex) + `</span>` +
				`<span class=midi-tracelen>` + htmlEscape(r.Len) + `</span>` +
				`<span class=midi-tracedec>` + htmlEscape(r.Dec) + `</span></div>`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(btnRow(
		btn(st.Refresh, "outline", "midi-drv-trace-refresh", ""),
		btn(st.Close, "ghost", "midi-drv-trace:0", "")))
	b.WriteString(`</div>`)
	return b.String()
}
