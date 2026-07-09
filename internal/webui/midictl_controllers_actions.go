package webui

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
)

// Native MIDI-learn action handlers: connect physical controllers, learn each control per
// channel (captured off the controller's own MIDI-in), manage THRU, and the two-port DJ bridge.
// The learn callback fires on a background goroutine, so all controller-config mutations go
// through midiCfgMu; u.saveCfg/patchMain/toast marshal to the UI thread via u.eval.

// midiCfgMu serialises MIDI.Controllers/Bridge mutations across dispatch handlers + the async
// learn callback.
var midiCfgMu sync.Mutex

// learnTimeout bounds one capture: enough to reach the control + wiggle it.
const learnTimeout = 15 * time.Second

func init() {
	onExact("midi-ctl-add", func(u *UI, _ actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		midiCfgMu.Lock()
		n := len(u.svc.Cfg.Features.MIDI.Controllers) + 1
		u.svc.Cfg.Features.MIDI.Controllers = append(u.svc.Cfg.Features.MIDI.Controllers,
			config.MIDIControllerMap{Name: i18n.T("midictl.in.ctlN", i18n.A{"n": strconv.Itoa(n)}), Enabled: true})
		midiCfgMu.Unlock()
		u.midiApply()
	})

	onPrefix("midi-ctl-remove:", func(u *UI, m actMsg) {
		i := atoiSafe(m.arg("midi-ctl-remove:"))
		if u.svc.Cfg == nil {
			return
		}
		midiCfgMu.Lock()
		cs := u.svc.Cfg.Features.MIDI.Controllers
		if i >= 0 && i < len(cs) {
			u.svc.Cfg.Features.MIDI.Controllers = append(cs[:i], cs[i+1:]...)
		}
		midiCfgMu.Unlock()
		u.midiApply()
	})

	onPrefix("midi-ctl-enable:", func(u *UI, m actMsg) {
		if u.withCtl(atoiSafe(m.arg("midi-ctl-enable:")), func(c *config.MIDIControllerMap) { c.Enabled = m.Val == "true" }) {
			u.midiApply()
		}
	})

	onPrefix("midi-ctl-port:", func(u *UI, m actMsg) {
		if u.withCtl(atoiSafe(m.arg("midi-ctl-port:")), func(c *config.MIDIControllerMap) {
			c.Port = m.Val
			if c.Name == "" && m.Val != "" {
				c.Name = m.Val // adopt the port as the controller's name
			}
		}) {
			u.midiApply()
		}
	})

	onPrefix("midi-ctl-thru:", func(u *UI, m actMsg) {
		if u.withCtl(atoiSafe(m.arg("midi-ctl-thru:")), func(c *config.MIDIControllerMap) { c.ThruPort = m.Val }) {
			u.midiApply()
		}
	})

	// learn: arm a one-shot capture on this controller's port; the first control moved binds.
	onPrefix("midi-learn:", func(u *UI, m actMsg) {
		ctlIdx, control, ch, ok := parseLearnArg(m.arg("midi-learn:"))
		if !ok || u.svc.MIDISource == nil || u.svc.Cfg == nil {
			return
		}
		midiCfgMu.Lock()
		cs := u.svc.Cfg.Features.MIDI.Controllers
		port := ""
		if ctlIdx >= 0 && ctlIdx < len(cs) {
			port = cs[ctlIdx].Port
		}
		midiCfgMu.Unlock()
		if port == "" {
			u.toast(i18n.T("midictl.in.needPort"))
			return
		}
		u.toast(i18n.T("midictl.in.listening", i18n.A{"control": i18n.T("midictl.ctl." + control), "ch": strconv.Itoa(ch)}))
		u.svc.MIDISource.ArmLearn(port, learnTimeout, func(_ string, status, data1 byte, okc bool) {
			if !okc {
				u.toast(i18n.T("midictl.in.learnTimeout"))
				return
			}
			u.withCtl(ctlIdx, func(c *config.MIDIControllerMap) { setBinding(c, control, ch, status, data1) })
			u.midiApply()
			u.toast(i18n.T("midictl.in.learned", i18n.A{"midi": bindingReadout(config.MIDIBinding{Status: status, Data1: data1})}))
		})
	})

	// unlearn: drop the binding for (control, channel) on a controller.
	onPrefix("midi-unlearn:", func(u *UI, m actMsg) {
		ctlIdx, control, ch, ok := parseLearnArg(m.arg("midi-unlearn:"))
		if !ok {
			return
		}
		if u.withCtl(ctlIdx, func(c *config.MIDIControllerMap) { clearBinding(c, control, ch) }) {
			u.midiApply()
		}
	})

	// two-port DJ bridge.
	onExact("midi-bridge-enable", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		midiCfgMu.Lock()
		u.svc.Cfg.Features.MIDI.Bridge.Enabled = m.Val == "true"
		midiCfgMu.Unlock()
		u.midiApply()
	})
	onExact("midi-bridge-todj", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		midiCfgMu.Lock()
		u.svc.Cfg.Features.MIDI.Bridge.ToDJPort = m.Val
		midiCfgMu.Unlock()
		u.midiApply()
	})
	onExact("midi-bridge-fromdj", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		midiCfgMu.Lock()
		u.svc.Cfg.Features.MIDI.Bridge.FromDJPort = m.Val
		midiCfgMu.Unlock()
		u.midiApply()
	})
}

// midiApply persists config, pushes it to the running MIDI child (live reconfigure), re-renders.
func (u *UI) midiApply() {
	u.saveCfg()
	if u.svc.MIDISource != nil {
		u.svc.MIDISource.Reconfigure()
	}
	u.patchMain()
}

// withCtl mutates controller i under midiCfgMu; returns false if i is out of range.
func (u *UI) withCtl(i int, fn func(c *config.MIDIControllerMap)) bool {
	if u.svc.Cfg == nil {
		return false
	}
	midiCfgMu.Lock()
	defer midiCfgMu.Unlock()
	cs := u.svc.Cfg.Features.MIDI.Controllers
	if i < 0 || i >= len(cs) {
		return false
	}
	fn(&u.svc.Cfg.Features.MIDI.Controllers[i])
	return true
}

// setBinding replaces or appends the (control, channel) binding on c.
func setBinding(c *config.MIDIControllerMap, control string, ch int, status, data1 byte) {
	nb := config.MIDIBinding{Control: control, Channel: ch, Status: status, Data1: data1}
	for j := range c.Bindings {
		if c.Bindings[j].Control == control && c.Bindings[j].Channel == ch {
			c.Bindings[j] = nb
			return
		}
	}
	c.Bindings = append(c.Bindings, nb)
}

// clearBinding drops the (control, channel) binding from c.
func clearBinding(c *config.MIDIControllerMap, control string, ch int) {
	out := c.Bindings[:0]
	for _, b := range c.Bindings {
		if b.Control == control && b.Channel == ch {
			continue
		}
		out = append(out, b)
	}
	c.Bindings = out
}

// parseLearnArg parses "<ctlIdx>:<control>:<channel>".
func parseLearnArg(arg string) (idx int, control string, ch int, ok bool) {
	parts := strings.Split(arg, ":")
	if len(parts) != 3 {
		return 0, "", 0, false
	}
	idx = atoiSafe(parts[0])
	control = parts[1]
	ch = atoiSafe(parts[2])
	if control == "" || ch < 1 {
		return 0, "", 0, false
	}
	return idx, control, ch, true
}
