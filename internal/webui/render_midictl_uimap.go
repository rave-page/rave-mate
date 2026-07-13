package webui

// MIDI tab ▸ "Control rave-mate" card: map controller notes/CCs/encoders to the desktop-UI
// actions (cue-editor transport, library browsing, nav history). Bindings live in the same
// list as the VR keybinds (cfg.Features.VROverlay.Binds - one bind store, two editors); this
// card filters to the ui.* groups and adds the per-bind mode/sensitivity/reverse editors the
// generic keybinds modal doesn't need. Learn goes through Services.MIDILearn, which the app
// layer arms EXCLUSIVELY: while armed, the captured press never also fires an existing bind.

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrbind"
)

// umSt is the per-UI learn state for the mappings card.
type umSt struct {
	mu     sync.Mutex
	learn  vrbind.ActionID // armed action ("" = idle)
	cancel func()          // disarms the pending MIDILearn capture
}

var (
	umMu  sync.Mutex
	umMap = map[*UI]*umSt{}
)

func (u *UI) um() *umSt {
	umMu.Lock()
	defer umMu.Unlock()
	s := umMap[u]
	if s == nil {
		s = &umSt{}
		umMap[u] = s
	}
	return s
}

// umGroups is the card's section order.
var umGroups = []string{vrbind.GroupCueEdit, vrbind.GroupLibrary, vrbind.GroupNav}

// umActLabel translates an action id (midictl.uimap.act.<ce_audition>), falling back to the
// vrbind catalog's English label for ids without a key yet.
func umActLabel(a vrbind.Action) string {
	key := "midictl.uimap.act." + strings.ReplaceAll(strings.TrimPrefix(string(a.ID), "ui."), ".", "_")
	if got := i18n.T(key); got != key {
		return got
	}
	return a.Label
}

func umKindLabel(k vrbind.ActionKind) string {
	switch k {
	case vrbind.KindHold:
		return i18n.T("midictl.uimap.kind.hold")
	case vrbind.KindStep:
		return i18n.T("midictl.uimap.kind.step")
	}
	return i18n.T("midictl.uimap.kind.trigger")
}

// umModeOpts returns the mode choices for a bind, by action kind + message family.
func umModeOpts(kind vrbind.ActionKind, isNote bool) []ssOpt {
	mode := func(val, key string) ssOpt {
		return ssOpt{Val: val, Label: i18n.T("midictl.uimap.mode." + key), Sub: i18n.T("midictl.uimap.modeSub." + key)}
	}
	if isNote {
		if kind == vrbind.KindHold {
			return []ssOpt{mode("", "momentary"), mode(vrbind.ModeToggle, "toggle")}
		}
		return nil // note on trigger/step: press-only, nothing to pick
	}
	rel := []ssOpt{mode(vrbind.ModeRel2C, "rel2c"), mode(vrbind.ModeRelSM, "relsm"), mode(vrbind.ModeRel64, "rel64")}
	switch kind {
	case vrbind.KindHold:
		return []ssOpt{mode("", "momentary"), mode(vrbind.ModeToggle, "toggle")}
	case vrbind.KindStep:
		return append([]ssOpt{mode(vrbind.ModeAbs, "abs"), mode("", "press")}, rel...)
	default:
		return append([]ssOpt{mode("", "press")}, rel...)
	}
}

// umModeLabel names a bind's current mode for the chip row.
func umModeLabel(kind vrbind.ActionKind, k vrbind.MIDIKey) string {
	key := "press"
	switch k.Mode {
	case vrbind.ModeToggle:
		key = "toggle"
	case vrbind.ModeAbs:
		key = "abs"
	case vrbind.ModeRel2C:
		key = "rel2c"
	case vrbind.ModeRelSM:
		key = "relsm"
	case vrbind.ModeRel64:
		key = "rel64"
	default:
		if kind == vrbind.KindHold {
			key = "momentary"
		}
	}
	return i18n.T("midictl.uimap.mode." + key)
}

// umBindLabel renders "CC 20 (ch1) · DDJ-400" (key + source device when port-matched).
func umBindLabel(k vrbind.MIDIKey) string {
	l := vrmMIDIKeyLabel(k)
	if k.Port != "" {
		l += " · " + k.Port
	}
	return l
}

