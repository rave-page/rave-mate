package webui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
)

// MIDI monitor + ravemidi wire trace: live "what is actually on the wire" views.
// Monitor = the child's raw-message bus (which controller sent what, decoded) -
// answers "which device is which" by pressing a control and watching the row light
// up. Trace = the driver's per-port ring (IOCTL_RAVEMIDI_QUERY_TRACE) - shows each
// hop (tap raw / to-app / read-pop / from-app / feedback) for on-hardware diagnosis.

const midiMonRows = 14

// midiMonitorCard renders the live input monitor (patched ~1 Hz via #midi-monitor).
func (u *UI) midiMonitorCard() string {
	if u.svc.MIDIMon == nil {
		return ""
	}
	body := `<p class=page-sub>` + htmlEscape(i18n.T("midictl.mon.sub")) + `</p>` +
		`<div id=midi-monitor>` + u.midiMonitorInner() + `</div>`
	return card(i18n.T("midictl.mon.card"), badge(i18n.T("midictl.mon.badge"), "info"), body)
}

// midiMonitorInner: newest-first rows of the raw-message bus.
func (u *UI) midiMonitorInner() string {
	entries := u.svc.MIDIMon.Snapshot()
	if len(entries) == 0 {
		return emptyState(i18n.T("midictl.mon.empty"))
	}
	var b strings.Builder
	b.WriteString(`<div class=midi-monrows>`)
	n := 0
	for i := len(entries) - 1; i >= 0 && n < midiMonRows; i-- {
		e := entries[i]
		b.WriteString(`<div class=midi-monrow><span class=midi-mont>` + htmlEscape(agoShort(e.Time)) + `</span>` +
			`<span class=midi-monsrc>` + htmlEscape(e.Source) + `</span>` +
			`<span class=midi-monmsg>` + htmlEscape(e.Msg) + `</span></div>`)
		n++
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

// midiDrvTraceHTML renders the wire-trace table for the open port (u.midiTrace).
func (u *UI) midiDrvTraceHTML() string {
	if u.midiTrace == 0 {
		return ""
	}
	es, err := midi.QueryDriverTrace(u.midiTrace)
	var b strings.Builder
	b.WriteString(`<div class=midi-trace><div class=pb-label>` +
		htmlEscape(i18n.T("midictl.trace.hdr", i18n.A{"id": strconv.Itoa(int(u.midiTrace))})) + `</div>`)
	switch {
	case err != nil:
		b.WriteString(hint("warn", err.Error()))
	case len(es) == 0:
		b.WriteString(emptyState(i18n.T("midictl.trace.empty")))
	default:
		b.WriteString(`<div class=midi-tracerows>`)
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
			b.WriteString(`<div class=midi-tracerow><span class=midi-tracedt>` + htmlEscape(dt) + `</span>` +
				`<span class="midi-tracedir midi-tracedir--` + strconv.Itoa(int(e.Dir)) + `">` +
				htmlEscape(i18n.T(midiTraceDirKey(e.Dir))) + `</span>` +
				`<span class=midi-tracehex>` + htmlEscape(strings.Join(hexs, " ")) + `</span>` +
				`<span class=midi-tracelen>` + htmlEscape(strconv.Itoa(int(e.Len))+"B") + `</span>` +
				`<span class=midi-tracedec>` + htmlEscape(dec) + `</span></div>`)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(btnRow(
		btn(i18n.T("midictl.trace.refresh"), "outline", "midi-drv-trace-refresh", ""),
		btn(i18n.T("midictl.trace.close"), "ghost", "midi-drv-trace:0", "")))
	b.WriteString(`</div>`)
	return b.String()
}
