package webui

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/midimap"
)

// MIDI Mixer panel: a visual DJ-mixer surface (per-channel EQ Hi/Mid/Low, Filter, Trim knobs +
// a vertical Fader + Play/Cue) that SENDS MIDI to a loopback port so a DJ app (Rekordbox / Serato
// etc.) can MIDI-learn custom mappings. Send-on-interaction only - never a continuous stream. The
// send CCs are the shared midimap contract, byte-identical to the receive decoder
// (session/sources/midisrc/custom.go), so a learned mapping's output/feedback echoes back on the
// same CC and drives the overlays. ctl-drivable: every control carries data-testid=midi-ch<n>-<id>.

func (u *UI) renderMIDICtl() string {
	if u.svc.MIDIEmit == nil {
		return u.renderPlaceholder("midictl")
	}
	ctx := u.midiCtlCtx() // ONE cached OS-probe snapshot for the whole tab (ports + driver status)
	var b strings.Builder
	b.WriteString(panel(i18n.T("tab.midictl"), i18n.T("midictl.subtitle")))
	b.WriteString(u.midiControllersCard(ctx)) // native MIDI-learn: read physical controllers (input)
	b.WriteString(u.midiUIMapCard())          // map controller notes/CCs/encoders to app actions
	b.WriteString(u.midiMonitorCard())        // live input monitor ("which device is which")
	// output + driver / bridge + help: small cards pair up ≥1100px (.midi-2col)
	b.WriteString(`<div class=midi-2col>` + u.midiPortCard(ctx) + u.midiDriverCard(ctx) + `</div>`)
	b.WriteString(u.midiRackCard())
	b.WriteString(`<div class=midi-2col>` + u.midiBridgeCard(ctx) + u.midiHelpCard() + `</div>`)
	return b.String()
}

