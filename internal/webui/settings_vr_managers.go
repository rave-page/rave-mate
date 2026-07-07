package webui

// Settings ▸ VR manager modals - webview port of the Fyne dialogs the rewrite missed
// (C6 phase 2): keybinds (view_keybinds.go), overlay layouts incl. import/export
// (view_settings_vr.go), wrist quick buttons + per-world layouts
// (view_settings_vr_wrist.go). Mutations go straight to Cfg + saveCfg, exactly like
// Fyne; dynamic pickers are smart-selects, free-text targets are fields.

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"strconv"
	"strings"
	"sync"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrbind"
)

// vrmSt is the per-UI add-form state for the keybind + quick-button modals.
type vrmSt struct {
	mu       sync.Mutex
	kbAction string // vrbind action id
	kbTarget string
	kbVR     string // VR slot ("" = none)
	kbMIDI   *vrbind.MIDIKey
	kbCancel func() // armed MIDI learn (nil = idle)

	qbAction string // action id or layout.load / campath.load
	qbTarget string
	qbLabel  string
	qbGlyph  string

	wlWorldID   string // bind-current-world capture
	wlWorldName string
	wlPick      string
}

var (
	vrmMu  sync.Mutex
	vrmMap = map[*UI]*vrmSt{}
)

func (u *UI) vrm() *vrmSt {
	vrmMu.Lock()
	defer vrmMu.Unlock()
	s := vrmMap[u]
	if s == nil {
		s = &vrmSt{}
		vrmMap[u] = s
	}
	return s
}

