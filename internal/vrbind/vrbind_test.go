package vrbind

import "testing"

func TestActionCatalogConsistent(t *testing.T) {
	for _, a := range Actions() {
		got, ok := ActionByID(a.ID)
		if !ok || got.ID != a.ID {
			t.Errorf("ActionByID(%q) miss", a.ID)
		}
		if a.Label == "" {
			t.Errorf("action %q has empty label", a.ID)
		}
	}
	if _, ok := ActionByID("nope.nope"); ok {
		t.Error("unknown action resolved")
	}
}

func TestMIDIKeyMatches(t *testing.T) {
	k := MIDIKey{Status: 0x90, Data1: 60}
	if !k.Matches(0x90, 60) {
		t.Error("should match same status+data1")
	}
	if k.Matches(0x90, 61) || k.Matches(0x80, 60) {
		t.Error("must not match different data1/status (velocity-independent only)")
	}
}

func TestDispatcherFire(t *testing.T) {
	d := NewDispatcher()
	var gotTarget string
	calls := 0
	d.Register(ActOBSRecord, func(target string) { calls++; gotTarget = target })

	if d.Fire(Bind{Action: ActOBSStream, Target: "x"}) {
		t.Error("unregistered action should not fire")
	}
	if !d.Fire(Bind{Action: ActOBSRecord, Target: "stream-pc"}) {
		t.Fatal("registered action should fire")
	}
	if calls != 1 || gotTarget != "stream-pc" {
		t.Errorf("handler got calls=%d target=%q", calls, gotTarget)
	}
}

func TestFireMIDI(t *testing.T) {
	d := NewDispatcher()
	rec, mic := 0, 0
	d.Register(ActOBSRecord, func(string) { rec++ })
	d.Register(ActOBSMic, func(string) { mic++ })
	binds := []Bind{
		{Action: ActOBSRecord, MIDI: &MIDIKey{0x90, 60}},
		{Action: ActOBSMic, Target: "Mic/Aux", MIDI: &MIDIKey{0xB0, 7}},
		{Action: ActOverlaysToggle, VRAction: "slot1"}, // no MIDI - must be ignored
	}
	if n := d.FireMIDI(binds, 0x90, 60); n != 1 || rec != 1 {
		t.Errorf("note 60 → n=%d rec=%d", n, rec)
	}
	if n := d.FireMIDI(binds, 0xB0, 7); n != 1 || mic != 1 {
		t.Errorf("cc 7 → n=%d mic=%d", n, mic)
	}
	if n := d.FireMIDI(binds, 0x90, 99); n != 0 {
		t.Errorf("unbound note → n=%d", n)
	}
}

func TestFireMIDIMultipleBindsSameAction(t *testing.T) {
	// "assign multiple keybinds": two MIDI inputs both fire one action.
	d := NewDispatcher()
	hits := 0
	d.Register(ActSTTSend, func(string) { hits++ })
	binds := []Bind{
		{Action: ActSTTSend, MIDI: &MIDIKey{0x90, 36}},
		{Action: ActSTTSend, MIDI: &MIDIKey{0x90, 38}},
	}
	d.FireMIDI(binds, 0x90, 36)
	d.FireMIDI(binds, 0x90, 38)
	if hits != 2 {
		t.Errorf("both binds should fire the action: hits=%d", hits)
	}
}

func TestFireVR(t *testing.T) {
	d := NewDispatcher()
	n := 0
	d.Register(ActEditorToggle, func(string) { n++ })
	binds := []Bind{
		{Action: ActEditorToggle, VRAction: "slot1"},
		{Action: ActOBSRecord, VRAction: "slot2", MIDI: &MIDIKey{0x90, 60}}, // dual-source
	}
	if got := d.FireVR(binds, "slot1"); got != 1 || n != 1 {
		t.Errorf("slot1 → got=%d n=%d", got, n)
	}
	if got := d.FireVR(binds, ""); got != 0 {
		t.Errorf("empty action must match nothing: got=%d", got)
	}
	if got := d.FireVR(binds, "slot9"); got != 0 {
		t.Errorf("unbound slot → got=%d", got)
	}
}

func TestVRActionSlotsStable(t *testing.T) {
	if len(VRActionSlots()) != 8 {
		t.Errorf("expected 8 slots, got %d", len(VRActionSlots()))
	}
}
