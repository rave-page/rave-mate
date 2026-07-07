package featurehost

import (
	"encoding/json"
	"math"
	"testing"

	"rave.page/mate/internal/netstats"
)

// Net snapshot survives the pipe including NaN gaps (JSON can't carry NaN - nil encodes them).
func TestVRNetWireNaNRoundTrip(t *testing.T) {
	in := netstats.Snapshot{
		PeerIn:     []float64{1, math.NaN(), 3},
		APIOut:     []float64{0.5},
		SessPeerIn: 42, SessAPIOut: 7, Span: 120,
		RTT: []netstats.RTTSeries{{NodeID: "n1", Label: "vr", Ms: []float64{math.NaN(), 12.5}, LatestMs: 12.5, Has: true}},
	}
	raw, err := json.Marshal(netToWire(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var w vrNetWire
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out := wireToNet(w)
	if len(out.PeerIn) != 3 || out.PeerIn[0] != 1 || !math.IsNaN(out.PeerIn[1]) || out.PeerIn[2] != 3 {
		t.Fatalf("PeerIn round-trip: %v", out.PeerIn)
	}
	if out.SessPeerIn != 42 || out.SessAPIOut != 7 || out.Span != 120 {
		t.Fatalf("totals round-trip: %+v", out)
	}
	r := out.RTT[0]
	if r.NodeID != "n1" || r.Label != "vr" || !math.IsNaN(r.Ms[0]) || r.Ms[1] != 12.5 || !r.Has {
		t.Fatalf("RTT round-trip: %+v", r)
	}
}

// A raw NaN in a series would fail json.Marshal - the wire encoder must never let one through.
func TestVRNetWireMarshalsAllNaN(t *testing.T) {
	w := netToWire(netstats.Snapshot{PeerOut: []float64{math.NaN(), math.NaN()}})
	if _, err := json.Marshal(w); err != nil {
		t.Fatalf("NaN leaked into wire form: %v", err)
	}
}

// Bus bridge frames keep Origin/Local so the child renders "(this PC)" and peer tags correctly.
func TestVRBusEventRoundTrip(t *testing.T) {
	in := vrBusEvent{Topic: "obscontrol.status", Origin: "node-a", Local: true, Data: json.RawMessage(`{"id":"x"}`)}
	raw, _ := json.Marshal(in)
	var out vrBusEvent
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Topic != in.Topic || out.Origin != in.Origin || !out.Local || string(out.Data) != `{"id":"x"}` {
		t.Fatalf("round-trip: %+v", out)
	}
}