func init() {
	// entry points (Settings ▸ VR overlays)
	onExact("settings-vr-keybinds", func(u *UI, _ actMsg) { u.vrKeybindsModal() })
	onExact("settings-vr-layouts", func(u *UI, _ actMsg) { u.vrLayoutsModal() })
	onExact("settings-vr-wrist", func(u *UI, _ actMsg) { u.vrQuickButtonsModal() })
	onExact("settings-vr-worldlay", func(u *UI, _ actMsg) { u.vrWorldLayoutsModal() })

	// keybinds
	onPrefix("vr-kb-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(m.arg("vr-kb-del:")); i >= 0 && i < len(f.Binds) {
			f.Binds = append(f.Binds[:i], f.Binds[i+1:]...)
			u.saveCfg()
		}
		u.vrKeybindsModal()
	})
	onPrefix("vr-kb-actpick:", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.kbAction, s.kbTarget = m.arg("vr-kb-actpick:"), ""
		s.mu.Unlock()
		u.vrKeybindsModal()
	})
	onPrefix("vr-kb-tgtpick:", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.kbTarget = m.arg("vr-kb-tgtpick:")
		s.mu.Unlock()
		u.vrKeybindsModal()
	})
	onExact("vr-kb-tgtval", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.kbTarget = m.Val
		s.mu.Unlock()
	})
	onPrefix("vr-kb-vrpick:", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.kbVR = m.arg("vr-kb-vrpick:")
		s.mu.Unlock()
		u.vrKeybindsModal()
	})
	onExact("vr-kb-learn", func(u *UI, _ actMsg) { u.vrmLearnToggle() })
	onExact("vr-kb-add", func(u *UI, _ actMsg) { u.vrmAddBind() })

	// layouts
	onPrefix("vr-lay-load:", func(u *UI, m actMsg) { u.vrmLoadLayout(atoiSafe(m.arg("vr-lay-load:"))) })
	onPrefix("vr-lay-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(m.arg("vr-lay-del:")); i >= 0 && i < len(f.Layouts) {
			f.Layouts = append(f.Layouts[:i], f.Layouts[i+1:]...)
			u.saveCfg()
		}
		u.vrLayoutsModal()
	})
	onPrefix("vr-lay-ren:", func(u *UI, m actMsg) { u.vrmRenameModal(atoiSafe(m.arg("vr-lay-ren:"))) })
	onPrefix("vr-lay-rensave:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		i := atoiSafe(m.arg("vr-lay-rensave:"))
		if name := strings.TrimSpace(parseForm(m.Form)["name"]); name != "" && i >= 0 && i < len(f.Layouts) {
			f.Layouts[i].Name = name
			u.saveCfg()
		}
		u.vrLayoutsModal()
	})
	onExact("vr-lay-savecur-open", func(u *UI, _ actMsg) { u.vrmSaveCurModal() })
	onExact("vr-lay-savecur", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		name := strings.TrimSpace(parseForm(m.Form)["name"])
		if name == "" {
			name = i18n.T("settings.vr.defaultLayoutName", i18n.A{"n": strconv.Itoa(len(f.Layouts) + 1)})
		}
		f.Layouts = append(f.Layouts, u.vrmSnapshotLayout(name))
		u.saveCfg()
		u.vrLayoutsModal()
	})
	onPrefix("vr-lay-exp-", func(u *UI, m actMsg) { u.vrmExportLayout(atoiSafe(m.arg("vr-lay-exp-")), m.Val) })
	onExact("vr-lay-imp", func(u *UI, m actMsg) { u.vrmImportLayout(m.Val) })

	// quick buttons
	onPrefix("vr-qb-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(m.arg("vr-qb-del:")); i >= 0 && i < len(f.QuickButtons) {
			f.QuickButtons = append(f.QuickButtons[:i], f.QuickButtons[i+1:]...)
			u.saveCfg()
		}
		u.vrQuickButtonsModal()
	})
	onPrefix("vr-qb-actpick:", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.qbAction, s.qbTarget = m.arg("vr-qb-actpick:"), ""
		s.mu.Unlock()
		u.vrQuickButtonsModal()
	})
	onPrefix("vr-qb-tgtpick:", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.qbTarget = m.arg("vr-qb-tgtpick:")
		s.mu.Unlock()
		u.vrQuickButtonsModal()
	})
	onExact("vr-qb-tgtval", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.qbTarget = m.Val
		s.mu.Unlock()
	})
	onExact("vr-qb-label", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.qbLabel = m.Val
		s.mu.Unlock()
	})
	onExact("vr-qb-glyph", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.qbGlyph = m.Val
		s.mu.Unlock()
	})
	onExact("vr-qb-add", func(u *UI, _ actMsg) { u.vrmAddQuickButton() })

	// world layouts
	onPrefix("vr-wl-modepick:", func(u *UI, m actMsg) {
		u.svc.Cfg.Features.VROverlay.WorldLayoutMode = m.arg("vr-wl-modepick:")
		u.saveCfg()
		u.vrWorldLayoutsModal()
	})
	onPrefix("vr-wl-en:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(m.arg("vr-wl-en:")); i >= 0 && i < len(f.WorldLayouts) {
			f.WorldLayouts[i].Enabled = !f.WorldLayouts[i].Enabled
			u.saveCfg()
		}
		u.vrWorldLayoutsModal()
	})
	onPrefix("vr-wl-del:", func(u *UI, m actMsg) {
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(m.arg("vr-wl-del:")); i >= 0 && i < len(f.WorldLayouts) {
			f.WorldLayouts = append(f.WorldLayouts[:i], f.WorldLayouts[i+1:]...)
			u.saveCfg()
		}
		u.vrWorldLayoutsModal()
	})
	onPrefix("vr-wl-set:", func(u *UI, m actMsg) { // "<idx>:<layout name>"
		is, name, ok := strings.Cut(m.arg("vr-wl-set:"), ":")
		f := &u.svc.Cfg.Features.VROverlay
		if i := atoiSafe(is); ok && i >= 0 && i < len(f.WorldLayouts) {
			f.WorldLayouts[i].Layout = name
			u.saveCfg()
		}
		u.vrWorldLayoutsModal()
	})
	onExact("vr-wl-bind", func(u *UI, _ actMsg) { u.vrmBindWorldModal() })
	onPrefix("vr-wl-bindpick:", func(u *UI, m actMsg) {
		s := u.vrm()
		s.mu.Lock()
		s.wlPick = m.arg("vr-wl-bindpick:")
		s.mu.Unlock()
		u.vrmBindWorldModal()
	})
	onExact("vr-wl-bindgo", func(u *UI, _ actMsg) { u.vrmBindWorldGo() })
}

