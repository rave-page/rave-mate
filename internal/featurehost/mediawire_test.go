package featurehost

import (
	"encoding/json"
	"testing"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/webcam"
)

// TestMediaWireRoundTrip guards that the media-child payloads survive JSON encode/decode with
// their fields intact (the boundary is newline-JSON - a dropped tag silently loses state).
func TestMediaWireRoundTrip(t *testing.T) {
	in := mediaInit{
		Self:     "node-A",
		Secrets:  []peerSecret{{Node: "peer-B", Secret: []byte{1, 2, 3}}},
		Encoders: []string{"libx264", "hevc_amf"},
		SyncPeer: "peer-B",
	}
	var out mediaInit
	if err := json.Unmarshal(mustJSON(t, in), &out); err != nil {
		t.Fatal(err)
	}
	if out.Self != "node-A" || out.SyncPeer != "peer-B" || len(out.Secrets) != 1 ||
		out.Secrets[0].Node != "peer-B" || len(out.Secrets[0].Secret) != 3 {
		t.Fatalf("mediaInit round-trip lost fields: %+v", out)
	}

	tel := mediaTelemetry{
		Routes:   []medialink.RouteStat{{Session: "s1", Peer: "peer-B", Direction: "recv", Frames: 42}},
		Clock:    medialink.ClockQuality{Locked: true, OffsetNs: 1500},
		Encoders: []string{"libx264"},
		Cams:     []webcam.Instance{{Node: "node-A", Local: true}},
	}
	var telOut mediaTelemetry
	if err := json.Unmarshal(mustJSON(t, tel), &telOut); err != nil {
		t.Fatal(err)
	}
	if len(telOut.Routes) != 1 || telOut.Routes[0].Frames != 42 || !telOut.Clock.Locked ||
		telOut.Clock.OffsetNs != 1500 || len(telOut.Cams) != 1 || !telOut.Cams[0].Local {
		t.Fatalf("mediaTelemetry round-trip lost fields: %+v", telOut)
	}

	off := clockOffsetEvent{NowNs: 123456789, Tier: "software", Locked: true}
	var offOut clockOffsetEvent
	if err := json.Unmarshal(mustJSON(t, off), &offOut); err != nil {
		t.Fatal(err)
	}
	if offOut != off {
		t.Fatalf("clockOffsetEvent round-trip: got %+v want %+v", offOut, off)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