// midiUIMapCard renders the mappings card on the MIDI tab.
func (u *UI) midiUIMapCard() string {
	if u.svc.Cfg == nil || u.svc.MIDILearn == nil {
		return ""
	}
	f := &u.svc.Cfg.Features.VROverlay
	st := u.um()
	st.mu.Lock()
	learning := st.learn
	st.mu.Unlock()

	var b strings.Builder
	b.WriteString(`<p class=page-sub>` + htmlEscape(i18n.T("midictl.uimap.sub")) + `</p>`)
	b.WriteString(toggleRowTip(i18n.T("midictl.uimap.enable"), "um-enable",
		!u.svc.Cfg.Features.MIDI.DisableUIBinds, tipTopic("midi-mapping")))

	for _, group := range umGroups {
		b.WriteString(`<div class=card-label>` + htmlEscape(i18n.T("midictl.uimap.group."+group)) + `</div>`)
		for _, a := range vrbind.Actions() {
			if a.ResolvedGroup() != group {
				continue
			}
			learnLbl := i18n.T("midictl.uimap.learn")
			learnVariant := "outline"
			if learning == a.ID {
				learnLbl = i18n.T("midictl.uimap.learnArmed")
				learnVariant = "warn"
			}
			b.WriteString(itemRow(umActLabel(a), umKindLabel(a.Kind),
				btn(learnLbl, learnVariant, "um-learn:"+string(a.ID), "")))
			for i := range f.Binds {
				bd := f.Binds[i]
				if bd.Action != a.ID || bd.MIDI == nil {
					continue
				}
				b.WriteString(u.umBindRow(i, a, *bd.MIDI))
			}
		}
	}
	b.WriteString(`<div class=set-note>` + htmlEscape(i18n.T("midictl.uimap.note")) + `</div>`)
	return card(i18n.T("midictl.uimap.title"), tipTopic("midi-mapping"), b.String())
}

// umBindRow renders one existing bind: key chip + mode/sensitivity/reverse editors + remove.
// idx is the bind's absolute index in VROverlay.Binds (the shared store).
func (u *UI) umBindRow(idx int, a vrbind.Action, k vrbind.MIDIKey) string {
	is := strconv.Itoa(idx)
	isNote := k.Status&0xF0 == 0x90 || k.Status&0xF0 == 0x80
	var trail []string
	if opts := umModeOpts(a.Kind, isNote); len(opts) > 0 {
		o := opts
		trail = append(trail, smartSelect("um-mode-"+is, "", "um-mode:"+is+":", umModeLabel(a.Kind, k),
			func() []ssOpt { return o }))
	}
	if a.Kind == vrbind.KindStep && !isNote && (k.Mode == vrbind.ModeAbs || k.Mode == vrbind.ModeRel2C ||
		k.Mode == vrbind.ModeRelSM || k.Mode == vrbind.ModeRel64) {
		step := k.Step
		if step <= 0 {
			step = 1
		}
		trail = append(trail, smartSelect("um-step-"+is, "", "um-step:"+is+":", i18n.T("midictl.uimap.step")+" "+strconv.Itoa(step),
			func() []ssOpt {
				var out []ssOpt
				for _, n := range []int{1, 2, 4, 8} {
					out = append(out, ssOpt{Val: strconv.Itoa(n), Label: strconv.Itoa(n),
						Sub: i18n.Tn("midictl.uimap.stepSub", n)})
				}
				return out
			}))
	}
	if a.Kind == vrbind.KindStep {
		revVariant := "ghost"
		if k.Rev {
			revVariant = "secondary"
		}
		trail = append(trail, btn(i18n.T("midictl.uimap.rev"), revVariant, "um-rev:"+is, ""))
	}
	trail = append(trail, btn("✕", "ghost", "um-del:"+is, ""))
	return itemRow("↳ "+umBindLabel(k), umModeLabel(a.Kind, k), trail...)
}

// ── actions ──