// ── keybinds ──

func (u *UI) vrKeybindsModal() {
	f := &u.svc.Cfg.Features.VROverlay
	s := u.vrm()
	s.mu.Lock()
	actID, tgt, vrSlot := s.kbAction, s.kbTarget, s.kbVR
	learning, midi := s.kbCancel != nil, s.kbMIDI
	s.mu.Unlock()

	var b strings.Builder
	if len(f.Binds) == 0 {
		b.WriteString(emptyState(i18n.T("settings.vr.emptyKeybinds")))
	}
	for i, bd := range f.Binds {
		b.WriteString(itemRow(vrmBindSummary(bd), "", btn(i18n.T("common.delete"), "ghost", "vr-kb-del:"+strconv.Itoa(i), "")))
	}

	b.WriteString(`<div class=card-label>` + i18n.T("settings.vr.addKeybind") + `</div>`)
	b.WriteString(smartSelect("vr-kb-action", i18n.T("settings.vr.action"), "vr-kb-actpick:", actID, vrmActionOpts))
	if a, ok := vrbind.ActionByID(vrbind.ActionID(actID)); ok {
		b.WriteString(u.vrmTargetRow("vr-kb-target", "vr-kb-tgtpick:", "vr-kb-tgtval", tgt, a.Target))
	}
	learnLbl := i18n.T("settings.vr.learnMidi")
	if learning {
		learnLbl = i18n.T("settings.vr.learnPressKey")
	}
	midiTxt := i18n.T("settings.vr.midiNone")
	if midi != nil {
		midiTxt = i18n.T("settings.vr.midiPrefix") + vrmMIDIKeyLabel(*midi)
	}
	b.WriteString(btnRow(btn(learnLbl, "outline", "vr-kb-learn", "")) +
		`<div class=set-note>` + html.EscapeString(midiTxt) + `</div>`)
	b.WriteString(smartSelect("vr-kb-vrslot", i18n.T("settings.vr.vrSlot"), "vr-kb-vrpick:", vrSlot, vrmVRSlotOpts))
	b.WriteString(btnRow(btn(i18n.T("settings.vr.addBind"), "primary", "vr-kb-add", "")))
	b.WriteString(`<div class=set-note>` + i18n.T("settings.vr.vrSlotNote") + `</div>`)
	b.WriteString(btnRow(btn(i18n.T("settings.vr.editBindsSteamvr"), "outline", "settings-vr-bindings", "")))
	u.openModal(modal(i18n.T("settings.vr.keybinds"), b.String(), ""))
}

// vrmLearnToggle arms/cancels a one-shot MIDI capture. The callback fires off-thread;
// it only applies while still armed, so a cancel (or Add) can't be raced into a stale
// capture. A scrim-close mid-learn leaves the listener armed until the next key - the
// capture then stores + re-renders the modal (matches Fyne's live-update behaviour).
func (u *UI) vrmLearnToggle() {
	s := u.vrm()
	s.mu.Lock()
	if s.kbCancel != nil {
		s.kbCancel()
		s.kbCancel = nil
		s.mu.Unlock()
		u.vrKeybindsModal()
		return
	}
	s.mu.Unlock()
	if u.svc.MIDILearn == nil {
		u.toast(i18n.T("settings.vr.midiUnavailable"))
		return
	}
	cancel := u.svc.MIDILearn(func(status, data1 byte) {
		s.mu.Lock()
		armed := s.kbCancel != nil
		if armed {
			s.kbMIDI = &vrbind.MIDIKey{Status: status, Data1: data1}
			s.kbCancel = nil
		}
		s.mu.Unlock()
		if armed {
			u.vrKeybindsModal()
		}
	})
	s.mu.Lock()
	s.kbCancel = cancel
	s.mu.Unlock()
	u.vrKeybindsModal()
}

