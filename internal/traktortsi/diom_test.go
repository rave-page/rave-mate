package traktortsi

import (
	"encoding/binary"
	"testing"
)

// buildSyntheticDIOM assembles a minimal valid DIOM blob from devices (name/comment/ports
// only - enough to exercise framing + the parser). Mirrors the on-disk structure.
func buildSyntheticDIOM(devs []Device) []byte {
	var deviAll []byte
	for _, d := range devs {
		ddpt := putString(putString(nil, d.InPort), d.OutPort)
		ddic := putString(nil, d.Comment)
		ddat := putFrame(putFrame(nil, "DDIC", ddic), "DDPT", ddpt)
		deviPayload := putFrame(putString(nil, d.Name), "DDAT", ddat)
		deviAll = putFrame(deviAll, "DEVI", deviPayload)
	}
	devsPayload := binary.BigEndian.AppendUint32(nil, uint32(len(devs)))
	devsPayload = append(devsPayload, deviAll...)
	dioi := putFrame(nil, "DIOI", []byte{0, 0, 0, 1})
	dueInner := putFrame(dioi, "DEVS", devsPayload)
	return putFrame(nil, "DIOM", dueInner)
}

func TestParseDevices(t *testing.T) {
	want := []Device{
		{Name: "Traktor.Kontrol S4 MK3", Comment: "Tekken v1.3", InPort: "5A263432", OutPort: "5A263432"},
		{Name: "Generic MIDI", Comment: "", InPort: "None", OutPort: "LoopBe Internal MIDI"},
	}
	blob := buildSyntheticDIOM(want)

	got, err := ParseDevices(blob)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d devices, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Comment != want[i].Comment ||
			got[i].InPort != want[i].InPort || got[i].OutPort != want[i].OutPort {
			t.Errorf("device %d = %+v, want %+v", i, got[i], want[i])
		}
		if len(got[i].raw) == 0 {
			t.Errorf("device %d retained no raw DEVI bytes", i)
		}
	}
}

// TestRoundTripUTF16: non-ASCII names survive the UTF-16BE string codec.
func TestRoundTripUTF16(t *testing.T) {
	blob := buildSyntheticDIOM([]Device{{Name: "Tékken ♥ MIDI", OutPort: "LoopBe"}})
	got, err := ParseDevices(blob)
	if err != nil || len(got) != 1 || got[0].Name != "Tékken ♥ MIDI" || got[0].OutPort != "LoopBe" {
		t.Fatalf("round-trip = %+v err=%v", got, err)
	}
}
