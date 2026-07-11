package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/logbus"
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

// midiCtlRenderCtx is per-render shared state: driver bind status by input id +
// latest monitor activity by source (one query/snapshot per render, not per block).
type midiCtlRenderCtx struct {
	drv map[string]midi.DriverInputStatus
	act map[string]logbus.Entry
}

// midiCtlCtx builds the shared render context.
func (u *UI) midiCtlCtx() midiCtlRenderCtx {
	ctx := midiCtlRenderCtx{act: u.midiLastActivity()}
	if midi.DriverInstalled() {
		if sts, err := midi.QueryDriverInputs(); err == nil {
			ctx.drv = map[string]midi.DriverInputStatus{}
			for _, st := range sts {
				ctx.drv[st.ID] = st
			}
		}
	}
	return ctx
}

// midiControllersCard renders the connect + learn surface for physical controllers.
func (u *UI) midiControllersCard() string {
	if u.svc.MIDISource == nil || u.svc.Cfg == nil {
		return ""
	}
	ctrls := u.svc.Cfg.Features.MIDI.Controllers
	ctx := u.midiCtlCtx()
	var b strings.Builder
	b.WriteString(`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.in.intro")) + ` ` + tipTopic("midi-learn-controllers") + `</p>`)
	b.WriteString(virtualMIDILinksRow())
	if len(ctrls) == 0 {
		b.WriteString(emptyState(i18n.T("midictl.in.empty")))
	}
	for i := range ctrls {
		b.WriteString(u.midiControllerBlock(i, ctrls[i], ctx))
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

// midiOwnDriverPorts collects the names of ports the ravemidi driver derives from the
// current config (reserved + fan-outs) - internal/DJ-facing, never controller INPUTs.
func (u *UI) midiOwnDriverPorts() map[string]bool {
	out := map[string]bool{}
	for _, c := range u.svc.Cfg.Features.MIDI.Controllers {
		if c.ThruPort == midi.DriverSentinel && c.Name != "" {
			out[strings.ToLower(midi.ReservedPortName(c.Name))] = true
			out[strings.ToLower(midi.DJPortName(c.Name))] = true
		}
	}
	return out
}

// midiControllerBlock renders one controller: port + enable + routing + remove, then the learn grid.
func (u *UI) midiControllerBlock(i int, c config.MIDIControllerMap, ctx midiCtlRenderCtx) string {
	idx := strconv.Itoa(i)
	own := u.midiOwnDriverPorts()
	portOpts := [][2]string{{"", i18n.T("midictl.in.pickPort")}}
	for _, p := range u.svc.MIDISource.InputPorts() {
		if p == midi.VirtualDJPortName || p == midi.VirtualMixerPortName ||
			own[strings.ToLower(p)] || strings.Contains(p, "(rave-mate)") {
			continue // our own virtual ports - reading them back would loop through rave-mate
		}
		portOpts = append(portOpts, [2]string{p, p})
	}
	thruOpts := [][2]string{{"", i18n.T("midictl.in.thruNone")}}
	// ravemidi driver first (recommended): the DRIVER taps the hardware and fans it
	// out loop-free; forwarding survives rave-mate exit and reboots.
	if midi.DriverInstalled() {
		thruOpts = append(thruOpts, [2]string{midi.DriverSentinel, i18n.T("midictl.in.thruDriver")})
	}
	// Built-in one-way port: the DJ app sees an input-only port, so its automatic
	// LED echo has no output endpoint to loop back through.
	if midi.OneWayAvailable() {
		thruOpts = append(thruOpts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
	if u.svc.MIDIEmit != nil {
		for _, p := range u.svc.MIDIEmit.Ports() {
			thruOpts = append(thruOpts, [2]string{p, p})
		}
	}
	head := selectBoxTip(i18n.T("midictl.in.port"), "midi-ctl-port:"+idx, portOpts, c.Port, "midi-in-port") +
		`<div id="midi-ctlstat-` + idx + `">` + u.midiCtlPortStatusInner(c, ctx) + `</div>` +
		toggleRow(i18n.T("midictl.in.enabled"), "midi-ctl-enable:"+idx, c.Enabled) +
		selectBoxTip(i18n.T("midictl.in.thru"), "midi-ctl-thru:"+idx, thruOpts, c.ThruPort, "midi-thru") +
		u.midiDriverThruHTML(i, c, ctx) +
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

// midiDriverThruHTML renders the driver-managed routing block for a controller whose
// THRU is the ravemidi driver: which port the DJ software must use, live bind state,
// and the fan-out message-filter chips. Empty for other THRU modes.
func (u *UI) midiDriverThruHTML(i int, c config.MIDIControllerMap, ctx midiCtlRenderCtx) string {
	if c.ThruPort != midi.DriverSentinel {
		return ""
	}
	idx := strconv.Itoa(i)
	var b strings.Builder
	b.WriteString(`<div class=midi-drvthru>`)
	// the ONE port to select in the DJ software - the core "which device do I use" answer
	b.WriteString(`<div class=midi-drvuse>` + htmlEscape(i18n.T("midictl.in.useInDJ")) +
		` <code>` + htmlEscape(midi.DJPortName(c.Name)) + `</code></div>`)
	b.WriteString(`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.in.driverNote")) + `</p>`)
	if st, ok := ctx.drv[c.Name]; ok {
		variant, line := "warning", i18n.T("midictl.drv.retrying", i18n.A{"n": strconv.Itoa(int(st.RetryCount))})
		if st.Bound {
			variant, line = "success", i18n.T("midictl.drv.bound")
			if st.FeedbackBound {
				line += " · " + i18n.T("midictl.drv.feedback")
			}
		}
		b.WriteString(statusRow(variant, i18n.T("midictl.in.driverState"), line))
	} else if midi.DriverInstalled() {
		b.WriteString(statusRow("muted", i18n.T("midictl.in.driverState"), i18n.T("midictl.in.driverPending")))
	}
	// fan-out filter chips: default drops mapping-hostile clutter (aftertouch caught by
	// MIDI-learn = "every key fires the binding")
	fl := c.DriverFilter
	if fl == nil {
		fl = midi.DefaultDriverFilter()
	}
	on := map[string]bool{}
	for _, k := range fl {
		on[k] = true
	}
	b.WriteString(`<div class=midi-drvfilters><span class=midi-steplbl>` +
		htmlEscape(i18n.T("midictl.in.filterLbl")) + ` ` + tipTopic("midi-drv-filter") + `</span>`)
	for _, f := range midi.FilterKeys {
		b.WriteString(fchip(i18n.T("midictl.filter."+f.Key), "", "midi-ctl-filter:"+idx+":"+f.Key, on[f.Key]))
	}
	b.WriteString(`</div></div>`)
	return b.String()
}

// midiCtlPortStatusInner renders the open/failed status for the controller's input port. Only
// shown once the MIDI child has reported (some port opened or failed); "in use" points at the
// exact fix (Windows single-client MIDI: close the other app, or route via loopMIDI THRU). No
// tooltip here - this region is live-patched (~1 Hz), which would wipe a pinned tooltip; the
// full explanation lives on the port select's ⓘ. It flips to "reading" when auto-retry recovers
// the port after the holding app releases it. Driver-managed controllers read the reserved
// per-input port (the driver holds the hardware), so status checks that port instead.
// A live activity line (latest decoded message + age) answers "which device is THIS one".
func (u *UI) midiCtlPortStatusInner(c config.MIDIControllerMap, ctx midiCtlRenderCtx) string {
	if c.Port == "" || !c.Enabled || u.svc.MIDISource == nil {
		return ""
	}
	want := c.Port
	if c.ThruPort == midi.DriverSentinel && midi.DriverInstalled() {
		want = midi.ReservedPortName(c.Name)
	}
	var out string
	open := u.svc.MIDISource.OpenInputPorts()
	failed := u.svc.MIDISource.FailedInputPorts()
	switch {
	case len(open) == 0 && len(failed) == 0:
		// child hasn't reported yet - don't guess
	case portContains(open, want):
		out = statusRow("ok", i18n.T("midictl.in.portStatus"), i18n.T("midictl.in.portReading"))
	default:
		out = statusRow("warn", i18n.T("midictl.in.portStatus"), i18n.T("midictl.in.portInUseShort")) +
			`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.in.portInUseHint")) + `</p>`
	}
	if e, ok := ctx.act[c.Name]; ok {
		out += `<div class=midi-activity><span class=midi-actdot></span>` +
			htmlEscape(i18n.T("midictl.in.lastInput", i18n.A{"ago": agoShort(e.Time)})) +
			` <span class=midi-actmsg>` + htmlEscape(e.Msg) + `</span></div>`
	}
	return out
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