func (u *UI) vrmAddBind() {
	f := &u.svc.Cfg.Features.VROverlay
	s := u.vrm()
	s.mu.Lock()
	actID, tgt, vrSlot, midi := s.kbAction, s.kbTarget, s.kbVR, s.kbMIDI
	s.mu.Unlock()
	if _, ok := vrbind.ActionByID(vrbind.ActionID(actID)); !ok {
		u.toast(i18n.T("settings.vr.pickAction"))
		return
	}
	if midi == nil && vrSlot == "" {
		u.toast(i18n.T("settings.vr.assignKeyOrSlot"))
		return
	}
	bd := vrbind.Bind{Action: vrbind.ActionID(actID), Target: tgt, VRAction: vrSlot}
	bd.MIDI = midi
	f.Binds = append(f.Binds, bd)
	u.saveCfg()
	s.mu.Lock()
	if s.kbCancel != nil {
		s.kbCancel()
	}
	s.kbMIDI, s.kbCancel, s.kbVR = nil, nil, ""
	s.mu.Unlock()
	u.vrKeybindsModal()
}

// vrmBindSummary renders a bind as one line (Fyne bindSummary port).
func vrmBindSummary(b vrbind.Bind) string {
	label := string(b.Action)
	if a, ok := vrbind.ActionByID(b.Action); ok {
		label = a.Label
	}
	if b.Target != "" {
		label += " [" + b.Target + "]"
	}
	var src string
	if b.VRAction != "" {
		src = "VR:" + b.VRAction
	}
	if b.MIDI != nil {
		if src != "" {
			src += " + "
		}
		src += vrmMIDIKeyLabel(*b.MIDI)
	}
	return label + "  ←  " + src
}

// vrmMIDIKeyLabel names a MIDI key by message type + number (channel from status low nibble).
func vrmMIDIKeyLabel(k vrbind.MIDIKey) string {
	ch := int(k.Status&0x0F) + 1
	switch k.Status & 0xF0 {
	case 0x90, 0x80:
		return fmt.Sprintf("Note %d (ch%d)", k.Data1, ch)
	case 0xB0:
		return fmt.Sprintf("CC %d (ch%d)", k.Data1, ch)
	default:
		return fmt.Sprintf("MIDI %02X %d (ch%d)", k.Status&0xF0, k.Data1, ch)
	}
}

func vrmActionOpts() []ssOpt {
	var out []ssOpt
	for _, a := range vrbind.Actions() {
		out = append(out, ssOpt{Val: string(a.ID), Label: a.Label, Sub: string(a.ID)})
	}
	return out
}

func vrmVRSlotOpts() []ssOpt {
	out := []ssOpt{{Val: "", Label: i18n.T("settings.body.vrctools.noneOption")}}
	for _, s := range vrbind.VRActionSlots() {
		out = append(out, ssOpt{Val: s, Label: s})
	}
	return out
}

// vrmTargetRow renders the target picker for a bindable action's target kind:
// smart-select for enumerable targets, free-text field for OBS inputs.
func (u *UI) vrmTargetRow(ssID, pickAct, valAct, cur string, kind vrbind.TargetKind) string {
	f := &u.svc.Cfg.Features.VROverlay
	switch kind {
	case vrbind.TargetNone:
		return ""
	case vrbind.TargetOBSInput:
		return field(i18n.T("settings.vr.obsInputName"), valAct, cur, "text")
	case vrbind.TargetInstance:
		return smartSelect(ssID, i18n.T("settings.vr.obsInstance"), pickAct, cur, func() []ssOpt {
			out := []ssOpt{{Val: "", Label: i18n.T("settings.vr.local")}}
			if u.svc.OBSControl != nil {
				for _, in := range u.svc.OBSControl.Statuses() {
					out = append(out, ssOpt{Val: in.ID, Label: in.ID})
				}
			}
			return out
		})
	case vrbind.TargetOverlay:
		return smartSelect(ssID, i18n.T("settings.vr.overlay"), pickAct, cur, func() []ssOpt {
			var out []ssOpt
			for _, o := range f.Overlays {
				out = append(out, ssOpt{Val: o.ID, Label: o.ID, Sub: o.Type})
			}
			return out
		})
	case vrbind.TargetAppGroup:
		return smartSelect(ssID, i18n.T("settings.vr.appGroup"), pickAct, cur, func() []ssOpt {
			var out []ssOpt
			for _, g := range u.svc.Cfg.Features.AppGroups.Groups {
				out = append(out, ssOpt{Val: g.ID, Label: g.ID})
			}
			return out
		})
	}
	return field(i18n.T("settings.vr.target"), valAct, cur, "text")
}

