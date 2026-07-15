package sacn

import (
	"bytes"
	"net"
	"testing"
)

func TestParseValidPacket(t *testing.T) {
	slots := make([]byte, 512)
	slots[0] = 255
	slots[11] = 128
	slots[511] = 7
	pkt := BuildDataPacket(42, 9, slots)

	u, seq, data, ok := ParseDataPacket(pkt)
	if !ok {
		t.Fatal("valid packet rejected")
	}
	if u != 42 {
		t.Fatalf("universe: want 42, got %d", u)
	}
	if seq != 9 {
		t.Fatalf("seq: want 9, got %d", seq)
	}
	if len(data) != 512 {
		t.Fatalf("slots: want 512, got %d", len(data))
	}
	if data[0] != 255 || data[11] != 128 || data[511] != 7 {
		t.Fatalf("slot values wrong: [0]=%d [11]=%d [511]=%d", data[0], data[11], data[511])
	}
}

func TestParseShortDMX(t *testing.T) {
	// a sender may transmit fewer than 512 slots; count drives the slot span.
	pkt := BuildDataPacket(1, 0, []byte{10, 20, 30})
	u, _, data, ok := ParseDataPacket(pkt)
	if !ok {
		t.Fatal("short packet rejected")
	}
	if u != 1 {
		t.Fatalf("universe: want 1, got %d", u)
	}
	if !bytes.Equal(data, []byte{10, 20, 30}) {
		t.Fatalf("slots wrong: %v", data)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	good := BuildDataPacket(1, 0, []byte{1, 2, 3})

	if _, _, _, ok := ParseDataPacket(good[:50]); ok {
		t.Fatal("truncated packet accepted")
	}

	badID := append([]byte(nil), good...)
	badID[4] = 'X' // corrupt ACN identifier
	if _, _, _, ok := ParseDataPacket(badID); ok {
		t.Fatal("bad ACN identifier accepted")
	}

	badRoot := append([]byte(nil), good...)
	badRoot[21] = 0x09 // corrupt root vector
	if _, _, _, ok := ParseDataPacket(badRoot); ok {
		t.Fatal("bad root vector accepted")
	}

	badFrame := append([]byte(nil), good...)
	badFrame[43] = 0x09 // corrupt framing vector
	if _, _, _, ok := ParseDataPacket(badFrame); ok {
		t.Fatal("bad framing vector accepted")
	}

	badDMP := append([]byte(nil), good...)
	badDMP[117] = 0x03 // wrong DMP vector
	if _, _, _, ok := ParseDataPacket(badDMP); ok {
		t.Fatal("bad DMP vector accepted")
	}

	badStart := append([]byte(nil), good...)
	badStart[125] = 0x01 // non-null start code (not dimmer data)
	if _, _, _, ok := ParseDataPacket(badStart); ok {
		t.Fatal("non-null start code accepted")
	}
}

func TestMulticastIP(t *testing.T) {
	if got := multicastIP(1); !got.Equal(net.IPv4(239, 255, 0, 1)) {
		t.Fatalf("universe 1: want 239.255.0.1, got %v", got)
	}
	if got := multicastIP(0x0102); !got.Equal(net.IPv4(239, 255, 1, 2)) {
		t.Fatalf("universe 258: want 239.255.1.2, got %v", got)
	}
}
