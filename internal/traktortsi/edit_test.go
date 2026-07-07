package traktortsi

import "testing"

// makeDEVI builds a single full DEVI frame (name + ports) for editor tests.
func makeDEVI(name, in, out string) []byte {
	ddpt := putString(putString(nil, in), out)
	ddat := putFrame(nil, "DDPT", ddpt)
	payload := putFrame(putString(nil, name), "DDAT", ddat)
	return putFrame(nil, "DEVI", payload)
}

func TestAddRemoveDevice(t *testing.T) {
	blob := buildSyntheticDIOM([]Device{
		{Name: "Traktor.Kontrol S4 MK3", InPort: "5A26", OutPort: "5A26"},
	})

	// Add the RavePage mapping.
	blob, err := AddDevice(blob, makeDEVI("RavePage State", "None", "LoopBe Internal MIDI"))
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	names, _ := DeviceNames(blob)
	if len(names) != 2 || names[1] != "RavePage State" {
		t.Fatalf("after add: %v", names)
	}
	if ok, _ := HasDevice(blob, "RavePage State"); !ok {
		t.Fatal("HasDevice should find RavePage State")
	}

	// Re-adding the same name replaces (idempotent install), doesn't duplicate.
	blob, _ = AddDevice(blob, makeDEVI("RavePage State", "None", "loopMIDI Port"))
	names, _ = DeviceNames(blob)
	if len(names) != 2 {
		t.Fatalf("re-add should replace, got %v", names)
	}
	devs, _ := ParseDevices(blob)
	if devs[1].OutPort != "loopMIDI Port" {
		t.Fatalf("re-add should update port, got %q", devs[1].OutPort)
	}

	// Remove it → back to one device, and the survivor is intact.
	blob, err = RemoveDevice(blob, "RavePage State")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	names, _ = DeviceNames(blob)
	if len(names) != 1 || names[0] != "Traktor.Kontrol S4 MK3" {
		t.Fatalf("after remove: %v", names)
	}

	// Removing an absent device is a no-op.
	if out, _ := RemoveDevice(blob, "nope"); len(out) != len(blob) {
		t.Fatal("removing absent device should be a no-op")
	}
}
