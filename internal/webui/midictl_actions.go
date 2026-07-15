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

// MIDI mixer action handlers: the virtual-mixer controls SEND MIDI to the loopback port so a DJ
// app can MIDI-learn them. Nothing sends unless the user interacts. Registered in init() so
// parallel tab work never collides on a central switch (dispatch.go convention).
//
// Action encoding (arg = "<wireCh>:<num>"):
//   midi-send:<wireCh>:<cc>    live continuous CC (val = 0..127) - knob/fader drag
//   midi-sweep:<wireCh>:<cc>   0->127->0 ramp so learn catches the control
//   midi-note:<wireCh>:<note>  momentary Note On (127) then Note Off - Play/Cue (buttons)

func init() {
	// driver-managed forwarding (ravemidi config plane). Sync is AUTOMATIC on every
	// MIDI config change (midiApply); the button is a manual re-apply fallback.
	onExact("midi-drv-sync", func(u *UI, _ actMsg) { u.midiDrvSync(true) })
	onExact("midi-drv-reload", func(u *UI, _ actMsg) {
		if err := midi.ReloadDriverConfig(); err != nil {
			u.toast(err.Error())
			return
		}
		u.toast(i18n.T("midictl.drv.reloadedToast"))
		u.patchMain()
	})
	// per-port wire-trace viewer (driver diagnosis)
	onPrefix("midi-drv-trace:", func(u *UI, m actMsg) {
		id := uint32(atoiSafe(m.arg("midi-drv-trace:")))
		if u.midiTrace == id {
			id = 0 // toggle off
		}
		u.midiTrace = id
		u.patchMain()
	})
	onExact("midi-drv-trace-refresh", func(u *UI, _ actMsg) { u.patchMain() })
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

	// ~1 Hz refresh of the active-port line (resolves after the first send opens the port), each
	// controller's port status + activity, and the live input monitor.
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
			ctx := u.midiCtlCtx()
			for i, c := range cs {
				u.tickPatch(&js, "midi-ctlstat-"+strconv.Itoa(i), u.midiCtlPortStatusInner(c, ctx))
			}
		}
		if u.svc.MIDIMon != nil {
			u.tickPatch(&js, "midi-monitor", u.midiMonitorInner())
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

// midiDrvSync mirrors the driver-managed controllers (THRU = ravemidi) into the
// driver's persisted config. Runs automatically on every MIDI config change - the
// driver diffs by Id, so unchanged inputs keep their live taps (zero interruption).
// manual=true adds toasts + re-render (the "Re-apply" button path).
func (u *UI) midiDrvSync(manual bool) {
	if !midi.DriverInstalled() {
		if manual {
			u.toast(i18n.T("midictl.drv.none"))
		}
		return
	}
	midiCfgMu.Lock()
	var ins []midi.ManagedInput
	for _, c := range u.svc.Cfg.Features.MIDI.Controllers {
		if !c.Enabled || c.Port == "" || c.ThruPort != midi.DriverSentinel {
			continue
		}
		ins = append(ins, midi.ManagedInput{Name: c.Name, SourceMatch: c.Port, Filter: c.DriverFilter, Distinct: c.ThruDistinctName})
	}
	midiCfgMu.Unlock()
	// empty set is a valid sync: clears managed forwarding when the last
	// driver-managed controller is removed/switched away
	// outcome lands in midi.DriverSyncErr() - rendered as a persistent driver-card hint
	if err := midi.SetDriverConfig(midi.ManagedCfgs(ins)); err != nil {
		if manual {
			u.toast(err.Error())
			u.patchMain() // render the persistent sync-failed hint
		} else if u.log != nil {
			// auto path stays quiet: an older installed driver rejects the v2 blob
			// until it's updated - a toast on every config change would be noise
			u.log.Warn("midi", "ravemidi config sync failed (driver update needed?)",
				map[string]any{"err": err.Error()})
		}
		return
	}
	if manual {
		u.toast(i18n.T("midictl.drv.syncedToast", i18n.A{"n": fmt.Sprint(len(ins))}))
		u.patchMain()
	}
}
