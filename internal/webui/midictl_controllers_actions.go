package webui

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/midi"
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

	// clone toggle (driver-managed): ON mirrors the controller's own name to the DJ fan-out
	// (Serato matches by name); OFF names it "<Name> THRU". Toggle sends the CLONE state, so
	// the stored distinct-name flag is its inverse.
	onPrefix("midi-ctl-clone:", func(u *UI, m actMsg) {
		if u.withCtl(atoiSafe(m.arg("midi-ctl-clone:")), func(c *config.MIDIControllerMap) { c.ThruDistinctName = m.Val != "true" }) {
			u.midiApply()
		}
	})

	// driver fan-out message-filter chips (driver-managed controllers only)
	onPrefix("midi-ctl-filter:", func(u *UI, m actMsg) {
		idxs, key, ok := strings.Cut(m.arg("midi-ctl-filter:"), ":")
		if !ok {
			return
		}
		if u.withCtl(atoiSafe(idxs), func(c *config.MIDIControllerMap) {
			fl := c.DriverFilter
			if fl == nil {
				fl = append([]string(nil), midi.DefaultDriverFilter()...)
			}
			out := fl[:0]
			found := false
			for _, k := range fl {
				if k == key {
					found = true
					continue
				}
				out = append(out, k)
			}
			if !found {
				out = append(out, key)
			}
			c.DriverFilter = out // non-nil from here: [] = filter nothing
		}) {
			u.midiApply()
		}
	})

	// learn: arm a one-shot capture on this controller's port; the first control moved binds.
	// Driver-managed controllers learn off the driver's hidden reserved endpoint (the child
	// reads it over IOCTL) - the raw hardware name would never match what the child opened.
	onPrefix("midi-learn:", func(u *UI, m actMsg) {
		ctlIdx, control, ch, ok := parseLearnArg(m.arg("midi-learn:"))
		if !ok || u.svc.MIDISource == nil || u.svc.Cfg == nil {
			return
		}
		midiCfgMu.Lock()
		cs := u.svc.Cfg.Features.MIDI.Controllers
		var ctl config.MIDIControllerMap
		if ctlIdx >= 0 && ctlIdx < len(cs) {
			ctl = cs[ctlIdx]
		}
		midiCfgMu.Unlock()
		if ctl.Port == "" {
			u.toast(i18n.T("midictl.in.needPort"))
			return
		}
		port := midiChildPort(ctl)
		notOpen := func() {
			if port != ctl.Port { // driver-managed: "close the other app" would be wrong advice
				u.toast(i18n.T("midictl.in.drvNotReady"))
			} else {
				u.toast(i18n.T("midictl.in.portInUse", i18n.A{"port": ctl.Port}))
			}
		}
		// Fast feedback: if the port isn't open (held by another app / gone), say so now instead
		// of arming a capture that would silently time out.
		if !portContains(u.svc.MIDISource.OpenInputPorts(), port) {
			notOpen()
			return
		}
		u.toast(i18n.T("midictl.in.listening", i18n.A{"control": i18n.T("midictl.ctl." + control), "ch": strconv.Itoa(ch)}))
		u.svc.MIDISource.ArmLearn(port, learnTimeout, func(_ string, status, data1 byte, okc bool, reason string) {
			if !okc {
				if reason == "port-not-open" {
					notOpen()
				} else {
					u.toast(i18n.T("midictl.in.learnTimeout"))
				}
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

// midiApply persists config, pushes it to the running MIDI child (live reconfigure),
// mirrors driver-managed controllers into the ravemidi driver, re-renders. One entry
// point = zero manual sync/reload friction.
func (u *UI) midiApply() {
	u.saveCfg()
	u.midiDrvSync(false) // BEFORE child reconfigure: reserved ports must exist when it opens them
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

// portContains reports whether any port name in list contains want (case-insensitive) - the
// same substring rule midi.Open uses, so "open" here means the same port the child opened.
func portContains(list []string, want string) bool {
	w := strings.ToLower(strings.TrimSpace(want))
	if w == "" {
		return false
	}
	for _, p := range list {
		if strings.Contains(strings.ToLower(p), w) {
			return true
		}
	}
	return false
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