// ── overlay layouts ──

func (u *UI) vrLayoutsModal() {
	f := &u.svc.Cfg.Features.VROverlay
	var b strings.Builder
	if len(f.Layouts) == 0 {
		b.WriteString(emptyState(i18n.T("settings.vr.emptyLayouts")))
	}
	for i, L := range f.Layouts {
		is := strconv.Itoa(i)
		b.WriteString(itemRow(L.Name, i18n.Tn("settings.vr.overlayCount", len(L.Overlays)),
			btn(i18n.T("settings.vr.load"), "primary", "vr-lay-load:"+is, ""),
			btn(i18n.T("settings.vr.rename"), "outline", "vr-lay-ren:"+is, ""),
			btn(i18n.T("settings.vr.export"), "ghost", "pick-save:json:vr-lay-exp-"+is, ""),
			btn(i18n.T("common.delete"), "ghost", "vr-lay-del:"+is, "")))
	}
	u.openModal(modal(i18n.T("settings.vr.layoutsTitle"), b.String(),
		btn(i18n.T("settings.vr.saveCurrentAs"), "primary", "vr-lay-savecur-open", "")+
			btn(i18n.T("settings.vr.import"), "outline", "pick-file:vr-lay-imp", "")+
			btn(i18n.T("common.close"), "outline", "modal-close", "")))
}

func (u *UI) vrmRenameModal(i int) {
	f := &u.svc.Cfg.Features.VROverlay
	if i < 0 || i >= len(f.Layouts) {
		return
	}
	body := `<form class=set-dlgform data-act=vr-lay-rensave:` + strconv.Itoa(i) + `>` +
		`<label class=field data-label=vr-lay-name><span class=field-label>` + i18n.T("settings.vr.layoutName") + `</span>` +
		`<input class=field-input type=text name=name value=` + attrQ(f.Layouts[i].Name) + `></label>` +
		`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("settings.vr.renameLayout") + `</button></form>`
	u.openModal(modal(i18n.T("settings.vr.renameLayout"), body, ""))
}

func (u *UI) vrmSaveCurModal() {
	body := `<form class=set-dlgform data-act=vr-lay-savecur>` +
		`<label class=field data-label=vr-lay-name><span class=field-label>` + i18n.T("settings.vr.layoutName") + `</span>` +
		`<input class=field-input type=text name=name placeholder="` + html.EscapeString(i18n.T("settings.vr.layoutName")) + `"></label>` +
		`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("settings.vr.saveLayout") + `</button></form>` +
		`<div class=set-note>` + i18n.T("settings.vr.saveLayoutNote") + `</div>`
	u.openModal(modal(i18n.T("settings.vr.saveLayout"), body, ""))
}

// vrmSnapshotLayout captures the current overlays + menu placement (Fyne port).
func (u *UI) vrmSnapshotLayout(name string) config.VRLayout {
	f := u.svc.Cfg.Features.VROverlay
	return config.VRLayout{
		Name: name, Overlays: append([]config.VROverlay(nil), f.Overlays...),
		MenuSnap: f.MenuSnap, MenuX: f.MenuX, MenuY: f.MenuY, MenuZ: f.MenuZ,
		MenuYaw: f.MenuYaw, MenuPitch: f.MenuPitch, MenuWidth: f.MenuWidth, MenuBg: f.MenuBg,
	}
}

