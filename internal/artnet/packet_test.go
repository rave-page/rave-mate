package artnet

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestBuildArtDmxGoldenBytes(t *testing.T) {
	data := []byte{1, 2, 3, 255}
	// Universe 0x0102: Net=0x01, SubUni=0x02.
	p := BuildArtDmx(0x0102, 7, 0, data)
	if len(p) != artDmxHeader+4 {
		t.Fatalf("len=%d want %d", len(p), artDmxHeader+4)
	}
	if !bytes.Equal(p[0:8], artID[:]) {
		t.Fatalf("magic=%v", p[0:8])
	}
	if op := binary.LittleEndian.Uint16(p[8:10]); op != opDmx {
		t.Fatalf("opcode=%#x want %#x", op, opDmx)
	}
	if p[10] != 0 || p[11] != protVer {
		t.Fatalf("protver=%d.%d", p[10], p[11])
	}
	if p[12] != 7 || p[13] != 0 {
		t.Fatalf("seq/phys=%d/%d", p[12], p[13])
	}
	if p[14] != 0x02 || p[15] != 0x01 {
		t.Fatalf("subuni/net=%#x/%#x", p[14], p[15])
	}
	if n := binary.BigEndian.Uint16(p[16:18]); n != 4 {
		t.Fatalf("length=%d want 4", n)
	}
	if !bytes.Equal(p[18:22], data) {
		t.Fatalf("payload=%v", p[18:22])
	}
}

func TestArtDmxRoundTrip(t *testing.T) {
	in := make([]byte, 512)
	for i := range in {
		in[i] = byte(i)
	}
	p := BuildArtDmx(0x7FFF, 200, 1, in)
	d, err := ParseArtDmx(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Universe != 0x7FFF || d.Sequence != 200 || d.Physical != 1 {
		t.Fatalf("hdr=%+v", d)
	}
	if !bytes.Equal(d.Data, in) {
		t.Fatalf("data mismatch")
	}
}

func TestBuildArtDmxOddLengthPadded(t *testing.T) {
	p := BuildArtDmx(0, 1, 0, []byte{9, 9, 9}) // 3 → padded to 4
	if n := binary.BigEndian.Uint16(p[16:18]); n != 4 {
		t.Fatalf("length=%d want 4 (even-padded)", n)
	}
}

func TestParseArtDmxEdgeCases(t *testing.T) {
	if _, err := ParseArtDmx([]byte{1, 2, 3}); err != ErrNotArtNet {
		t.Fatalf("short non-artnet: %v", err)
	}
	// Right magic, wrong opcode.
	poll := make([]byte, 14)
	copy(poll, artID[:])
	binary.LittleEndian.PutUint16(poll[8:10], opPoll)
	if _, err := ParseArtDmx(poll); err != ErrNotArtNet {
		t.Fatalf("poll parsed as dmx: %v", err)
	}
	// Truncated payload (declares 10 slots, carries none).
	bad := BuildArtDmx(1, 1, 0, make([]byte, 10))
	bad = bad[:artDmxHeader] // strip payload
	if _, err := ParseArtDmx(bad); err == nil {
		t.Fatal("truncated payload accepted")
	}
	// Over-length claim.
	over := make([]byte, artDmxHeader)
	copy(over, artID[:])
	binary.LittleEndian.PutUint16(over[8:10], opDmx)
	binary.BigEndian.PutUint16(over[16:18], 513)
	if _, err := ParseArtDmx(over); err == nil {
		t.Fatal("length>512 accepted")
	}
}

func TestArtPollAndReply(t *testing.T) {
	poll := make([]byte, 14)
	copy(poll, artID[:])
	binary.LittleEndian.PutUint16(poll[8:10], opPoll)
	if !IsArtPoll(poll) {
		t.Fatal("IsArtPoll false")
	}
	r := BuildArtPollReply(net.IPv4(192, 168, 1, 42), "rave-mate", "rave-mate VRSL", []uint16{5})
	if len(r) != 239 {
		t.Fatalf("reply len=%d want 239", len(r))
	}
	if op := binary.LittleEndian.Uint16(r[8:10]); op != opPollReply {
		t.Fatalf("reply opcode=%#x", op)
	}
	if !bytes.Equal(r[10:14], []byte{192, 168, 1, 42}) {
		t.Fatalf("reply ip=%v", r[10:14])
	}
	if binary.LittleEndian.Uint16(r[14:16]) != Port {
		t.Fatalf("reply port=%d", binary.LittleEndian.Uint16(r[14:16]))
	}
	if got := string(bytes.TrimRight(r[26:44], "\x00")); got != "rave-mate" {
		t.Fatalf("shortname=%q", got)
	}
	if binary.BigEndian.Uint16(r[172:174]) != 1 {
		t.Fatalf("numports=%d", binary.BigEndian.Uint16(r[172:174]))
	}
}