// midiPortCard renders the output-port selector, the live active-port line, and Panic.
func (u *UI) midiPortCard(ctx midiCtlRenderCtx) string {
	e := u.svc.MIDIEmit
	opts := [][2]string{{"", i18n.T("midictl.autoPort")}}
	// Built-in one-way port first: kills the DJ app's LED-echo self-loop (ravemidi driver
	// preferred, teVirtualMIDI fallback - see midi.OpenOneWayOut).
	if ctx.oneWay {
		opts = append(opts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
	for _, p := range ctx.outPorts {
		opts = append(opts, [2]string{p, p})
	}
	body := `<p class=page-sub>` + htmlEscape(i18n.T("midictl.out.sub")) + `</p>` +
		selectBox(i18n.T("midictl.port"), "midi-port", opts, e.Want()) +
		`<div id=midi-active>` + midiActiveRow(e.ActivePort()) + `</div>` +
		btnRow(btn(i18n.T("midictl.panic"), "warn", "midi-panic", ""))
	return card(i18n.T("midictl.outputCard"), "", body)
}

// midiDriverCard: ravemidi kernel-driver status + the self-signed install walkthrough.
// Only rendered on Windows (the driver is a Windows WDM/PortCls component).
func (u *UI) midiDriverCard(ctx midiCtlRenderCtx) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	installed := ctx.drvInstalled
	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + htmlEscape(i18n.T("midictl.drv.why")) + `</p>`)
	switch {
	case installed:
		b.WriteString(statusRow("success", i18n.T("midictl.drv.status"), i18n.T("midictl.drv.installed")))
	case ctx.virtualAvail:
		b.WriteString(statusRow("warning", i18n.T("midictl.drv.status"), i18n.T("midictl.drv.fallback")))
	default:
		b.WriteString(statusRow("muted", i18n.T("midictl.drv.status"), i18n.T("midictl.drv.none")))
	}
	if !installed {
		b.WriteString(hint("info", i18n.T("midictl.drv.testsign")))
		b.WriteString(`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.drv.steps")) + `</p>`)
		b.WriteString(`<pre class=midi-cmds>` + htmlEscape(driverInstallCmds) + `</pre>`)
		b.WriteString(hint("warn", i18n.T("midictl.drv.smartscreen")))
	} else {
		b.WriteString(u.midiDriverManagedHTML(ctx))
	}
	b.WriteString(btnRow(btn(i18n.T("midictl.drv.docs"), "outline", "open-url",
		"https://github.com/rave-page/rave-mate/tree/development/driver/ravemidi")))
	badgeTx, badgeVar := i18n.T("midictl.drv.badgePreview"), "warning"
	if installed {
		badgeTx, badgeVar = i18n.T("midictl.drv.badgeOn"), "success"
	}
	return card(i18n.T("midictl.drv.card"), badge(badgeTx, badgeVar), b.String())
}

// midiDriverManagedHTML: managed-input live status + wire trace + re-apply/reload.
// Forwarding lives IN the driver (persisted kernel-side) - it survives rave-mate exit
// and reboots. Config sync is AUTOMATIC (driver-managed THRU on a controller +
// every MIDI config change re-syncs); the buttons are manual fallbacks.
func (u *UI) midiDriverManagedHTML(ctx midiCtlRenderCtx) string {
	var b strings.Builder
	b.WriteString(`<div class=pb-label>` + htmlEscape(i18n.T("midictl.drv.managedHdr")) + `</div>`)
	b.WriteString(`<p class=page-sub>` + htmlEscape(i18n.T("midictl.drv.managedSub")) + `</p>`)
	if e := midi.DriverSyncErr(); e != "" { // covers boot sync AND webui apply sync
		b.WriteString(hint("warn", i18n.T("midictl.drv.syncFailed", i18n.A{"err": e})))
	}
	// Driver inputs come from the cached probe (midiCtlProbe), never a fresh ioctl on the render
	// path. Reached only when ctx.drvInstalled, so the probe has landed + drvInputs/drvQueryErr valid.
	switch {
	case ctx.drvQueryErr:
		// older driver build without the config plane - honest degradation
		b.WriteString(hint("warn", i18n.T("midictl.drv.queryFailed")))
	case len(ctx.drvInputs) == 0:
		b.WriteString(`<p class=page-sub>` + htmlEscape(i18n.T("midictl.drv.noneManaged")) + `</p>`)
	default:
		for _, st := range ctx.drvInputs {
			variant, line := "warning", i18n.T("midictl.drv.retrying", i18n.A{"n": strconv.Itoa(int(st.RetryCount))})
			if st.Bound {
				variant, line = "success", i18n.T("midictl.drv.bound")
				if st.FeedbackBound {
					line += " · " + i18n.T("midictl.drv.feedback")
				}
			}
			b.WriteString(statusRow(variant, st.Name, line))
			if st.Bound && !st.FeedbackBound {
				// name WHY the LED test is absent: device render pin not bound
				b.WriteString(hint("info", i18n.T("midictl.drv.fbNotBound")))
			}
			if st.ReservedPortID != 0 {
				id := strconv.Itoa(int(st.ReservedPortID))
				btns := []string{btn(i18n.T("midictl.trace.open"), "ghost", "midi-drv-trace:"+id, "")}
				if st.Bound && st.FeedbackBound {
					btns = append(btns, btn(i18n.T("midictl.drv.fbTest"), "ghost", "midi-fbtest:"+id, "")+
						tipTopic("led-feedback"))
				}
				b.WriteString(btnRow(btns...))
				if r := u.fbtResultFor(st.ReservedPortID); r.line != "" {
					b.WriteString(statusRow(r.variant, i18n.T("midictl.drv.fbLabel"), r.line))
				}
			}
		}
	}
	b.WriteString(u.midiDrvTraceHTML())
	b.WriteString(btnRow(
		btn(i18n.T("midictl.drv.reapply"), "outline", "midi-drv-sync", ""),
		btn(i18n.T("midictl.drv.reload"), "ghost", "midi-drv-reload", "")))
	return b.String()
}

// driverInstallCmds: dev/test-signed install, mirrors driver/ravemidi/README.md.
const driverInstallCmds = `# 1  elevated PowerShell, inside the driver package folder
certutil -addstore Root ravemidi-test.cer
certutil -addstore TrustedPublisher ravemidi-test.cer
bcdedit /set testsigning on
#    reboot Windows now
# 2  after the reboot (elevated again)
pnputil /add-driver ravemidi.inf /install
devcon install ravemidi.inf Root\ravemidi`

// midiActiveRow renders the resolved active-port status line (patched ~1 Hz via #midi-active).
func midiActiveRow(active string) string {
	variant := "ok"
	if active == "" {
		variant, active = "off", i18n.T("midictl.portNotOpen")
	}
	return statusRow(variant, i18n.T("midictl.activePort"), active)
}

// midiRackCard renders the channel-count stepper + the horizontally-scrollable channel rack.
func (u *UI) midiRackCard() string {
	n := u.midiChannels()
	trail := u.midiChannelStepper(n)
	var rack strings.Builder
	rack.WriteString(`<div class="midi-mixer" data-testid=midi-mixer>`)
	for ch := 1; ch <= n; ch++ {
		rack.WriteString(midiStrip(ch))
	}
	rack.WriteString(`</div>`)
	return card(i18n.T("midictl.rackCard"), trail,
		`<p class=page-sub>`+htmlEscape(i18n.T("midictl.rack.sub"))+`</p>`+rack.String())
}

// midiChannelStepper renders the -/+ channel-count control (1..MaxChannels).
func (u *UI) midiChannelStepper(n int) string {
	dec, inc := n-1, n+1
	minus := btn("-", "outline", "midi-channels", strconv.Itoa(dec))
	plus := btn("+", "outline", "midi-channels", strconv.Itoa(inc))
	if n <= 1 {
		minus = `<button class="rp-btn rp-btn--outline" disabled>-</button>`
	}
	if n >= midimap.MaxChannels {
		plus = `<button class="rp-btn rp-btn--outline" disabled>+</button>`
	}
	count := `<span class=midi-chcount data-testid=midi-channels data-label=` + attrQ(i18n.T("midictl.channels.label")) +
		` data-value=` + attrQ(strconv.Itoa(n)) + `>` + strconv.Itoa(n) + `</span>`
	return `<span class=midi-stepper><span class=midi-steplbl>` + htmlEscape(i18n.T("midictl.channels.label")) +
		`</span>` + minus + count + plus + `</span>`
}

// midiStrip renders one channel strip (1-based ch): label, EQ/filter/trim knobs, a vertical fader,
// then Play + Cue. Every control's assigned CC is shown and stamped for ctl.
func midiStrip(ch int) string {
	wire := int(midimap.WireChannel(ch))
	letter := midimap.Letters[wire]
	var b strings.Builder
	b.WriteString(`<div class=midi-strip>`)
	b.WriteString(`<div class=midi-striphead>` + htmlEscape(i18n.T("midictl.channelLabel", i18n.A{"n": strconv.Itoa(ch), "letter": letter})) + `</div>`)
	b.WriteString(`<div class=midi-knobs>`)
	for _, c := range midimap.Controls {
		switch {
		case c.Kind == midimap.Momentary:
			continue // Play/Cue rendered below the fader
		case c.ID == "fader":
			continue // rendered after the knobs
		default:
			b.WriteString(midiKnob(ch, wire, c))
		}
	}
	b.WriteString(`</div>`)
	// Fader.
	for _, c := range midimap.Controls {
		if c.ID == "fader" {
			b.WriteString(midiFader(ch, wire, c))
		}
	}
	// Play + Cue.
	b.WriteString(`<div class=midi-btns>`)
	for _, c := range midimap.Controls {
		if c.Kind == midimap.Momentary {
			b.WriteString(midiMomBtn(ch, wire, c))
		}
	}
	b.WriteString(`</div>`)
	b.WriteString(`</div>`)
	return b.String()
}

// knobInitial is the starting value for a continuous control (EQ/filter/trim centred; fader down).
func knobInitial(id string) int {
	if id == "fader" {
		return 0
	}
	return 64
}

// midiKnob renders a rotary knob: an SVG-free circular dial whose pointer rotates with the value,
// an overlaid vertical range input (drag = live CC send), a caption, the CC readout, and a Sweep.
func midiKnob(ch, wire int, c midimap.Control) string {
	val := knobInitial(c.ID)
	label := i18n.T("midictl.ctl." + c.LabelKey)
	cc := ctlReadout(c, ch)
	tid := fmt.Sprintf("midi-ch%d-%s", ch, c.ID)
	dl := strings.ToLower(fmt.Sprintf("ch%d %s", ch, label))
	rot := float64(val)/127*270 - 135
	oninput := `oninput="var l=this.closest('.midi-knob');var v=this.value/127;l.style.setProperty('--v',v);l.style.setProperty('--rot',(v*270-135)+'deg')"`
	return fmt.Sprintf(`<label class=midi-knob data-label=%s style="--v:%s;--rot:%sdeg">`+
		`<span class=mk-dial aria-hidden=true><span class=mk-ptr></span></span>`+
		`<input class=mk-in type=range min=0 max=127 step=1 value=%d data-value=%d `+
		`data-actinput=%s data-testid=%s aria-label=%s %s>`+
		`<span class=mk-cap>%s</span><span class=mk-cc>%s</span>`+
		`<button class="mk-sweep rp-btn rp-btn--ghost" data-act=%s title=%s aria-label=%s>%s</button>`+
		`</label>`,
		attrQ(dl), trimNum(float64(val)/127), trimNum(rot),
		val, val,
		attrQ(fmt.Sprintf("midi-send:%d:%d", wire, c.CC)), attrQ(tid), attrQ(label+" "+cc), oninput,
		htmlEscape(label), htmlEscape(cc),
		attrQ(fmt.Sprintf("midi-sweep:%d:%d", wire, c.CC)), attrQ(i18n.T("midictl.sweep")),
		attrQ(i18n.T("midictl.sweep")+" "+label), htmlEscape(i18n.T("midictl.sweepGlyph")))
}

// midiFader renders a vertical fader: a track + level fill (mint, driven by --v) with an overlaid
// vertical range input (drag = live CC send), a caption, the CC readout, and a Sweep.
func midiFader(ch, wire int, c midimap.Control) string {
	val := knobInitial(c.ID)
	label := i18n.T("midictl.ctl." + c.LabelKey)
	cc := ctlReadout(c, ch)
	tid := fmt.Sprintf("midi-ch%d-%s", ch, c.ID)
	dl := strings.ToLower(fmt.Sprintf("ch%d %s", ch, label))
	oninput := `oninput="this.closest('.midi-vfader').style.setProperty('--v',this.value/127)"`
	return fmt.Sprintf(`<label class=midi-vfader data-label=%s style="--v:%s">`+
		`<span class=mf-track aria-hidden=true><span class=mf-fill></span></span>`+
		`<input class=mf-in type=range min=0 max=127 step=1 value=%d data-value=%d `+
		`data-actinput=%s data-testid=%s aria-label=%s %s>`+
		`<span class=mf-cap>%s</span><span class=mf-cc>%s</span>`+
		`<button class="mf-sweep rp-btn rp-btn--ghost" data-act=%s title=%s aria-label=%s>%s</button>`+
		`</label>`,
		attrQ(dl), trimNum(float64(val)/127),
		val, val,
		attrQ(fmt.Sprintf("midi-send:%d:%d", wire, c.CC)), attrQ(tid), attrQ(label+" "+cc), oninput,
		htmlEscape(label), htmlEscape(cc),
		attrQ(fmt.Sprintf("midi-sweep:%d:%d", wire, c.CC)), attrQ(i18n.T("midictl.sweep")),
		attrQ(i18n.T("midictl.sweep")+" "+label), htmlEscape(i18n.T("midictl.sweepGlyph")))
}

// midiMomBtn renders a Play/Cue pill: a press sends a momentary Note On + Note Off (a DJ app learns
// a Note as a Button on the Note-On; the Note-Off is the release - one clean learn event).
func midiMomBtn(ch, wire int, c midimap.Control) string {
	label := i18n.T("midictl.ctl." + c.LabelKey)
	cc := ctlReadout(c, ch)
	tid := fmt.Sprintf("midi-ch%d-%s", ch, c.ID)
	dl := strings.ToLower(fmt.Sprintf("ch%d %s", ch, label))
	cls := "midi-btn midi-btn--" + c.ID
	return fmt.Sprintf(`<button class=%s data-act=%s data-testid=%s data-label=%s aria-label=%s>`+
		`<span class=midi-btn-lbl>%s</span><span class=midi-btn-cc>%s</span></button>`,
		attrQ(cls), attrQ(fmt.Sprintf("midi-note:%d:%d", wire, c.CC)), attrQ(tid), attrQ(dl),
		attrQ(label+" "+cc), htmlEscape(label), htmlEscape(cc))
}

// ctlReadout formats a control's assigned MIDI as "CC24·ch1" (continuous) or "Note20·ch1" (button).
func ctlReadout(c midimap.Control, ch int) string {
	kind := "CC"
	if c.Note {
		kind = "Note"
	}
	return kind + strconv.Itoa(int(c.CC)) + "·ch" + strconv.Itoa(ch)
}

// midiHelpCard explains the send-to-learn round-trip + honest per-software status.
func (u *UI) midiHelpCard() string {
	steps := `<ol class=midi-help><li>` + htmlEscape(i18n.T("midictl.help.step1")) + `</li><li>` +
		htmlEscape(i18n.T("midictl.help.step2")) + `</li><li>` +
		htmlEscape(i18n.T("midictl.help.step3")) + `</li></ol>` +
		`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.help.feedback")) + `</p>` +
		`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.help.caveat")) + ` ` +
		`<a href="https://rekordbox.com/en/support/faq/mapping-6/" target=_blank rel=noopener>` +
		htmlEscape(i18n.T("midictl.help.link")) + `</a></p>`
	return card(i18n.T("midictl.helpCard"), badge(i18n.T("midictl.help.badge"), "info"),
		steps+midiSoftwareMatrix())
}

// midiSoftwareMatrix: honest per-DJ-software maturity + caveats (mirrors the README).
func midiSoftwareMatrix() string {
	row := func(name, badgeKey, badgeVar, noteKey string) string {
		return `<div class=midi-sw><span class=midi-sw-name>` + htmlEscape(name) + `</span>` +
			badge(i18n.T("midictl.sw."+badgeKey), badgeVar) +
			`<span class=midi-sw-note>` + htmlEscape(i18n.T("midictl.sw."+noteKey)) + `</span></div>`
	}
	return `<div class=pb-label>` + htmlEscape(i18n.T("midictl.sw.hdr")) + `</div>` +
		row("Traktor Pro", "stable", "success", "traktor") +
		row("Rekordbox", "experimental", "warning", "rekordbox") +
		row("VirtualDJ", "untested", "warning", "virtualdj") +
		row("Serato", "unfinished", "error", "serato")
}