// vrmLoadLayout applies a saved layout to the live config (Fyne port).
func (u *UI) vrmLoadLayout(i int) {
	f := &u.svc.Cfg.Features.VROverlay
	if i < 0 || i >= len(f.Layouts) {
		return
	}
	L := f.Layouts[i]
	f.Overlays = append([]config.VROverlay(nil), L.Overlays...)
	f.MenuSnap, f.MenuX, f.MenuY, f.MenuZ = L.MenuSnap, L.MenuX, L.MenuY, L.MenuZ
	f.MenuYaw, f.MenuPitch, f.MenuWidth, f.MenuBg = L.MenuYaw, L.MenuPitch, L.MenuWidth, L.MenuBg
	u.saveCfg()
	u.toast(i18n.T("settings.vr.loadedLayout", i18n.A{"name": L.Name}))
}

func (u *UI) vrmExportLayout(i int, path string) {
	f := &u.svc.Cfg.Features.VROverlay
	if i < 0 || i >= len(f.Layouts) || path == "" {
		return
	}
	data, err := json.MarshalIndent(f.Layouts[i], "", "  ")
	if err == nil {
		err = os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		u.toast(i18n.T("settings.toast.exportFailed") + err.Error())
		return
	}
	u.toast(i18n.T("settings.vr.exported", i18n.A{"name": f.Layouts[i].Name}))
}

func (u *UI) vrmImportLayout(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		u.toast(i18n.T("settings.vr.importFailed") + err.Error())
		return
	}
	var L config.VRLayout
	if err := json.Unmarshal(data, &L); err != nil {
		u.toast(i18n.T("settings.vr.importFailedNotJson") + err.Error())
		return
	}
	if L.Name == "" {
		L.Name = i18n.T("settings.vr.importedLayoutName")
	}
	f := &u.svc.Cfg.Features.VROverlay
	f.Layouts = append(f.Layouts, L)
	u.saveCfg()
	u.vrLayoutsModal()
	u.toast(i18n.T("settings.vr.imported", i18n.A{"name": L.Name}))
}

// ── wrist quick buttons ──

// vrmQuickActionOpts: every bindable action + the two editor-local loads (Fyne
// quickActionOptions port).
func vrmQuickActionOpts() []ssOpt {
	out := vrmActionOpts()
	out = append(out,
		ssOpt{Val: "layout.load", Label: i18n.T("settings.vr.loadOverlayLayout"), Sub: "layout.load"},
		ssOpt{Val: "campath.load", Label: i18n.T("settings.vr.loadCameraPath"), Sub: "campath.load"})
	return out
}

func vrmQuickActionLabel(id string) string {
	for _, o := range vrmQuickActionOpts() {
		if o.Val == id {
			return o.Label
		}
	}
	return id
}

func (u *UI) vrQuickButtonsModal() {
	f := &u.svc.Cfg.Features.VROverlay
	s := u.vrm()
	s.mu.Lock()
	actID, tgt, label, glyph := s.qbAction, s.qbTarget, s.qbLabel, s.qbGlyph
	s.mu.Unlock()

	var b strings.Builder
	if len(f.QuickButtons) == 0 {
		b.WriteString(emptyState(i18n.T("settings.vr.emptyQuickButtons")))
	}
	for i, q := range f.QuickButtons {
		sum := vrmQuickActionLabel(q.Action)
		if q.Target != "" {
			sum += " [" + q.Target + "]"
		}
		title := q.Label
		if title == "" {
			title = sum
		}
		b.WriteString(itemRow(title, sum, btn(i18n.T("common.delete"), "ghost", "vr-qb-del:"+strconv.Itoa(i), "")))
	}

	b.WriteString(`<div class=card-label>` + i18n.T("settings.vr.addQuickButtonHeader") + `</div>`)
	b.WriteString(`<div class=set-note>` + i18n.T("settings.vr.quickButtonsNote") + `</div>`)
	b.WriteString(smartSelect("vr-qb-action", i18n.T("settings.vr.action"), "vr-qb-actpick:", actID, vrmQuickActionOpts))
	b.WriteString(u.vrmQuickTargetRow(actID, tgt))
	b.WriteString(field(i18n.T("settings.vr.buttonLabel"), "vr-qb-label", label, "text"))
	b.WriteString(field(i18n.T("settings.vr.glyph"), "vr-qb-glyph", glyph, "text"))
	b.WriteString(btnRow(btn(i18n.T("settings.vr.addQuickButton"), "primary", "vr-qb-add", "")))
	u.openModal(modal(i18n.T("settings.vr.wristQuickButtons"), b.String(), ""))
}

