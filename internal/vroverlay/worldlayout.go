package vroverlay

import (
	"strings"
	"time"

	"rave.page/mate/internal/config"
)

// Per-VRChat-world overlay layouts (#24): a config binding maps a world ID to a saved layout; on
// world change (vrctools location timeline via SetWorldSource) the mode decides: off = nothing,
// notify = head-locked toast + menu hint, auto = apply. Runs on the VR goroutine (Manager.tick).

const (
	toastKey = "page.rave.mate.__toast" // head-locked notice (world-layout notify/auto feedback)
	toastDur = 6 * time.Second
)

// worldSuggest is a pending notify-mode suggestion for the world just joined.
type worldSuggest struct {
	layout, worldName string
}

// SetWorldSource wires the current-VRChat-world provider (the vrctools location timeline) for
// per-world layouts. fn must be safe to call from the VR goroutine.
func (m *Manager) SetWorldSource(fn func() (id, name string, ok bool)) { m.worldSrc = fn }

// findWorldBinding returns the first binding for worldID (enabled or not).
func findWorldBinding(feat config.VROverlayFeature, worldID string) (config.VRWorldLayout, bool) {
	for _, b := range feat.WorldLayouts {
		if b.WorldID == worldID && worldID != "" {
			return b, true
		}
	}
	return config.VRWorldLayout{}, false
}

// resolveWorldLayout returns the first ENABLED binding for worldID whose layout exists.
func resolveWorldLayout(feat config.VROverlayFeature, worldID string) (config.VRWorldLayout, bool) {
	if worldID == "" {
		return config.VRWorldLayout{}, false
	}
	for _, b := range feat.WorldLayouts {
		if b.Enabled && b.WorldID == worldID && layoutExists(feat, b.Layout) {
			return b, true
		}
	}
	return config.VRWorldLayout{}, false
}

func layoutExists(feat config.VROverlayFeature, name string) bool {
	for _, l := range feat.Layouts {
		if l.Name == name {
			return true
		}
	}
	return false
}

// applyLayoutTo loads layout name into f (overlays + menu placement); false if absent. Pure -
// shared by the editor's LAYOUTS page, quick buttons, and the world auto-apply.
func applyLayoutTo(f *config.VROverlayFeature, name string) bool {
	for _, l := range f.Layouts {
		if l.Name != name {
			continue
		}
		f.Overlays = append([]config.VROverlay(nil), l.Overlays...)
		f.MenuSnap, f.MenuX, f.MenuY, f.MenuZ = l.MenuSnap, l.MenuX, l.MenuY, l.MenuZ
		f.MenuYaw, f.MenuPitch, f.MenuWidth, f.MenuBg = l.MenuYaw, l.MenuPitch, l.MenuWidth, l.MenuBg
		return true
	}
	return false
}

// applyLayout applies a named layout to the live config (persisted via mutate).
func (m *Manager) applyLayout(name string) bool {
	if m.mutate == nil {
		return false
	}
	ok := false
	m.mutate(func(f *config.VROverlayFeature) { ok = applyLayoutTo(f, name) })
	return ok
}

// tickWorldLayout detects a world change and runs the configured mode (off/notify/auto). The
// FIRST observed world also counts as a change, so a rave-mate restart mid-set still re-applies
// the bound layout (crash recovery); a SteamVR reconnect does not re-fire (lastWorldID persists).
func (m *Manager) tickWorldLayout(feat config.VROverlayFeature) {
	if m.worldSrc == nil {
		return
	}
	id, name, ok := m.worldSrc()
	if !ok || id == "" || id == m.lastWorldID {
		return
	}
	m.lastWorldID = id
	m.suggest = nil
	b, found := resolveWorldLayout(feat, id)
	if !found {
		return
	}
	where := name
	if where == "" {
		where = id
	}
	switch feat.ResolvedWorldLayoutMode() {
	case "off":
	case "auto":
		if m.applyLayout(b.Layout) {
			m.log.Info(logTag, "world layout auto-applied", map[string]any{"layout": b.Layout, "world": id})
			m.showToast("Overlay layout '" + b.Layout + "' applied (" + where + ")")
			if m.edit != nil {
				m.edit.evt("world layout AUTO %q (world %s)", b.Layout, id)
			}
		}
	default: // notify
		m.suggest = &worldSuggest{layout: b.Layout, worldName: name}
		m.showToast("Overlay layout '" + b.Layout + "' available here - apply it from the wrist menu")
		if m.edit != nil {
			m.edit.evt("world layout SUGGEST %q (world %s)", b.Layout, id)
		}
	}
}

