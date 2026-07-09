package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midimap"
)

// MIDI-in (native MIDI-learn): connect physical DJ controllers, learn each control per channel,
// and feed the shared deck/channel model. Multiple controllers on different ports run at once,
// each with its own map. Optional per-controller THRU re-emits the raw input to a MIDI-OUT
// (a loopMIDI cable the DJ app reads) so rave-mate can read the controller AND the DJ app still
// gets it on single-client Windows MIDI. The two-port DJ bridge routes peer control out to the
// DJ app. Rekordbox can't emit play/cue state over a loopback (Button LED == input → self-loop),
// so we read it straight from the controller instead.

// midiControllersCard renders the connect + learn surface for physical controllers.
func (u *UI) midiControllersCard() string {
	if u.svc.MIDISource == nil || u.svc.Cfg == nil {
		return ""
	}
	ctrls := u.svc.Cfg.Features.MIDI.Controllers
	var b strings.Builder
	b.WriteString(`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.in.intro")) + ` ` + tipTopic("midi-learn-controllers") + `</p>`)
	if len(ctrls) == 0 {
		b.WriteString(emptyState(i18n.T("midictl.in.empty")))
	}
	for i := range ctrls {
		b.WriteString(u.midiControllerBlock(i, ctrls[i]))
	}
	b.WriteString(btnRow(btn(i18n.T("midictl.in.add"), "primary", "midi-ctl-add", "")))
	return card(i18n.T("midictl.in.card"), badge(i18n.T("midictl.in.badge"), "info"), b.String())
}

// midiControllerBlock renders one controller: port + enable + THRU + remove, then the learn grid.
func (u *UI) midiControllerBlock(i int, c config.MIDIControllerMap) string {
	idx := strconv.Itoa(i)
	portOpts := [][2]string{{"", i18n.T("midictl.in.pickPort")}}
	for _, p := range u.svc.MIDISource.InputPorts() {
		portOpts = append(portOpts, [2]string{p, p})
	}
	thruOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	if u.svc.MIDIEmit != nil {
		for _, p := range u.svc.MIDIEmit.Ports() {
			thruOpts = append(thruOpts, [2]string{p, p})
		}
	}
	head := selectBoxTip(i18n.T("midictl.in.port"), "midi-ctl-port:"+idx, portOpts, c.Port, "midi-in-port") +
		toggleRow(i18n.T("midictl.in.enabled"), "midi-ctl-enable:"+idx, c.Enabled) +
		selectBoxTip(i18n.T("midictl.in.thru"), "midi-ctl-thru:"+idx, thruOpts, c.ThruPort, "midi-thru") +
		btnRow(btn(i18n.T("midictl.in.remove"), "warn", "midi-ctl-remove:"+idx, ""))
	title := c.Name
	if title == "" {
		title = i18n.T("midictl.in.newCtl")
	}
	return `<div class=midi-ctlblock data-testid=` + attrQ("midi-ctl-"+idx) + `>` +
		`<div class=midi-ctlhead>` + htmlEscape(title) + `</div>` + head +
		u.midiLearnGrid(i, c) + `</div>`
}

// midiLearnGrid renders a controls×channels grid of learn chips (rows = controls, cols = channels).
func (u *UI) midiLearnGrid(ctlIdx int, c config.MIDIControllerMap) string {
	n := u.midiChannels()
	var b strings.Builder
	b.WriteString(`<div class=midi-learnhdr>` + htmlEscape(i18n.T("midictl.in.learnHdr")) + ` ` + tipTopic("midi-learn-grid") + `</div>`)
	b.WriteString(`<div class=midi-learngrid style="--cols:` + strconv.Itoa(n) + `">`)
	b.WriteString(`<div class=mlg-h></div>`)
	for ch := 1; ch <= n; ch++ {
		b.WriteString(`<div class=mlg-h>` + htmlEscape(i18n.T("midictl.in.chShort", i18n.A{"n": strconv.Itoa(ch)})) + `</div>`)
	}
	for _, ctl := range midimap.Controls {
		b.WriteString(`<div class=mlg-rowlbl>` + htmlEscape(i18n.T("midictl.ctl."+ctl.LabelKey)) + `</div>`)
		for ch := 1; ch <= n; ch++ {
			b.WriteString(u.midiLearnCell(ctlIdx, c, ctl.ID, ch))
		}
	}
	b.WriteString(`</div>`)
	return b.String()
}

// midiLearnCell renders one learn chip: bound (shows the MIDI + a clear ✕) or an empty "Learn".
func (u *UI) midiLearnCell(ctlIdx int, c config.MIDIControllerMap, control string, ch int) string {
	arg := fmt.Sprintf("%d:%s:%d", ctlIdx, control, ch)
	tid := fmt.Sprintf("midi-learn-%d-%s-%d", ctlIdx, control, ch)
	if bd, ok := findBinding(c, control, ch); ok {
		return `<span class=mlg-cell>` +
			`<button class="mlg-chip mlg-chip--set" data-act=` + attrQ("midi-learn:"+arg) + ` data-testid=` + attrQ(tid) +
			` title=` + attrQ(i18n.T("midictl.in.relearn")) + `>` + htmlEscape(bindingReadout(bd)) + `</button>` +
			`<button class="mlg-clear" data-act=` + attrQ("midi-unlearn:"+arg) + ` aria-label=` + attrQ(i18n.T("midictl.in.clear")) + `>✕</button>` +
			`</span>`
	}
	return `<button class=mlg-chip data-act=` + attrQ("midi-learn:"+arg) + ` data-testid=` + attrQ(tid) + `>` +
		htmlEscape(i18n.T("midictl.in.learn")) + `</button>`
}

// findBinding returns the binding for (control, channel) on c, if learned.
func findBinding(c config.MIDIControllerMap, control string, ch int) (config.MIDIBinding, bool) {
	for _, b := range c.Bindings {
		if b.Control == control && b.Channel == ch {
			return b, true
		}
	}
	return config.MIDIBinding{}, false
}

// bindingReadout formats a learned binding as "CC24" / "N20" for the chip.
func bindingReadout(b config.MIDIBinding) string {
	if b.Status&0xF0 == 0xB0 {
		return "CC" + strconv.Itoa(int(b.Data1))
	}
	return "N" + strconv.Itoa(int(b.Data1))
}

// midiBridgeCard renders the two-port loopMIDI DJ router (peer control → DJ; DJ output → us).
func (u *UI) midiBridgeCard() string {
	if u.svc.Cfg == nil || u.svc.MIDISource == nil {
		return ""
	}
	br := u.svc.Cfg.Features.MIDI.Bridge
	inOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	for _, p := range u.svc.MIDISource.InputPorts() {
		inOpts = append(inOpts, [2]string{p, p})
	}
	outOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	if u.svc.MIDIEmit != nil {
		for _, p := range u.svc.MIDIEmit.Ports() {
			outOpts = append(outOpts, [2]string{p, p})
		}
	}
	body := `<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.bridge.intro")) + ` ` + tipTopic("midi-bridge") + `</p>` +
		toggleRowTip(i18n.T("midictl.bridge.enable"), "midi-bridge-enable", br.Enabled, tipTopic("midi-bridge")) +
		selectBoxTip(i18n.T("midictl.bridge.todj"), "midi-bridge-todj", outOpts, br.ToDJPort, "midi-bridge") +
		selectBoxTip(i18n.T("midictl.bridge.fromdj"), "midi-bridge-fromdj", inOpts, br.FromDJPort, "midi-bridge")
	return card(i18n.T("midictl.bridge.card"), badge(i18n.T("midictl.bridge.badge"), "info"), body)
}
