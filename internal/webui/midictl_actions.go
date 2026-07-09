package webui

import (
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midimap"
)

// MIDI mixer action handlers: the virtual-mixer controls SEND MIDI to the loopback port so a DJ
// app can MIDI-learn them. Nothing sends unless the user interacts. Registered in init() so
// parallel tab work never collides on a central switch (dispatch.go convention).
//
// Action encoding (arg = "<wireCh>:<num>"):
//   midi-send:<wireCh>:<cc>    live continuous CC (val = 0..127) - knob/fader drag
//   midi-sweep:<wireCh>:<cc>   0->127->0 ramp so learn catches the control
//   midi-note:<wireCh>:<note>  momentary Note On (127) then Note Off - Play/Cue (buttons)

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

	// channel-count stepper: clamp 1..8, persist, re-render the rack.
	onExact("midi-channels", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.MIDIController.Channels = midimap.ClampChannels(atoiSafe(strings.TrimSpace(m.Val)))
		u.saveCfg()
		u.patchMain()
	})

	// continuous control drag: live CC (val = 0..127).
	onPrefix("midi-send:", func(u *UI, m actMsg) {
		e := u.svc.MIDIEmit
		if e == nil {
			return
		}
		ch, cc, ok := parseChCC(m.arg("midi-send:"))
		val, okv := midiByte(m.Val)
		if !ok || !okv {
			return
		}
		if err := e.SendCC(ch, cc, val); err != nil {
			u.toast(i18n.T("midictl.noPort"))
		}
	})

	// sweep affordance: 0->127->0 ramp so a DJ app arming learn catches the control.
	onPrefix("midi-sweep:", func(u *UI, m actMsg) {
		e := u.svc.MIDIEmit
		if e == nil {
			return
		}
		ch, cc, ok := parseChCC(m.arg("midi-sweep:"))
		if !ok {
			return
		}
		if err := e.SweepCC(ch, cc); err != nil {
			u.toast(i18n.T("midictl.noPort"))
		}
	})

	// momentary Play/Cue: Note On (127) then Note Off after a bounded delay. A DJ app learns a Note
	// as a Button on the Note-On; the Note-Off (a distinct status byte) is the release, not a second
	// learn event - so the learn dialog fires once, not twice like a CC 127/0 value-toggle.
	onPrefix("midi-note:", func(u *UI, m actMsg) {
		e := u.svc.MIDIEmit
		if e == nil {
			return
		}
		ch, note, ok := parseChCC(m.arg("midi-note:"))
		if !ok {
			return
		}
		if err := e.TriggerPad(ch, note, 127); err != nil {
			u.toast(i18n.T("midictl.noPort"))
		}
	})

	// Panic / All Notes Off across every configured channel.
	onExact("midi-panic", func(u *UI, _ actMsg) {
		e := u.svc.MIDIEmit
		if e == nil {
			return
		}
		for ch := 0; ch < u.midiChannels(); ch++ {
			e.Panic(byte(ch), 0, 0)
		}
		u.toast(i18n.T("midictl.panicked"))
	})

	// ~1 Hz refresh of the active-port line (resolves after the first send opens the port) + each
	// controller's port status (flips to "reading" when auto-retry recovers a released port).
	onLiveTick("midictl", func(u *UI) {
		if u.svc.MIDIEmit == nil {
			return
		}
		var js strings.Builder
		u.tickPatch(&js, "midi-active", midiActiveRow(u.svc.MIDIEmit.ActivePort()))
		if u.svc.Cfg != nil && u.svc.MIDISource != nil {
			midiCfgMu.Lock()
			cs := append([]config.MIDIControllerMap(nil), u.svc.Cfg.Features.MIDI.Controllers...)
			midiCfgMu.Unlock()
			for i, c := range cs {
				u.tickPatch(&js, "midi-ctlstat-"+strconv.Itoa(i), u.midiCtlPortStatusInner(c))
			}
		}
		u.flushTick(&js)
	})
}

// midiChannels returns the configured mixer channel/deck count clamped to 1..MaxChannels.
func (u *UI) midiChannels() int {
	n := midimap.DefaultChannels
	if u.svc.Cfg != nil {
		n = u.svc.Cfg.Features.MIDIController.Channels
	}
	return midimap.ClampChannels(n)
}

// midiByte parses a 0-127 MIDI data byte from s. ok=false on parse error / out of range.
func midiByte(s string) (byte, bool) {
	n := atoiSafe(strings.TrimSpace(s))
	if n < 0 || n > 127 {
		return 0, false
	}
	return byte(n), true
}

// parseChCC parses a "<wireCh>:<cc>" arg into a MIDI wire channel (0-15) + controller (0-127).
func parseChCC(arg string) (ch, cc byte, ok bool) {
	chs, ccs, found := strings.Cut(arg, ":")
	if !found {
		return 0, 0, false
	}
	c := atoiSafe(strings.TrimSpace(chs))
	if c < 0 || c > 15 {
		return 0, 0, false
	}
	cv, okv := midiByte(ccs)
	if !okv {
		return 0, 0, false
	}
	return byte(c), cv, true
}