func init() {
	onExact("um-enable", func(u *UI, m actMsg) {
		if u.svc.Cfg == nil {
			return
		}
		u.svc.Cfg.Features.MIDI.DisableUIBinds = m.Val != "true"
		u.saveCfg()
	})
	onPrefix("um-learn:", func(u *UI, m actMsg) { u.umLearnToggle(vrbind.ActionID(m.arg("um-learn:"))) })
	onPrefix("um-capt:", func(u *UI, m actMsg) { u.umCapture(vrbind.ActionID(m.arg("um-capt:")), m.Val) })
	onPrefix("um-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(m.arg("um-del:")); i >= 0 && i < len(f.Binds) {
			f.Binds = append(f.Binds[:i], f.Binds[i+1:]...)
			u.saveCfg()
		}
		u.umPatch()
	})
	onPrefix("um-mode:", func(u *UI, m actMsg) { // "<idx>:<mode>"
		is, mode, _ := strings.Cut(m.arg("um-mode:"), ":")
		u.umEditBind(atoiSafe(is), func(k *vrbind.MIDIKey) { k.Mode = mode })
	})
	onPrefix("um-step:", func(u *UI, m actMsg) { // "<idx>:<n>"
		is, ns, _ := strings.Cut(m.arg("um-step:"), ":")
		n := atoiSafe(ns)
		if n < 1 {
			n = 1
		}
		u.umEditBind(atoiSafe(is), func(k *vrbind.MIDIKey) { k.Step = n })
	})
	onPrefix("um-rev:", func(u *UI, m actMsg) {
		u.umEditBind(atoiSafe(m.arg("um-rev:")), func(k *vrbind.MIDIKey) { k.Rev = !k.Rev })
	})
}

// umEditBind mutates one bind's MIDI key in place + persists + re-renders.
func (u *UI) umEditBind(idx int, edit func(*vrbind.MIDIKey)) {
	f := &u.svc.Cfg.Features.VROverlay
	if idx < 0 || idx >= len(f.Binds) || f.Binds[idx].MIDI == nil {
		return
	}
	edit(f.Binds[idx].MIDI)
	u.saveCfg()
	u.umPatch()
}

// umLearnToggle arms (or cancels) a capture for one action. The MIDILearn callback fires on
// the MIDI event goroutine - it only re-posts onto the act worker (um-capt) so the config
// mutation runs serialized with every other act.
func (u *UI) umLearnToggle(id vrbind.ActionID) {
	if _, ok := vrbind.ActionByID(id); !ok || u.svc.MIDILearn == nil {
		return
	}
	st := u.um()
	st.mu.Lock()
	if st.cancel != nil { // learning (this or another action): cancel first
		st.cancel()
		st.cancel = nil
	}
	if st.learn == id { // second click on the armed action = plain cancel
		st.learn = ""
		st.mu.Unlock()
		u.umPatch()
		return
	}
	st.learn = id
	st.mu.Unlock()
	cancel := u.svc.MIDILearn(func(port string, status, data1 byte) {
		u.postAct("um-capt:"+string(id), fmt.Sprintf("%d|%d|%s", status, data1, port))
	})
	st.mu.Lock()
	st.cancel = cancel
	st.mu.Unlock()
	u.umPatch()
}

// umCapture (act worker) appends the learned bind. val = "<status>|<data1>|<port>" (port
// last - device names may contain any rune but never produce ambiguity after two cuts).
func (u *UI) umCapture(id vrbind.ActionID, val string) {
	st := u.um()
	st.mu.Lock()
	armed := st.learn == id
	st.learn, st.cancel = "", nil
	st.mu.Unlock()
	a, ok := vrbind.ActionByID(id)
	if !ok || !armed || u.svc.Cfg == nil {
		return
	}
	ss, rest, ok1 := strings.Cut(val, "|")
	ds, port, ok2 := strings.Cut(rest, "|")
	if !ok1 || !ok2 {
		return
	}
	status, data1 := atoiSafe(ss), atoiSafe(ds)
	if status < 0x80 || status > 0xFF || data1 < 0 || data1 > 127 {
		return
	}
	k := &vrbind.MIDIKey{Status: byte(status), Data1: byte(data1), Port: port}
	// CC on a step action defaults to the absolute-knob decoding; endless encoders switch
	// to their relative encoding in the row's mode picker.
	if a.Kind == vrbind.KindStep && k.Status&0xF0 == 0xB0 {
		k.Mode = vrbind.ModeAbs
	}
	f := &u.svc.Cfg.Features.VROverlay
	f.Binds = append(f.Binds, vrbind.Bind{Action: id, MIDI: k})
	u.saveCfg()
	u.toast(i18n.T("midictl.uimap.captured", i18n.A{"key": umBindLabel(*k)}))
	u.umPatch()
}

// umPatch re-renders the MIDI tab if it is showing (the card lives there).
func (u *UI) umPatch() {
	if u.activeTab() == "midictl" {
		u.patchMain()
	}
}
