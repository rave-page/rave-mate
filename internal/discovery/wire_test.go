package discovery

import (
	"net"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

func testSvc() Service {
	return Service{NodeID: "peerNodeABC", Name: "Laptop", Port: 47620, ProtoVersion: 1, IdentityFP: "fp123"}
}

func TestQueryRoundTrip(t *testing.T) {
	msg, err := parseMessage(encodeQuery(serviceType))
	if err != nil {
		t.Fatal(err)
	}
	if msg.response {
		t.Error("query parsed as response")
	}
	if len(msg.questions) != 1 || msg.questions[0].name != serviceType || msg.questions[0].qtype != typePTR {
		t.Fatalf("bad question: %+v", msg.questions)
	}
}

func TestAnnounceRoundTrip(t *testing.T) {
	svc := testSvc()
	ip := net.IPv4(192, 168, 1, 50)
	msg, err := parseMessage(encodeAnnounce(svc, []net.IP{ip}, announceTTL))
	if err != nil {
		t.Fatal(err)
	}
	if !msg.response {
		t.Error("announce not flagged response")
	}
	var gotPTR, gotSRV, gotTXT, gotA bool
	txt := map[string]string{}
	for _, r := range msg.records {
		switch r.rtype {
		case typePTR:
			if r.name == serviceType && r.ptr == svc.instanceName() {
				gotPTR = true
			}
		case typeSRV:
			if r.srvPort == uint16(svc.Port) && r.srvTgt == svc.hostName() {
				gotSRV = true
			}
		case typeTXT:
			gotTXT = true
			for _, kv := range r.txt {
				if i := indexByte(kv, '='); i >= 0 {
					txt[kv[:i]] = kv[i+1:]
				}
			}
		case typeA:
			if r.a.Equal(ip) {
				gotA = true
			}
		}
	}
	if !gotPTR || !gotSRV || !gotTXT || !gotA {
		t.Fatalf("missing record: ptr=%v srv=%v txt=%v a=%v", gotPTR, gotSRV, gotTXT, gotA)
	}
	if txt["nid"] != svc.NodeID || txt["port"] != "47620" || txt["pv"] != "1" || txt["fp"] != "fp123" {
		t.Fatalf("bad TXT: %+v", txt)
	}
}

func TestGoodbyeTTLZero(t *testing.T) {
	msg, err := parseMessage(encodeGoodbye(testSvc()))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.records) != 1 || msg.records[0].rtype != typePTR || msg.records[0].ttl != 0 {
		t.Fatalf("goodbye not a TTL-0 PTR: %+v", msg.records)
	}
}

// TestCompressionPointer hand-builds a message whose RR name is a pointer to an earlier
// name, exercising readName's pointer following.
func TestCompressionPointer(t *testing.T) {
	// Header (12) + question name "local." at offset 12 + qtype/qclass, then a PTR RR whose
	// name is a pointer back to offset 12.
	buf := make([]byte, 12)
	putHeader(buf, 0, 1, 1)
	nameOff := len(buf)
	buf = append(buf, encodeName("local")...)
	buf = appendU16(buf, typePTR)
	buf = appendU16(buf, classIN)
	// RR: name = pointer to nameOff
	buf = append(buf, 0xC0, byte(nameOff))
	buf = appendU16(buf, typePTR)
	buf = appendU16(buf, classIN)
	buf = appendU32(buf, 60)
	rd := encodeName("x.local")
	buf = appendU16(buf, uint16(len(rd)))
	buf = append(buf, rd...)

	msg, err := parseMessage(buf)
	if err != nil {
		t.Fatalf("parse with pointer: %v", err)
	}
	if len(msg.records) != 1 || msg.records[0].name != "local." {
		t.Fatalf("pointer name not resolved: %+v", msg.records)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := parseMessage([]byte{1, 2, 3}); err == nil {
		t.Error("expected error for short message")
	}
}

func newTestDiscovery(self string, clk func() time.Time) *Discovery {
	d := New(Service{NodeID: self, ProtoVersion: 1}, logbus.New(16))
	d.clock = clk
	return d
}

func TestHandleUpsertAndSelfFilter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	d := newTestDiscovery("self", func() time.Time { return now })

	// A peer announce → discovered.
	peer := testSvc()
	pm, _ := parseMessage(encodeAnnounce(peer, []net.IP{net.IPv4(10, 0, 0, 9)}, announceTTL))
	d.handle(pm, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 9)})
	peers := d.Peers()
	if len(peers) != 1 || peers[0].NodeID != peer.NodeID || peers[0].Port != peer.Port {
		t.Fatalf("peer not discovered: %+v", peers)
	}
	if !peers[0].Address.Equal(net.IPv4(10, 0, 0, 9)) {
		t.Errorf("wrong source address: %v", peers[0].Address)
	}

	// Our own announce echoed back → ignored.
	self := Service{NodeID: "self", Port: 1, ProtoVersion: 1}
	sm, _ := parseMessage(encodeAnnounce(self, []net.IP{net.IPv4(10, 0, 0, 1)}, announceTTL))
	d.handle(sm, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1)})
	if len(d.Peers()) != 1 {
		t.Error("self-announce was not filtered")
	}
}

func TestExpire(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clk := func() time.Time { return now }
	d := newTestDiscovery("self", clk)

	peer := testSvc()
	pm, _ := parseMessage(encodeAnnounce(peer, []net.IP{net.IPv4(10, 0, 0, 9)}, announceTTL))
	d.handle(pm, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 9)})
	if len(d.Peers()) != 1 {
		t.Fatal("setup: peer missing")
	}
	now = now.Add(peerTTL + time.Second)
	d.expire()
	if len(d.Peers()) != 0 {
		t.Error("stale peer not expired")
	}
}

func TestSubscribeDeliversCurrent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	d := newTestDiscovery("self", func() time.Time { return now })
	peer := testSvc()
	pm, _ := parseMessage(encodeAnnounce(peer, []net.IP{net.IPv4(10, 0, 0, 9)}, announceTTL))
	d.handle(pm, &net.UDPAddr{IP: net.IPv4(10, 0, 0, 9)})

	ch, unsub := d.Subscribe()
	defer unsub()
	select {
	case got := <-ch:
		if len(got) != 1 {
			t.Fatalf("subscribe initial snapshot wrong: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no initial snapshot")
	}
}