// vrmQuickTargetRow mirrors the Fyne per-action target options incl. the two
// editor-local loads (layout picks by name, camera path picks by file).
func (u *UI) vrmQuickTargetRow(actID, cur string) string {
	f := &u.svc.Cfg.Features.VROverlay
	switch actID {
	case "layout.load":
		return smartSelect("vr-qb-target", i18n.T("settings.vr.layout"), "vr-qb-tgtpick:", cur, func() []ssOpt {
			var out []ssOpt
			for _, l := range f.Layouts {
				out = append(out, ssOpt{Val: l.Name, Label: l.Name, Sub: i18n.Tn("settings.vr.overlayCount", len(l.Overlays))})
			}
			return out
		})
	case "campath.load":
		return smartSelect("vr-qb-target", i18n.T("settings.vr.cameraPath"), "vr-qb-tgtpick:", cur, func() []ssOpt {
			var out []ssOpt
			if u.svc.VRCTools != nil {
				for _, p := range u.svc.VRCTools.CamPaths() {
					out = append(out, ssOpt{Val: p.File, Label: p.Name, Sub: p.Folder()})
				}
			}
			return out
		})
	case string(vrbind.ActOverlayToggle), string(vrbind.ActOverlayShow), string(vrbind.ActOverlayHide):
		return u.vrmTargetRow("vr-qb-target", "vr-qb-tgtpick:", "vr-qb-tgtval", cur, vrbind.TargetOverlay)
	case string(vrbind.ActOBSRecord), string(vrbind.ActOBSStream):
		return u.vrmTargetRow("vr-qb-target", "vr-qb-tgtpick:", "vr-qb-tgtval", cur, vrbind.TargetInstance)
	case string(vrbind.ActOBSMic):
		return u.vrmTargetRow("vr-qb-target", "vr-qb-tgtpick:", "vr-qb-tgtval", cur, vrbind.TargetOBSInput)
	case string(vrbind.ActAppGroupLaunch):
		return u.vrmTargetRow("vr-qb-target", "vr-qb-tgtpick:", "vr-qb-tgtval", cur, vrbind.TargetAppGroup)
	}
	return ""
}

func (u *UI) vrmAddQuickButton() {
	f := &u.svc.Cfg.Features.VROverlay
	s := u.vrm()
	s.mu.Lock()
	actID, tgt, label, glyph := s.qbAction, s.qbTarget, s.qbLabel, s.qbGlyph
	s.mu.Unlock()
	if actID == "" {
		u.toast(i18n.T("settings.vr.pickAction"))
		return
	}
	f.QuickButtons = append(f.QuickButtons, config.VRQuickButton{
		Label: label, Glyph: glyph, Action: actID, Target: tgt,
	})
	u.saveCfg()
	s.mu.Lock()
	s.qbLabel, s.qbGlyph, s.qbTarget = "", "", ""
	s.mu.Unlock()
	u.vrQuickButtonsModal()
}

// ── per-world layouts ──

func vrmWorldModeOpts() []ssOpt {
	return []ssOpt{
		{Val: "off", Label: i18n.T("common.off"), Sub: i18n.T("settings.vr.worldModeOffSub")},
		{Val: "notify", Label: i18n.T("settings.vr.worldModeNotify"), Sub: i18n.T("settings.vr.worldModeNotifySub")},
		{Val: "auto", Label: i18n.T("settings.vr.worldModeAuto"), Sub: i18n.T("settings.vr.worldModeAutoSub")},
	}
}

