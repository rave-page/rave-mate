package webui

import (
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/vrbind"
)

func profFixture() (config.MIDIFeature, config.VROverlayFeature) {
	m := config.MIDIFeature{Controllers: []config.MIDIControllerMap{
		{Name: "Pioneer", Port: "DDJ-400"},
		{Name: "NI", Port: "Kontrol"},
	}}
	f := config.VROverlayFeature{Binds: []vrbind.Bind{
		{Action: vrbind.ActCEAudition, MIDI: &vrbind.MIDIKey{Status: 0x90, Data1: 40, Port: "Pioneer DDJ-400 MIDI 1"}},
		{Action: vrbind.ActLibNav, MIDI: &vrbind.MIDIKey{Status: 0xB0, Data1: 20, Port: "Pioneer DDJ-400 MIDI 1", Mode: vrbind.ModeRel2C, Step: 4, Rev: true}},
		{Action: vrbind.ActNavBack, MIDI: &vrbind.MIDIKey{Status: 0x90, Data1: 41}},                    // any-device
		{Action: vrbind.ActCECue, MIDI: &vrbind.MIDIKey{Status: 0x90, Data1: 42, Port: "Akai MPK"}},    // orphan device
		{Action: vrbind.ActOBSRecord, MIDI: &vrbind.MIDIKey{Status: 0x90, Data1: 43, Port: "DDJ-400"}}, // VR group: never profile-managed
	}}
	return m, f
}

func TestUmCopyProfileRemapsAndDedups(t *testing.T) {
	m, f := profFixture()
	n := umCopyProfile(m, &f, "DDJ-400", "Kontrol")
	if n != 2 || len(f.Binds) != 7 {
		t.Fatalf("copy: n=%d len=%d", n, len(f.Binds))
	}
	for _, b := range f.Binds[5:] {
		if b.MIDI.Port != "Kontrol" {
			t.Fatalf("copied port: %q", b.MIDI.Port)
		}
	}
	// modes/sensitivity/reverse survive
	if k := f.Binds[6].MIDI; k.Mode != vrbind.ModeRel2C || k.Step != 4 || !k.Rev {
		t.Fatalf("copied key lost settings: %+v", k)
	}
	// idempotent: everything already exists in dst
	if n := umCopyProfile(m, &f, "DDJ-400", "Kontrol"); n != 0 {
		t.Fatalf("re-copy added %d", n)
	}
	// copy to the any-device profile strips the port
	if n := umCopyProfile(m, &f, "Akai MPK", config.BindProfileAny); n != 1 {
		t.Fatalf("copy to any: %d", n)
	}
	if k := f.Binds[len(f.Binds)-1].MIDI; k.Port != "" {
		t.Fatalf("any-device copy kept port %q", k.Port)
	}
	// self-copy is a no-op
	if n := umCopyProfile(m, &f, "DDJ-400", "DDJ-400"); n != 0 {
		t.Fatal("self copy")
	}
}

func TestUmClearProfileScopesToUIBinds(t *testing.T) {
	m, f := profFixture()
	if n := umClearProfile(m, &f, "DDJ-400"); n != 2 {
		t.Fatalf("clear: %d", n)
	}
	// VR-group bind on the same device + other profiles survive
	if len(f.Binds) != 3 {
		t.Fatalf("left: %d", len(f.Binds))
	}
	for _, b := range f.Binds {
		if b.Action == vrbind.ActCEAudition || b.Action == vrbind.ActLibNav {
			t.Fatal("profile bind survived clear")
		}
	}
	if f.Binds[2].Action != vrbind.ActOBSRecord {
		t.Fatal("VR bind must survive")
	}
}

// TestLearnAssignsSourceDeviceProfile: a captured bind (port stored at learn time) lands in
// the profile of the device that sent it.
func TestLearnAssignsSourceDeviceProfile(t *testing.T) {
	m, _ := profFixture()
	if m.BindProfileKey("Pioneer DDJ-400 MIDI 1") != "DDJ-400" {
		t.Fatal("configured device")
	}
	if m.BindProfileKey("") != config.BindProfileAny {
		t.Fatal("portless capture → any-device")
	}
	if m.BindProfileKey("Akai MPK") != "Akai MPK" {
		t.Fatal("unconfigured device keeps its own profile")
	}
}