// applySuggest applies the pending notify-mode suggestion (menu row). VR goroutine.
func (m *Manager) applySuggest() {
	if m.suggest == nil {
		return
	}
	if m.applyLayout(m.suggest.layout) {
		m.showToast("Overlay layout '" + m.suggest.layout + "' applied")
	}
	m.suggest = nil
}

// showToast queues a short head-locked toast (driveToast renders it).
func (m *Manager) showToast(msg string) {
	m.toastMsg = msg
	m.toastUntil = time.Now().Add(toastDur)
}

// driveToast reconciles the toast overlay: visor-locked below center, change-gated like every
// surface. Tick-rate (10fps) is plenty for a 6s notice.
func (m *Manager) driveToast() {
	show := m.toastMsg != "" && time.Now().Before(m.toastUntil)
	if !show {
		if m.toastShow.changed(false) {
			_ = m.rt.Show(toastKey, false)
		}
		return
	}
	if !m.toastEnsure {
		if m.rt.EnsureOverlay(toastKey, "rave-mate notice") != nil {
			return
		}
		m.toastEnsure = true
	}
	if m.toastSig != m.toastMsg {
		if m.rt.SetTexture(toastKey, m.rend.RenderTooltip(m.toastMsg)) != nil {
			return
		}
		m.toastSig = m.toastMsg
	}
	if tf := (Transform{Snap: HandHead, Y: -0.18, Z: -0.55, WidthM: 0.34, Opacity: 0.97}); m.toastTf.changed(tf) {
		_ = m.rt.SetTransform(toastKey, tf)
	}
	if m.toastShow.changed(true) {
		_ = m.rt.Show(toastKey, true)
	}
}

// ---- editor-side (LAYOUTS page) helpers ----

// currentWorld reads the wired world source (id, name, ok).
func (e *editor) currentWorld() (string, string, bool) {
	if e.m.worldSrc == nil {
		return "", "", false
	}
	return e.m.worldSrc()
}

// worldLayoutName names a world-bound layout (world name, else a shortened id).
func worldLayoutName(name, id string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		n = id
		if len(n) > 14 {
			n = n[:14]
		}
	}
	return "world: " + n
}

// saveLayoutForWorld snapshots the current arrangement as a layout named for the world and
// upserts an ENABLED binding to it - the one-tap "Save layout for this world" row.
func (e *editor) saveLayoutForWorld(id, name string) {
	lname := worldLayoutName(name, id)
	e.m.mutate(func(f *config.VROverlayFeature) {
		snap := config.VRLayout{
			Name: lname, Overlays: append([]config.VROverlay(nil), f.Overlays...),
			MenuSnap: f.MenuSnap, MenuX: f.MenuX, MenuY: f.MenuY, MenuZ: f.MenuZ,
			MenuYaw: f.MenuYaw, MenuPitch: f.MenuPitch, MenuWidth: f.MenuWidth, MenuBg: f.MenuBg,
		}
		replaced := false
		for i := range f.Layouts {
			if f.Layouts[i].Name == lname {
				f.Layouts[i], replaced = snap, true
				break
			}
		}
		if !replaced {
			f.Layouts = append(f.Layouts, snap)
		}
		for i := range f.WorldLayouts {
			if f.WorldLayouts[i].WorldID == id {
				f.WorldLayouts[i].Layout, f.WorldLayouts[i].WorldName, f.WorldLayouts[i].Enabled = lname, name, true
				return
			}
		}
		f.WorldLayouts = append(f.WorldLayouts, config.VRWorldLayout{WorldID: id, WorldName: name, Layout: lname, Enabled: true})
	})
	e.evt("layout saved for world %s -> %q", id, lname)
}

// toggleWorldBinding flips a world binding's enable.
func (e *editor) toggleWorldBinding(id string) {
	e.m.mutate(func(f *config.VROverlayFeature) {
		for i := range f.WorldLayouts {
			if f.WorldLayouts[i].WorldID == id {
				f.WorldLayouts[i].Enabled = !f.WorldLayouts[i].Enabled
				return
			}
		}
	})
}

// cycleWorldLayoutMode cycles the auto-apply mode off → notify → auto.
func (e *editor) cycleWorldLayoutMode() {
	e.m.mutate(func(f *config.VROverlayFeature) {
		switch f.ResolvedWorldLayoutMode() {
		case "off":
			f.WorldLayoutMode = "notify"
		case "notify":
			f.WorldLayoutMode = "auto"
		default:
			f.WorldLayoutMode = "off"
		}
	})
}
