package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBindProfileKey(t *testing.T) {
	m := MIDIFeature{Controllers: []MIDIControllerMap{
		{Name: "Pioneer", Port: "DDJ-400"},
		{Name: "NI", Port: "kontrol"},
	}}
	cases := []struct{ port, want string }{
		{"", BindProfileAny},
		{"Pioneer DDJ-400 MIDI 1", "DDJ-400"}, // matches controller (case-insensitive substring)
		{"traktor KONTROL S4", "kontrol"},     // second controller
		{"Akai MPK Mini", "Akai MPK Mini"},    // no controller → raw port is its own profile
	}
	for _, c := range cases {
		if got := m.BindProfileKey(c.port); got != c.want {
			t.Fatalf("BindProfileKey(%q)=%q want %q", c.port, got, c.want)
		}
	}
}

func TestBindProfileDisableRoundTrip(t *testing.T) {
	m := MIDIFeature{Controllers: []MIDIControllerMap{{Port: "DDJ-400"}}}
	if m.BindProfileDisabled("Pioneer DDJ-400") {
		t.Fatal("profiles start enabled")
	}
	m.SetBindProfileDisabled("DDJ-400", true)
	m.SetBindProfileDisabled("DDJ-400", true) // idempotent
	if len(m.DisabledBindProfiles) != 1 || !m.BindProfileDisabled("Pioneer DDJ-400 MIDI 1") {
		t.Fatalf("disable: %v", m.DisabledBindProfiles)
	}
	if m.BindProfileDisabled("") {
		t.Fatal("any-device profile must stay enabled")
	}
	m.SetBindProfileDisabled(BindProfileAny, true)
	if !m.BindProfileDisabled("") {
		t.Fatal("any-device profile disable")
	}
	m.SetBindProfileDisabled("DDJ-400", false)
	if m.BindProfileDisabled("Pioneer DDJ-400") {
		t.Fatal("re-enable")
	}
}

// TestLoadV29ProfilesDerived: a v29 config (flat binds, no profile keys) loads with every bind
// intact, all profiles enabled (dispatch identical), version bumped.
func TestLoadV29ProfilesDerived(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", dir)
	v29 := `{"version":29,"features":{"midi":{"enabled":true,
		"controllers":[{"name":"Pioneer","port":"DDJ-400","enabled":true,"driverFilter":[]}]},
		"vrOverlay":{"binds":[
			{"action":"ui.ce.audition","midi":{"status":144,"data1":40,"port":"Pioneer DDJ-400"}},
			{"action":"ui.nav.back","midi":{"status":144,"data1":41}}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(v29), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != configVersion {
		t.Fatalf("version: %d", cfg.Version)
	}
	binds := cfg.Features.VROverlay.Binds
	if len(binds) != 2 || binds[0].MIDI == nil || binds[0].MIDI.Port != "Pioneer DDJ-400" ||
		binds[1].MIDI == nil || binds[1].MIDI.Port != "" {
		raw, _ := json.Marshal(binds)
		t.Fatalf("binds changed: %s", raw)
	}
	m := cfg.Features.MIDI
	if m.BindProfileKey(binds[0].MIDI.Port) != "DDJ-400" || m.BindProfileKey(binds[1].MIDI.Port) != BindProfileAny {
		t.Fatal("profile derivation")
	}
	if m.BindProfileDisabled(binds[0].MIDI.Port) || m.BindProfileDisabled(binds[1].MIDI.Port) {
		t.Fatal("v29 binds must stay active post-migration")
	}
}
