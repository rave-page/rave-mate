package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midi"
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
	b.WriteString(virtualMIDILinksRow())
	if len(ctrls) == 0 {
		b.WriteString(emptyState(i18n.T("midictl.in.empty")))
	}
	for i := range ctrls {
		b.WriteString(u.midiControllerBlock(i, ctrls[i]))
	}
	b.WriteString(btnRow(btn(i18n.T("midictl.in.add"), "primary", "midi-ctl-add", "")))
	return card(i18n.T("midictl.in.card"), badge(i18n.T("midictl.in.badge"), "info"), b.String())
}

// virtualMIDILinksRow renders the "need a virtual MIDI port?" line with the driver download
// links (same list as the tooltips). Gives users flexibility over which loopback driver to use.
func virtualMIDILinksRow() string {
	var b strings.Builder
	b.WriteString(`<p class=midi-driver-links><span class=midi-driver-lbl>` + htmlEscape(i18n.T("midictl.in.getPort")) + `</span> `)
	for i, l := range virtualMIDILinks() {
		if i > 0 {
			b.WriteString(` · `)
		}
		b.WriteString(`<a href=` + attrQ(l.URL) + ` target=_blank rel=noopener>` + htmlEscape(l.Label) + `</a>`)
	}
	b.WriteString(`</p>`)
	return b.String()
}

// midiControllerBlock renders one controller: port + enable + THRU + remove, then the learn grid.
func (u *UI) midiControllerBlock(i int, c config.MIDIControllerMap) string {
	idx := strconv.Itoa(i)
	portOpts := [][2]string{{"", i18n.T("midictl.in.pickPort")}}
	for _, p := range u.svc.MIDISource.InputPorts() {
		if p == midi.VirtualDJPortName || p == midi.VirtualMixerPortName {
			continue // our own one-way ports - reading them back would loop through rave-mate
		}
		portOpts = append(portOpts, [2]string{p, p})
	}
	thruOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	// Built-in one-way port first (recommended): the DJ app sees an input-only port, so
	// its automatic LED echo has no output endpoint to loop back through.
	if midi.OneWayAvailable() {
		thruOpts = append(thruOpts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
	if u.svc.MIDIEmit != nil {
		for _, p := range u.svc.MIDIEmit.Ports() {
			thruOpts = append(thruOpts, [2]string{p, p})
		}
	}
	head := selectBoxTip(i18n.T("midictl.in.port"), "midi-ctl-port:"+idx, portOpts, c.Port, "midi-in-port") +
		`<div id="midi-ctlstat-` + idx + `">` + u.midiCtlPortStatusInner(c) + `</div>` +
		toggleRow(i18n.T("midictl.in.enabled"), "midi-ctl-enable:"+idx, c.Enabled) +
		selectBoxTip(i18n.T("midictl.in.thru"), "midi-ctl-thru:"+idx, thruOpts, c.ThruPort, "midi-thru") +
		u.midiThruWarn(i, c) +
		btnRow(btn(i18n.T("midictl.in.remove"), "warn", "midi-ctl-remove:"+idx, ""))
	title := c.Name
	if title == "" {
		title = i18n.T("midictl.in.newCtl")
	}
	return `<div class=midi-ctlblock data-testid=` + attrQ("midi-ctl-"+idx) + `>` +
		`<div class=midi-ctlhead>` + htmlEscape(title) + `</div>` + head +
		u.midiLearnGrid(i, c) + `</div>`
}

// midiCtlPortStatusInner renders the open/failed status for the controller's input port. Only
// shown once the MIDI child has reported (some port opened or failed); "in use" points at the
// exact fix (Windows single-client MIDI: close the other app, or route via loopMIDI THRU). No
// tooltip here - this region is live-patched (~1 Hz), which would wipe a pinned tooltip; the
// full explanation lives on the port select's ⓘ. It flips to "reading" when auto-retry recovers
// the port after the holding app releases it.
func (u *UI) midiCtlPortStatusInner(c config.MIDIControllerMap) string {
	if c.Port == "" || !c.Enabled || u.svc.MIDISource == nil {
		return ""
	}
	open := u.svc.MIDISource.OpenInputPorts()
	failed := u.svc.MIDISource.FailedInputPorts()
	if len(open) == 0 && len(failed) == 0 {
		return "" // child hasn't reported yet - don't guess
	}
	if portContains(open, c.Port) {
		return statusRow("ok", i18n.T("midictl.in.portStatus"), i18n.T("midictl.in.portReading"))
	}
	return statusRow("warn", i18n.T("midictl.in.portStatus"), i18n.T("midictl.in.portInUseShort")) +
		`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.in.portInUseHint")) + `</p>`
}

// midiThruWarn warns when a controller's THRU port is one rave-mate itself reads (its Traktor
// custom-map / Denon input, this or another controller's input, or the bridge from-DJ port).
// Windows MIDI-in is single-client, so a port rave-mate holds can't ALSO be opened by the DJ
// app - the THRU lands somewhere the DJ app can never read, and MIDI-learn silently sees
// nothing. The fix is a DEDICATED virtual cable for the THRU. Empty = no clash.
func (u *UI) midiThruWarn(self int, c config.MIDIControllerMap) string {
	if c.ThruPort == "" || u.svc.Cfg == nil {
		return ""
	}
	tp := strings.ToLower(strings.TrimSpace(c.ThruPort))
	if tp == "" {
		return ""
	}
	if tp == strings.ToLower(midi.VirtualDJSentinel) {
		// One-way port: no output endpoint exists, so nothing can loop - unless the
		// controller INPUT is (hand-configured to) our own virtual port.
		if !strings.EqualFold(strings.TrimSpace(c.Port), midi.VirtualDJPortName) {
			return ""
		}
		return statusRow("warn", i18n.T("midictl.in.thruClash"), i18n.T("midictl.in.thruClashShort")) +
			`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.in.thruClashHint")) + `</p>`
	}
	same := func(p string) bool {
		p = strings.ToLower(strings.TrimSpace(p))
		return p != "" && p == tp
	}
	m := u.svc.Cfg.Features.MIDI
	clash := same(m.CustomPort) || same(m.DenonPort) || same(m.Bridge.FromDJPort) || same(c.Port)
	if !clash {
		for i, o := range m.Controllers {
			if i != self && same(o.Port) {
				clash = true
				break
			}
		}
	}
	if !clash {
		return ""
	}
	return statusRow("warn", i18n.T("midictl.in.thruClash"), i18n.T("midictl.in.thruClashShort")) +
		`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.in.thruClashHint")) + `</p>`
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
		if p == midi.VirtualDJPortName || p == midi.VirtualMixerPortName {
			continue // our own one-way ports - reading them back would loop through rave-mate
		}
		inOpts = append(inOpts, [2]string{p, p})
	}
	outOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	if midi.OneWayAvailable() { // same one-way port as THRU (shared instance in the child)
		outOpts = append(outOpts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
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
