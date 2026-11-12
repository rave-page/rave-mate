package app

import (
	"net"
	"strings"
	"testing"

	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/peerlink"
	"rave.page/mate/internal/peers"
)

func TestParseYesNo(t *testing.T) {
	cases := []struct {
		in         string
		wantAccept bool
		wantOK     bool
	}{
		{"yes", true, true}, {"Y", true, true}, {"true", true, true}, {"1", true, true}, {"ok", true, true},
		{"no", false, true}, {"N", false, true}, {"false", false, true}, {"0", false, true},
		{"maybe", false, false}, {"", false, false}, {"  yes  ", true, true},
	}
	for _, c := range cases {
		a, ok := parseYesNo(c.in)
		if a != c.wantAccept || ok != c.wantOK {
			t.Errorf("parseYesNo(%q) = (%v,%v), want (%v,%v)", c.in, a, ok, c.wantAccept, c.wantOK)
		}
	}
}

func TestCutFirstField(t *testing.T) {
	cases := []struct{ in, first, rest string }{
		{"peer src", "peer", "src"},
		{"peer  a source name", "peer", "a source name"}, // source names keep spaces
		{"peer\tspout2pw.VRChat.VRCSender1", "peer", "spout2pw.VRChat.VRCSender1"},
		{"solo", "solo", ""},
		{"  ", "", ""},
	}
	for _, c := range cases {
		f, r := cutFirstField(c.in)
		if f != c.first || r != c.rest {
			t.Errorf("cutFirstField(%q) = (%q,%q), want (%q,%q)", c.in, f, r, c.first, c.rest)
		}
	}
}

