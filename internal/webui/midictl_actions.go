package webui

import (
	"strings"

	"rave.page/mate/internal/i18n"
)

// MIDI Controller (test) action handlers: pad/CC/knob controls → the emitter. Registered in init()
// so parallel tab work never collides on a central switch (dispatch.go convention).

func init() {
	// port selector: reopen on the picked port (or "" = auto) + persist to config.
	onExact("midi-port", func(u *UI, m actMsg) {
		if u.svc.MIDIEmit == nil {
			return
		}
		if u.svc.Cfg != nil {
			u.svc.Cfg.Features.MIDIController.Device = m.Val
			u.saveCfg()
		}
		u.svc.MIDIEmit.SetPort(m.Val)
		u.patchMain()
	})

	// pad: momentary Note On (velocity 127) + auto Note Off (val = note number).
	onExact("midi-pad", func(u *UI, m actMsg) {
		e := u.svc.MIDIEmit
		if e == nil {
			return
		}
		if note, ok := midiByte(m.Val); ok {
			if err := e.TriggerPad(midiChannel, note, 127); err != nil {
				u.toast(i18n.T("midictl.noPort"))
			}
		}
	})

	// fader/knob: Control Change (val = 0-127); act = "midi-cc:<cc>".
	onPrefix("midi-cc:", func(u *UI, m actMsg) {
		e := u.svc.MIDIEmit
		if e == nil {
			return
		}
		cc, okc := midiByte(m.arg("midi-cc:"))
		val, okv := midiByte(m.Val)
		if !okc || !okv {
			return
		}
		if err := e.SendCC(midiChannel, cc, val); err != nil {
			u.toast(i18n.T("midictl.noPort"))
		}
	})

	// Panic / All Notes Off: CC 123 + Note Off across the pad range.
	onExact("midi-panic", func(u *UI, _ actMsg) {
		if e := u.svc.MIDIEmit; e != nil {
			e.Panic(midiChannel, midiPadLo, midiPadLo+midiPadCount-1)
			u.toast(i18n.T("midictl.panicked"))
		}
	})

	// ~1 Hz refresh of the active-port line (resolves after the first send opens the port).
	onLiveTick("midictl", func(u *UI) {
		if u.svc.MIDIEmit == nil {
			return
		}
		var js strings.Builder
		u.tickPatch(&js, "midi-active", midiActiveRow(u.svc.MIDIEmit.ActivePort()))
		u.flushTick(&js)
	})
}

// midiByte parses a 0-127 MIDI data byte from s. ok=false on parse error / out of range.
func midiByte(s string) (byte, bool) {
	n := atoiSafe(strings.TrimSpace(s))
	if n < 0 || n > 127 {
		return 0, false
	}
	return byte(n), true
}
