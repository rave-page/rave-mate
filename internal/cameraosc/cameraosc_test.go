package cameraosc

import (
	"net"
	"testing"
	"time"

	"rave.page/mate/internal/osc"
)

func TestApplyOnlySetFields(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()

	c, err := osc.New(pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Only focal + aperture set → exactly 2 datagrams.
	if err := apply(c, Preset{FocalDistance: f(3), Aperture: f(12)}); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	buf := make([]byte, 512)
	for range 2 {
		_ = pc.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		got[oscAddr(buf[:n])] = true
	}
	// no third datagram
	_ = pc.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if n, _, err := pc.ReadFrom(buf); err == nil {
		t.Fatalf("unexpected extra datagram: %s", oscAddr(buf[:n]))
	}
	for _, want := range []string{"/usercamera/FocalDistance", "/usercamera/Aperture"} {
		if !got[want] {
			t.Errorf("missing %s (got %v)", want, got)
		}
	}
}

func TestBuiltinPresetsValid(t *testing.T) {
	ps := BuiltinPresets()
	if len(ps) == 0 {
		t.Fatal("no builtin presets")
	}
	for _, p := range ps {
		if p.Name == "" {
			t.Error("preset with empty name")
		}
	}
	if _, ok := PresetByName(ps, ps[0].Name); !ok {
		t.Error("PresetByName failed to find first builtin")
	}
	if _, ok := PresetByName(ps, "nope"); ok {
		t.Error("PresetByName found a nonexistent preset")
	}
}

func oscAddr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