func TestIsHostPort(t *testing.T) {
	yes := []string{"192.168.10.5:47631", "host.local:47631", "[::1]:47631"}
	no := []string{"abc123NodeID", "sus", "host:notaport", "noport", ":47631"}
	for _, s := range yes {
		if !isHostPort(s) {
			t.Errorf("isHostPort(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isHostPort(s) {
			t.Errorf("isHostPort(%q) = true, want false", s)
		}
	}
}

func TestMergePeersAndFormat(t *testing.T) {
	remembered := []peers.Peer{{NodeID: "nPaired", Nickname: "DeskPC", Trusted: true, LastAddress: "192.168.10.9:47631"}}
	discovered := []discovery.Peer{
		{NodeID: "nDisc", Name: "sus", Address: net.IPv4(192, 168, 10, 5), Port: 47631},
		{NodeID: "nPaired", Name: "DeskPC", Address: net.IPv4(192, 168, 10, 9), Port: 47631}, // also paired → stays paired
	}
	conns := []peerlink.ConnInfo{
		{NodeID: "nPending", Nickname: "LaptopPC", Address: "192.168.10.7:47631", Status: peerlink.StatusAwaitSAS, SAS: "483920", Inbound: true},
		{NodeID: "nPaired", Nickname: "DeskPC", Address: "192.168.10.9:47631", Status: peerlink.StatusConnected},
	}
	rows := mergePeers(remembered, discovered, conns)
	byID := map[string]peerRow{}
	for _, r := range rows {
		byID[r.NodeID] = r
	}
	if byID["nDisc"].State != "discovered" {
		t.Errorf("nDisc state = %q, want discovered", byID["nDisc"].State)
	}
	if byID["nPaired"].State != "connected" { // connection status wins over remembered-paired
		t.Errorf("nPaired state = %q, want connected", byID["nPaired"].State)
	}
	p := byID["nPending"]
	if p.State != "pairing-pending" || p.SAS != "483920" || p.Initiator != "remote" {
		t.Errorf("nPending = %+v, want pairing-pending/483920/remote", p)
	}
	out := formatPeerRows(rows)
	if !strings.Contains(out, "sas=483920") || !strings.Contains(out, "initiated=remote") {
		t.Errorf("formatted output missing SAS/initiator:\n%s", out)
	}
	if formatPeerRows(nil) == "" {
		t.Error("empty peers must still print a line")
	}
}

func TestResolveDiscovered(t *testing.T) {
	list := []discovery.Peer{
		{NodeID: "AAbbccdd", Name: "sus"},
		{NodeID: "EEffgghh", Name: "deskpc"},
	}
	if p, ok := resolveDiscovered(list, "AAbbccdd"); !ok || p.NodeID != "AAbbccdd" {
		t.Error("exact nodeID failed")
	}
	if p, ok := resolveDiscovered(list, "SUS"); !ok || p.NodeID != "AAbbccdd" {
		t.Error("case-insensitive exact name failed")
	}
	if p, ok := resolveDiscovered(list, "EEff"); !ok || p.NodeID != "EEffgghh" {
		t.Error("nodeID prefix failed")
	}
	if p, ok := resolveDiscovered(list, "desk"); !ok || p.NodeID != "EEffgghh" {
		t.Error("name substring failed")
	}
	if _, ok := resolveDiscovered(list, "nope"); ok {
		t.Error("no match must fail")
	}
}

func TestMatchNodeIDAmbiguous(t *testing.T) {
	cands := []nodeCand{{ID: "node-1", Name: "A"}, {ID: "node-2", Name: "B"}}
	if id, ok := matchNodeID(cands, "node-1"); !ok || id != "node-1" {
		t.Error("exact id failed")
	}
	if _, ok := matchNodeID(cands, "node-"); ok { // ambiguous prefix
		t.Error("ambiguous prefix must fail")
	}
	if id, ok := matchNodeID(cands, "node-2"); !ok || id != "node-2" {
		t.Error("exact id #2 failed")
	}
	if _, ok := matchNodeID(cands, ""); ok {
		t.Error("empty token must fail")
	}
}

func TestResolveRemoteSource(t *testing.T) {
	srcs := []mediaroute.RemoteSource{
		{Peer: "susNode", Desc: medialink.SourceDesc{ID: "pw:spout2pw.VRChat.VRCSender1", Name: "VRChat (spout2pw.VRChat.VRCSender1)", Kind: medialink.KindVideo, Width: 1920, Height: 1080}},
		{Peer: "susNode", Desc: medialink.SourceDesc{ID: "pw:testcard", Name: "testcard", Kind: medialink.KindVideo, Width: 1280, Height: 720}},
	}
	// Resolve the source by its exact ID.
	pid, sid, err := resolveRemoteSource(srcs, "susNode", "pw:spout2pw.VRChat.VRCSender1")
	if err != nil || pid != "susNode" || sid != "pw:spout2pw.VRChat.VRCSender1" {
		t.Fatalf("by id: %v %q %q", err, pid, sid)
	}
	// Resolve by case-insensitive exact name.
	_, sid, err = resolveRemoteSource(srcs, "susNode", "testcard")
	if err != nil || sid != "pw:testcard" {
		t.Fatalf("by name: %v %q", err, sid)
	}
	// Unknown peer / source error out.
	if _, _, err := resolveRemoteSource(srcs, "ghost", "testcard"); err == nil {
		t.Error("unknown peer must error")
	}
	if _, _, err := resolveRemoteSource(srcs, "susNode", "nope"); err == nil {
		t.Error("unknown source must error")
	}
}

func TestFormatRoutes(t *testing.T) {
	if got := formatRoutes(nil); got != "no active routes\n" {
		t.Errorf("empty routes = %q", got)
	}
	stats := []medialink.RouteStat{{
		Session: "s1", Peer: "susNodeLongID", Stream: 2, Direction: "recv",
		Encoder: "hevc_nvenc", Tier: 1, Transport: "udp", Encrypted: true,
		Frames: 1000, Bytes: 5_000_000, Keyframes: 10, RateBps: 8_000_000, WireFPS: 60,
		Pipe: &medialink.PipelineStats{Encoder: "hevc_nvenc", HWAccel: "cuda", OutFPS: 60, Dropped: 3, RateCapped: 2},
		UDP:  &medialink.UDPStats{LossPct: 0.5, RTTns: 4_000_000, NacksSent: 2, Retransmits: 1},
	}}
	out := formatRoutes(stats)
	for _, want := range []string{"recv", "udp/encrypted", "codec=hevc_nvenc", "hwaccel=cuda", "bitrate=8000 kbps", "udp loss=0.5%", "fps wire=60"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatRoutes missing %q in:\n%s", want, out)
		}
	}
}
