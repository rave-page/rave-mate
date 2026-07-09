package webui

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/i18n"
)

// MIDI Controller (test) panel: a software pad/CC/knob surface that emits MIDI to a loopback port
// (LoopBe1) so a DJ app (Serato DJ Pro etc.) can MIDI-learn custom mappings. Controls dispatch to
// the emitter via the midi-* action handlers (midictl_actions.go). ctl-drivable: pads carry
// data-testid=midi-pad-<note>, faders/knobs data-testid=midi-cc-<cc>.

const (
	midiChannel  = 0  // MIDI channel 1 (0-based on the wire)
	midiPadLo    = 36 // first pad note (16 pads → 36..51)
	midiPadCount = 16
	midiFaderLo  = 20 // first fader CC (8 faders → CC 20..27)
	midiFaderN   = 8
	midiKnobLo   = 28 // first knob CC (8 knobs → CC 28..35; non-overlapping with faders)
	midiKnobN    = 8
)

func (u *UI) renderMIDICtl() string {
	if u.svc.MIDIEmit == nil {
		return u.renderPlaceholder("midictl")
	}
	var b strings.Builder
	b.WriteString(panel(i18n.T("tab.midictl"), i18n.T("midictl.subtitle")))
	b.WriteString(u.midiPortCard())
	b.WriteString(u.midiPadsCard())
	b.WriteString(u.midiFadersCard())
	b.WriteString(u.midiHelpCard())
	return b.String()
}

// midiPortCard renders the output-port selector, the live active-port line, and Panic.
func (u *UI) midiPortCard() string {
	e := u.svc.MIDIEmit
	opts := [][2]string{{"", i18n.T("midictl.autoPort")}}
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

// midiPadsCard renders the 16-pad grid (Note On + momentary Note Off, velocity 127).
func (u *UI) midiPadsCard() string {
	var g strings.Builder
	g.WriteString(`<div class=midi-pads>`)
	for i := 0; i < midiPadCount; i++ {
		note := midiPadLo + i
		g.WriteString(fmt.Sprintf(`<button class="rp-btn rp-btn--outline midi-pad" data-act=midi-pad data-val=%d data-testid=midi-pad-%d>%d</button>`, note, note, note))
	}
	g.WriteString(`</div>`)
	trail := badge(fmt.Sprintf("Notes %d-%d", midiPadLo, midiPadLo+midiPadCount-1), "secondary")
	return card(i18n.T("midictl.padsCard"), trail, g.String())
}

// midiFadersCard renders the fader + knob CC sliders (value 0-127, emit on release).
func (u *UI) midiFadersCard() string {
	faders := card(i18n.T("midictl.fadersCard"),
		badge(fmt.Sprintf("CC %d-%d", midiFaderLo, midiFaderLo+midiFaderN-1), "secondary"),
		midiCCGrid(midiFaderLo, midiFaderN))
	knobs := card(i18n.T("midictl.knobsCard"),
		badge(fmt.Sprintf("CC %d-%d", midiKnobLo, midiKnobLo+midiKnobN-1), "secondary"),
		midiCCGrid(midiKnobLo, midiKnobN))
	return faders + knobs
}

// midiCCGrid renders n labelled CC sliders starting at cc lo.
func midiCCGrid(lo, n int) string {
	var s strings.Builder
	s.WriteString(`<div class=midi-faders>`)
	for i := 0; i < n; i++ {
		cc := lo + i
		s.WriteString(midiCCSlider(cc))
	}
	s.WriteString(`</div>`)
	return s.String()
}

// midiCCSlider is a labelled 0-127 range whose release dispatches midi-cc:<cc> with the value.
// (Mirrors the slider() primitive but stamps a stable data-testid for ctl.)
func midiCCSlider(cc int) string {
	label := "CC " + strconv.Itoa(cc)
	act := "midi-cc:" + strconv.Itoa(cc)
	oninput := `oninput='var b=this.parentNode.querySelector(".slider-val");if(b)b.textContent=this.value'`
	return fmt.Sprintf(`<label class=slider data-label=%s><span class=field-label>%s <b class=slider-val>0</b></span>`+
		`<input class=slider-input type=range min=0 max=127 step=1 value=0 data-act=%s data-value=0 data-testid=midi-cc-%d %s></label>`,
		attrQ(strings.ToLower(label)), htmlEscape(label), attrQ(act), cc, oninput)
}

// midiHelpCard explains the Serato MIDI-learn workflow.
func (u *UI) midiHelpCard() string {
	steps := `<ol class=midi-help><li>` + i18n.T("midictl.help.step1") + `</li><li>` +
		i18n.T("midictl.help.step2") + `</li><li>` + i18n.T("midictl.help.step3") + `</li></ol>`
	return card(i18n.T("midictl.helpCard"), "", steps)
}
