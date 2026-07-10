package webui

import (
	"fmt"
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
	var b strings.Builder
	b.WriteString(panel(i18n.T("tab.midictl"), i18n.T("midictl.subtitle")))
	b.WriteString(u.midiControllersCard()) // native MIDI-learn: read physical controllers (input)
	b.WriteString(u.midiPortCard())
	b.WriteString(u.midiRackCard())
	b.WriteString(u.midiBridgeCard()) // two-port loopMIDI DJ router (peer control)
	b.WriteString(u.midiHelpCard())
	return b.String()
}

// midiPortCard renders the output-port selector, the live active-port line, and Panic.
func (u *UI) midiPortCard() string {
	e := u.svc.MIDIEmit
	opts := [][2]string{{"", i18n.T("midictl.autoPort")}}
	// Built-in one-way port first: kills the DJ app's LED-echo self-loop (ravemidi driver
	// preferred, teVirtualMIDI fallback - see midi.OpenOneWayOut).
	if midi.OneWayAvailable() {
		opts = append(opts, [2]string{midi.VirtualDJSentinel, i18n.T("midictl.in.thruVirtual")})
	}
	for _, p := range e.Ports() {
		opts = append(opts, [2]string{p, p})
	}
	body := selectBox(i18n.T("midictl.port"), "midi-port", opts, e.Want()) +
		`<div id=midi-active>` + midiActiveRow(e.ActivePort()) + `</div>` +
		btnRow(btn(i18n.T("midictl.panic"), "warn", "midi-panic", ""))
	return card(i18n.T("midictl.outputCard"), "", body)
}

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
	return card(i18n.T("midictl.rackCard"), trail, rack.String())
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

// midiHelpCard explains the send-to-learn round-trip + honest software caveats.
func (u *UI) midiHelpCard() string {
	steps := `<ol class=midi-help><li>` + htmlEscape(i18n.T("midictl.help.step1")) + `</li><li>` +
		htmlEscape(i18n.T("midictl.help.step2")) + `</li><li>` +
		htmlEscape(i18n.T("midictl.help.step3")) + `</li></ol>` +
		`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.help.feedback")) + `</p>` +
		`<p class=midi-help-note>` + htmlEscape(i18n.T("midictl.help.caveat")) + ` ` +
		`<a href="https://rekordbox.com/en/support/faq/mapping-6/" target=_blank rel=noopener>` +
		htmlEscape(i18n.T("midictl.help.link")) + `</a></p>`
	return card(i18n.T("midictl.helpCard"), badge(i18n.T("midictl.help.badge"), "info"), steps)
}
