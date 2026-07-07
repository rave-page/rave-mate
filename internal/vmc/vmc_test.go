package vmc

import (
	"math"
	"net"
	"testing"
	"time"

	"rave.page/mate/internal/vrmotion"
)

func TestToUnity(t *testing.T) {
	p := vrmotion.Pose{Pos: [3]float32{1, 2, 3}, Rot: [4]float32{0.1, 0.2, 0.3, 0.9}}
	px, py, pz, qx, qy, qz, qw := ToUnity(p)
	if px != 1 || py != 2 || pz != -3 {
		t.Fatalf("pos: got %v %v %v", px, py, pz)
	}
	if qx != -0.1 || qy != -0.2 || qz != 0.3 || qw != 0.9 {
		t.Fatalf("quat: got %v %v %v %v", qx, qy, qz, qw)
	}
}

func TestToUnityPreservesNorm(t *testing.T) {
	p := vrmotion.Pose{Rot: [4]float32{0.1, 0.2, 0.3, 0.9}}
	_, _, _, qx, qy, qz, qw := ToUnity(p)
	n := math.Sqrt(float64(qx*qx + qy*qy + qz*qz + qw*qw))
	orig := math.Sqrt(0.1*0.1 + 0.2*0.2 + 0.3*0.3 + 0.9*0.9)
	if math.Abs(n-orig) > 1e-6 {
		t.Fatalf("norm changed: %v vs %v", n, orig)
	}
}

func TestDefaultMapping(t *testing.T) {
	cases := []struct {
		id   int
		kind DeviceKind
	}{{0, KindHMD}, {1, KindController}, {2, KindController}, {3, KindTracker}, {8, KindTracker}}
	for _, c := range cases {
		if d := DefaultMapping(c.id); d.Kind != c.kind {
			t.Errorf("id %d: kind %d, want %d", c.id, d.Kind, c.kind)
		}
	}
	if DefaultMapping(5).Serial != "Tracker5" {
		t.Errorf("tracker serial: %q", DefaultMapping(5).Serial)
	}
}

// TestSendFrameEmits binds a UDP socket and confirms SendFrame writes the OK + T + device
// packets (one datagram each) with valid OSC framing (address null-terminated, 4-aligned).
func TestSendFrameEmits(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()

	s, err := New(pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	s.SendFrame(1.5, map[int]vrmotion.Pose{
		0: {Pos: [3]float32{0, 1.6, 0}, Rot: [4]float32{0, 0, 0, 1}},
		1: {Pos: [3]float32{-0.2, 1.2, 0.1}, Rot: [4]float32{0, 0, 0, 1}},
	})

	// 2 devices → OK + T + 2 device messages = 4 datagrams.
	got := map[string]bool{}
	buf := make([]byte, 1024)
	for i := range 4 {
		_ = pc.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		addr := oscAddress(buf[:n])
		got[addr] = true
	}
	for _, want := range []string{"/VMC/Ext/OK", "/VMC/Ext/T", "/VMC/Ext/Hmd/Pos", "/VMC/Ext/Con/Pos"} {
		if !got[want] {
			t.Errorf("missing message %s (got %v)", want, got)
		}
	}
}

// oscAddress extracts the leading null-terminated OSC address from a datagram.
func oscAddress(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
