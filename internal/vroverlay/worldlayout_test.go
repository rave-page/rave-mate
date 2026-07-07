package vroverlay

import (
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

func wlFeat() config.VROverlayFeature {
	return config.VROverlayFeature{
		Layouts: []config.VRLayout{
			{Name: "club", Overlays: []config.VROverlay{{ID: "a", Type: "chat"}}, MenuWidth: 0.4},
			{Name: "home", Overlays: []config.VROverlay{{ID: "b", Type: "obs"}}},
		},
		WorldLayouts: []config.VRWorldLayout{
			{WorldID: "wrld_1", WorldName: "Club", Layout: "club", Enabled: true},
			{WorldID: "wrld_2", WorldName: "Off", Layout: "club", Enabled: false},
			{WorldID: "wrld_3", WorldName: "Gone", Layout: "missing", Enabled: true},
		},
	}
}

func TestResolveWorldLayout(t *testing.T) {
	feat := wlFeat()
	if b, ok := resolveWorldLayout(feat, "wrld_1"); !ok || b.Layout != "club" {
		t.Fatalf("wrld_1 = %+v ok=%v, want club", b, ok)
	}
	if _, ok := resolveWorldLayout(feat, "wrld_2"); ok {
		t.Fatal("disabled binding must not resolve")
	}
	if _, ok := resolveWorldLayout(feat, "wrld_3"); ok {
		t.Fatal("binding to a missing layout must not resolve")
	}
	if _, ok := resolveWorldLayout(feat, ""); ok {
		t.Fatal("empty world must not resolve")
	}
	if b, ok := findWorldBinding(feat, "wrld_2"); !ok || b.Enabled {
		t.Fatalf("findWorldBinding must return disabled bindings too: %+v ok=%v", b, ok)
	}
}

func TestApplyLayoutTo(t *testing.T) {
	feat := wlFeat()
	feat.Overlays = []config.VROverlay{{ID: "old"}}
	if !applyLayoutTo(&feat, "club") {
		t.Fatal("apply club failed")
	}
	if len(feat.Overlays) != 1 || feat.Overlays[0].ID != "a" || feat.MenuWidth != 0.4 {
		t.Fatalf("layout not applied: %+v menuW=%v", feat.Overlays, feat.MenuWidth)
	}
	if applyLayoutTo(&feat, "nope") {
		t.Fatal("unknown layout must return false")
	}
}

func TestWorldLayoutName(t *testing.T) {
	if n := worldLayoutName("The Great Pub", "wrld_x"); n != "world: The Great Pub" {
		t.Fatalf("name = %q", n)
	}
	if n := worldLayoutName("", "wrld_12345678901234567890"); n != "world: wrld_123456789" {
		t.Fatalf("id fallback = %q", n)
	}
}

// wlManager builds a Manager wired for tickWorldLayout tests (no editor, fake runtime).
func wlManager(feat *config.VROverlayFeature, world *[3]string) *Manager {
	m := New(logbus.New(16), nil, &fakeRT{},
		func() config.VROverlayFeature { return *feat },
		func(fn func(*config.VROverlayFeature)) { fn(feat) })
	m.SetWorldSource(func() (string, string, bool) { return world[0], world[1], world[2] == "ok" })
	return m
}

func TestTickWorldLayoutAuto(t *testing.T) {
	feat := wlFeat()
	feat.WorldLayoutMode = "auto"
	feat.Overlays = []config.VROverlay{{ID: "old"}}
	world := [3]string{"wrld_1", "Club", "ok"}
	m := wlManager(&feat, &world)

	m.tickWorldLayout(m.cfg()) // first observation counts as a change (restart mid-set re-applies)
	if len(feat.Overlays) != 1 || feat.Overlays[0].ID != "a" {
		t.Fatalf("auto mode did not apply: %+v", feat.Overlays)
	}
	if m.toastMsg == "" {
		t.Fatal("auto apply should toast")
	}
	// Same world again → no re-fire (would clobber manual tweaks every tick).
	feat.Overlays = []config.VROverlay{{ID: "tweaked"}}
	m.tickWorldLayout(m.cfg())
	if feat.Overlays[0].ID != "tweaked" {
		t.Fatal("same world must not re-apply")
	}
	// Unbound world → nothing.
	world[0] = "wrld_unknown"
	m.tickWorldLayout(m.cfg())
	if feat.Overlays[0].ID != "tweaked" {
		t.Fatal("unbound world must not apply")
	}
	// Back to the bound world → change fires again.
	world[0] = "wrld_1"
	m.tickWorldLayout(m.cfg())
	if feat.Overlays[0].ID != "a" {
		t.Fatal("rejoining the bound world must re-apply")
	}
}

func TestTickWorldLayoutNotifyAndOff(t *testing.T) {
	feat := wlFeat() // default mode = notify
	feat.Overlays = []config.VROverlay{{ID: "old"}}
	world := [3]string{"wrld_1", "Club", "ok"}
	m := wlManager(&feat, &world)

	m.tickWorldLayout(m.cfg())
	if feat.Overlays[0].ID != "old" {
		t.Fatal("notify mode must not apply")
	}
	if m.suggest == nil || m.suggest.layout != "club" {
		t.Fatalf("suggest = %+v, want club", m.suggest)
	}
	m.applySuggest()
	if feat.Overlays[0].ID != "a" || m.suggest != nil {
		t.Fatalf("applySuggest: overlays=%+v suggest=%+v", feat.Overlays, m.suggest)
	}

	feat2 := wlFeat()
	feat2.WorldLayoutMode = "off"
	feat2.Overlays = []config.VROverlay{{ID: "old"}}
	world2 := [3]string{"wrld_1", "Club", "ok"}
	m2 := wlManager(&feat2, &world2)
	m2.tickWorldLayout(m2.cfg())
	if feat2.Overlays[0].ID != "old" || m2.suggest != nil || m2.toastMsg != "" {
		t.Fatal("off mode must do nothing")
	}
}

func TestTickWorldLayoutUnknownWorld(t *testing.T) {
	feat := wlFeat()
	feat.WorldLayoutMode = "auto"
	world := [3]string{"", "", "no"}
	m := wlManager(&feat, &world)
	m.tickWorldLayout(m.cfg())
	if m.lastWorldID != "" || m.suggest != nil {
		t.Fatal("unknown world must be a no-op")
	}
}

func TestSaveLayoutForWorldUpsert(t *testing.T) {
	feat := config.VROverlayFeature{Overlays: []config.VROverlay{{ID: "x", Type: "chat"}}, MenuWidth: 0.3}
	m := &Manager{rt: &fakeRT{}, mutate: func(fn func(*config.VROverlayFeature)) { fn(&feat) }}
	e := &editor{m: m}
	e.resetSession()

	e.saveLayoutForWorld("wrld_9", "Bar")
	if len(feat.Layouts) != 1 || feat.Layouts[0].Name != "world: Bar" {
		t.Fatalf("layouts = %+v", feat.Layouts)
	}
	if len(feat.WorldLayouts) != 1 || !feat.WorldLayouts[0].Enabled || feat.WorldLayouts[0].Layout != "world: Bar" {
		t.Fatalf("bindings = %+v", feat.WorldLayouts)
	}
	// Second save for the same world REPLACES (no duplicate layout/binding).
	feat.Overlays = []config.VROverlay{{ID: "y", Type: "obs"}}
	e.saveLayoutForWorld("wrld_9", "Bar")
	if len(feat.Layouts) != 1 || len(feat.WorldLayouts) != 1 {
		t.Fatalf("upsert duplicated: %d layouts, %d bindings", len(feat.Layouts), len(feat.WorldLayouts))
	}
	if feat.Layouts[0].Overlays[0].ID != "y" {
		t.Fatalf("layout not refreshed: %+v", feat.Layouts[0].Overlays)
	}
	// Binding toggle + mode cycle.
	e.toggleWorldBinding("wrld_9")
	if feat.WorldLayouts[0].Enabled {
		t.Fatal("toggle should disable")
	}
	e.cycleWorldLayoutMode() // notify (default) → auto
	if feat.ResolvedWorldLayoutMode() != "auto" {
		t.Fatalf("mode = %q, want auto", feat.ResolvedWorldLayoutMode())
	}
	e.cycleWorldLayoutMode()
	if feat.ResolvedWorldLayoutMode() != "off" {
		t.Fatalf("mode = %q, want off", feat.ResolvedWorldLayoutMode())
	}
}