func (u *UI) vrWorldLayoutsModal() {
	f := &u.svc.Cfg.Features.VROverlay
	layoutOpts := func() []ssOpt {
		var out []ssOpt
		for _, l := range f.Layouts {
			out = append(out, ssOpt{Val: l.Name, Label: l.Name})
		}
		return out
	}

	var b strings.Builder
	b.WriteString(`<div class=set-note>` + i18n.T("settings.vr.worldLayoutsNote") + `</div>`)
	b.WriteString(smartSelect("vr-wl-mode", i18n.T("settings.vr.autoApplyMode"), "vr-wl-modepick:", f.ResolvedWorldLayoutMode(), vrmWorldModeOpts))
	if len(f.WorldLayouts) == 0 {
		b.WriteString(emptyState(i18n.T("settings.vr.emptyWorldBindings")))
	}
	for i, wl := range f.WorldLayouts {
		is := strconv.Itoa(i)
		name := wl.WorldName
		if name == "" {
			name = wl.WorldID
		}
		enLbl := i18n.T("settings.vr.toggleOff")
		if wl.Enabled {
			enLbl = i18n.T("settings.vr.toggleOn")
		}
		b.WriteString(itemRow(name, i18n.T("settings.vr.layoutPrefix")+wl.Layout,
			btn(enLbl, "outline", "vr-wl-en:"+is, ""),
			smartSelect("vr-wl-lay-"+is, "", "vr-wl-set:"+is+":", wl.Layout, layoutOpts),
			btn(i18n.T("common.delete"), "ghost", "vr-wl-del:"+is, "")))
	}
	u.openModal(modal(i18n.T("settings.vr.perWorldLayouts"), b.String(),
		btn(i18n.T("settings.vr.bindCurrentWorld"), "primary", "vr-wl-bind", "")+btn(i18n.T("common.close"), "outline", "modal-close", "")))
}

func (u *UI) vrmBindWorldModal() {
	f := &u.svc.Cfg.Features.VROverlay
	if u.svc.VRCTools == nil {
		u.toast(i18n.T("settings.vr.vrcToolsOff"))
		return
	}
	loc, ok := u.svc.VRCTools.CurrentWorld()
	if !ok || loc.WorldID == "" {
		u.toast(i18n.T("settings.vr.currentWorldUnknown"))
		return
	}
	if len(f.Layouts) == 0 {
		u.toast(i18n.T("settings.vr.noSavedLayouts"))
		return
	}
	s := u.vrm()
	s.mu.Lock()
	s.wlWorldID, s.wlWorldName = loc.WorldID, loc.WorldName
	if s.wlPick == "" {
		s.wlPick = f.Layouts[0].Name
	}
	pick := s.wlPick
	s.mu.Unlock()
	body := `<div class=set-note>` + i18n.T("settings.vr.worldPrefix") + html.EscapeString(loc.WorldName) + `</div>` +
		smartSelect("vr-wl-bindlay", i18n.T("settings.vr.layout"), "vr-wl-bindpick:", pick, func() []ssOpt {
			var out []ssOpt
			for _, l := range f.Layouts {
				out = append(out, ssOpt{Val: l.Name, Label: l.Name})
			}
			return out
		})
	u.openModal(modal(i18n.T("settings.vr.bindCurrentWorldTitle"), body,
		btn(i18n.T("settings.vr.bind"), "primary", "vr-wl-bindgo", "")+btn(i18n.T("common.cancel"), "outline", "modal-close", "")))
}

func (u *UI) vrmBindWorldGo() {
	f := &u.svc.Cfg.Features.VROverlay
	s := u.vrm()
	s.mu.Lock()
	wid, wname, pick := s.wlWorldID, s.wlWorldName, s.wlPick
	s.mu.Unlock()
	if wid == "" || pick == "" {
		return
	}
	for i := range f.WorldLayouts {
		if f.WorldLayouts[i].WorldID == wid {
			f.WorldLayouts[i].Layout, f.WorldLayouts[i].WorldName, f.WorldLayouts[i].Enabled = pick, wname, true
			u.saveCfg()
			u.vrWorldLayoutsModal()
			return
		}
	}
	f.WorldLayouts = append(f.WorldLayouts, config.VRWorldLayout{
		WorldID: wid, WorldName: wname, Layout: pick, Enabled: true,
	})
	u.saveCfg()
	u.vrWorldLayoutsModal()
}
