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
		{Action: ActOBSRecord, MIDI: &MIDIKey{Status: 0x90, Data1: 60}},
		{Action: ActOBSMic, Target: "Mic/Aux", MIDI: &MIDIKey{Status: 0xB0, Data1: 7}},
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
		{Action: ActSTTSend, MIDI: &MIDIKey{Status: 0x90, Data1: 36}},
		{Action: ActSTTSend, MIDI: &MIDIKey{Status: 0x90, Data1: 38}},
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
		{Action: ActOBSRecord, VRAction: "slot2", MIDI: &MIDIKey{Status: 0x90, Data1: 60}}, // dual-source
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

// ── FireMIDIMsg full-semantics tests ──

func TestHoldNotePressRelease(t *testing.T) {
	d := NewDispatcher()
	var seq []bool
	d.RegisterHold(ActCEAudition, func(_ string, down bool) { seq = append(seq, down) })
	binds := []Bind{{Action: ActCEAudition, MIDI: &MIDIKey{Status: 0x90, Data1: 40}}}
	d.FireMIDIMsg(binds, "DDJ", 0x90, 40, 100) // press
	d.FireMIDIMsg(binds, "DDJ", 0x90, 40, 100) // repeat press: deduped
	d.FireMIDIMsg(binds, "DDJ", 0x80, 40, 0)   // note-off = release
	d.FireMIDIMsg(binds, "DDJ", 0x80, 40, 0)   // repeat release: deduped
	if len(seq) != 2 || !seq[0] || seq[1] {
		t.Fatalf("want [down up], got %v", seq)
	}
	// zero-velocity note-on is also a release
	d.FireMIDIMsg(binds, "DDJ", 0x90, 40, 100)
	d.FireMIDIMsg(binds, "DDJ", 0x90, 40, 0)
	if len(seq) != 4 || !seq[2] || seq[3] {
		t.Fatalf("vel-0 release: got %v", seq)
	}
}

func TestHoldToggleMode(t *testing.T) {
	d := NewDispatcher()
	var seq []bool
	d.RegisterHold(ActCEAudition, func(_ string, down bool) { seq = append(seq, down) })
	binds := []Bind{{Action: ActCEAudition, MIDI: &MIDIKey{Status: 0x90, Data1: 41, Mode: ModeToggle}}}
	d.FireMIDIMsg(binds, "", 0x90, 41, 100) // press → down
	d.FireMIDIMsg(binds, "", 0x80, 41, 0)   // release ignored in toggle mode
	d.FireMIDIMsg(binds, "", 0x90, 41, 100) // press → up
	if len(seq) != 2 || !seq[0] || seq[1] {
		t.Fatalf("toggle: want [down up], got %v", seq)
	}
}

func TestStepRelativeEncodings(t *testing.T) {
	cases := []struct {
		mode string
		val  byte
		want int
	}{
		{ModeRel2C, 1, 1}, {ModeRel2C, 3, 3}, {ModeRel2C, 127, -1}, {ModeRel2C, 125, -3},
		{ModeRelSM, 1, 1}, {ModeRelSM, 0x41, -1}, {ModeRelSM, 0x43, -3},
		{ModeRel64, 65, 1}, {ModeRel64, 63, -1}, {ModeRel64, 68, 4},
	}
	for _, c := range cases {
		d := NewDispatcher()
		got := 0
		d.RegisterStep(ActLibNav, func(_ string, delta int) { got += delta })
		binds := []Bind{{Action: ActLibNav, MIDI: &MIDIKey{Status: 0xB0, Data1: 20, Mode: c.mode}}}
		d.FireMIDIMsg(binds, "", 0xB0, 20, c.val)
		if got != c.want {
			t.Errorf("%s val %d: want %d, got %d", c.mode, c.val, c.want, got)
		}
	}
}

func TestStepSensitivityAndRev(t *testing.T) {
	d := NewDispatcher()
	got := 0
	d.RegisterStep(ActCECursor, func(_ string, delta int) { got += delta })
	// Step=4: four raw +1 ticks = one emitted step
	binds := []Bind{{Action: ActCECursor, MIDI: &MIDIKey{Status: 0xB0, Data1: 21, Mode: ModeRel2C, Step: 4}}}
	for i := 0; i < 3; i++ {
		d.FireMIDIMsg(binds, "", 0xB0, 21, 1)
	}
	if got != 0 {
		t.Fatalf("3/4 ticks must not emit, got %d", got)
	}
	d.FireMIDIMsg(binds, "", 0xB0, 21, 1)
	if got != 1 {
		t.Fatalf("4th tick emits 1, got %d", got)
	}
	// Rev inverts
	binds[0].MIDI.Rev = true
	got = 0
	for i := 0; i < 4; i++ {
		d.FireMIDIMsg(binds, "", 0xB0, 21, 1)
	}
	if got != -1 {
		t.Fatalf("rev: want -1, got %d", got)
	}
}

func TestStepAbsoluteKnob(t *testing.T) {
	d := NewDispatcher()
	got := 0
	d.RegisterStep(ActLibNav, func(_ string, delta int) { got += delta })
	binds := []Bind{{Action: ActLibNav, MIDI: &MIDIKey{Status: 0xB0, Data1: 22, Mode: ModeAbs}}}
	d.FireMIDIMsg(binds, "", 0xB0, 22, 60) // first touch calibrates, no step
	if got != 0 {
		t.Fatalf("calibration must not step, got %d", got)
	}
	d.FireMIDIMsg(binds, "", 0xB0, 22, 63)
	if got != 3 {
		t.Fatalf("60→63: want +3, got %d", got)
	}
	d.FireMIDIMsg(binds, "", 0xB0, 22, 0) // big negative jump: capped at -stepCap
	if got != 3-stepCap {
		t.Fatalf("63→0 capped: want %d, got %d", 3-stepCap, got)
	}
}

func TestStepButtonPerPress(t *testing.T) {
	d := NewDispatcher()
	got := 0
	d.RegisterStep(ActCETrack, func(_ string, delta int) { got += delta })
	binds := []Bind{
		{Action: ActCETrack, MIDI: &MIDIKey{Status: 0x90, Data1: 50}},
		{Action: ActCETrack, MIDI: &MIDIKey{Status: 0x90, Data1: 51, Rev: true}},
	}
	d.FireMIDIMsg(binds, "", 0x90, 50, 100)
	d.FireMIDIMsg(binds, "", 0x80, 50, 0) // release: no step
	d.FireMIDIMsg(binds, "", 0x90, 51, 100)
	d.FireMIDIMsg(binds, "", 0x90, 51, 100)
	if got != -1 {
		t.Fatalf("+1 then -2: want -1, got %d", got)
	}
}

func TestTriggerPressOnlyAndEncoderFire(t *testing.T) {
	d := NewDispatcher()
	n := 0
	d.Register(ActCEDropAdd, func(string) { n++ })
	binds := []Bind{{Action: ActCEDropAdd, MIDI: &MIDIKey{Status: 0x90, Data1: 60}}}
	d.FireMIDIMsg(binds, "", 0x90, 60, 100)
	d.FireMIDIMsg(binds, "", 0x80, 60, 0) // release must not re-fire
	if n != 1 {
		t.Fatalf("trigger fired %d times, want 1", n)
	}
	// encoder-bound trigger: any motion fires
	binds = []Bind{{Action: ActCEDropAdd, MIDI: &MIDIKey{Status: 0xB0, Data1: 61, Mode: ModeRel2C}}}
	d.FireMIDIMsg(binds, "", 0xB0, 61, 127)
	if n != 2 {
		t.Fatalf("encoder trigger: want 2, got %d", n)
	}
}

func TestPortFilter(t *testing.T) {
	d := NewDispatcher()
	n := 0
	d.Register(ActCEUndo, func(string) { n++ })
	binds := []Bind{{Action: ActCEUndo, MIDI: &MIDIKey{Status: 0x90, Data1: 62, Port: "ddj-400"}}}
	d.FireMIDIMsg(binds, "Pioneer DDJ-400 MIDI 1", 0x90, 62, 100)
	d.FireMIDIMsg(binds, "Traktor Kontrol S4", 0x90, 62, 100) // wrong device
	if n != 1 {
		t.Fatalf("port filter: want 1, got %d", n)
	}
}

func TestGroupFilterDisablesUIBinds(t *testing.T) {
	d := NewDispatcher()
	ui, vr := 0, 0
	d.Register(ActCEUndo, func(string) { ui++ })
	d.Register(ActOBSRecord, func(string) { vr++ })
	d.SetGroupFilter(func(g string) bool { return g == GroupVR })
	binds := []Bind{
		{Action: ActCEUndo, MIDI: &MIDIKey{Status: 0x90, Data1: 10}},
		{Action: ActOBSRecord, MIDI: &MIDIKey{Status: 0x90, Data1: 11}},
	}
	d.FireMIDIMsg(binds, "", 0x90, 10, 100)
	d.FireMIDIMsg(binds, "", 0x90, 11, 100)
	if ui != 0 || vr != 1 {
		t.Fatalf("group filter: ui=%d vr=%d", ui, vr)
	}
}

func TestProfileFilter(t *testing.T) {
	d := NewDispatcher()
	ui, vr := 0, 0
	d.Register(ActCEUndo, func(string) { ui++ })
	d.Register(ActOBSRecord, func(string) { vr++ })
	// pause the "ddj" device profile for UI binds; VR binds unaffected (app.go carve-out)
	d.SetProfileFilter(func(group, bindPort string) bool {
		return group == GroupVR || bindPort != "ddj"
	})
	binds := []Bind{
		{Action: ActCEUndo, MIDI: &MIDIKey{Status: 0x90, Data1: 10, Port: "ddj"}},
		{Action: ActCEUndo, MIDI: &MIDIKey{Status: 0x90, Data1: 11}}, // any-device profile
		{Action: ActOBSRecord, MIDI: &MIDIKey{Status: 0x90, Data1: 12, Port: "ddj"}},
	}
	d.FireMIDIMsg(binds, "Pioneer DDJ-400", 0x90, 10, 100) // paused profile → inert
	d.FireMIDIMsg(binds, "Pioneer DDJ-400", 0x90, 11, 100) // global profile → fires
	d.FireMIDIMsg(binds, "Pioneer DDJ-400", 0x90, 12, 100) // VR group → fires despite pause
	if ui != 1 || vr != 1 {
		t.Fatalf("profile filter: ui=%d vr=%d", ui, vr)
	}
}

func TestFireFallbackForHoldAndStep(t *testing.T) {
	// VR slots / quick buttons call Fire (press-only): hold degrades to toggle, step to +1.
	d := NewDispatcher()
	var holds []bool
	steps := 0
	d.RegisterHold(ActCEAudition, func(_ string, down bool) { holds = append(holds, down) })
	d.RegisterStep(ActLibNav, func(_ string, delta int) { steps += delta })
	if !d.Fire(Bind{Action: ActCEAudition}) || !d.Fire(Bind{Action: ActCEAudition}) {
		t.Fatal("hold fallback should fire")
	}
	if len(holds) != 2 || !holds[0] || holds[1] {
		t.Fatalf("hold toggle fallback: %v", holds)
	}
	if !d.Fire(Bind{Action: ActLibNav}) || steps != 1 {
		t.Fatalf("step fallback: steps=%d", steps)
	}
}
