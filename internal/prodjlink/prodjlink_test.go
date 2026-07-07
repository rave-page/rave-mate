package prodjlink

import (
	"encoding/binary"
	"testing"
)

// buildStatus crafts a minimal CDJ status packet with the fields ParseStatus reads.
func buildStatus() []byte {
	p := make([]byte, 0xa4)
	copy(p, magic)
	p[0x0a] = statusType
	copy(p[0x0b:0x1f], []byte("CDJ-3000\x00\x00"))
	p[0x21] = 2 // player 2
	p[0x28] = 3 // source player
	p[0x29] = byte(SlotUSB)
	p[0x2a] = byte(TrackRekordbox)
	binary.BigEndian.PutUint32(p[0x2c:0x30], 12345) // track id
	p[0x89] = 0x40 | 0x08                           // playing + on-air, not master
	binary.BigEndian.PutUint16(p[0x92:0x94], 12800) // 128.00 BPM
	// pitch: +6% → value = 0x100000 * 1.06 ≈ 0x10F5C2
	pf := float64(0x100000) * 1.06
	pitch := uint32(pf)
	p[0x8d] = byte(pitch >> 16)
	p[0x8e] = byte(pitch >> 8)
	p[0x8f] = byte(pitch)
	binary.BigEndian.PutUint32(p[0xa0:0xa4], 3) // beat 3
	return p
}

func TestParseStatus(t *testing.T) {
	st, ok := ParseStatus(buildStatus())
	if !ok {
		t.Fatal("expected a valid status packet")
	}
	if st.Player != 2 {
		t.Errorf("player=%d", st.Player)
	}
	if st.Name != "CDJ-3000" {
		t.Errorf("name=%q", st.Name)
	}
	if st.TrackID != 12345 || st.Slot != SlotUSB || st.Type != TrackRekordbox {
		t.Errorf("track: id=%d slot=%v type=%v", st.TrackID, st.Slot, st.Type)
	}
	if !st.Playing || !st.OnAir || st.Master {
		t.Errorf("flags: playing=%v onair=%v master=%v", st.Playing, st.OnAir, st.Master)
	}
	if st.TrackBPM != 128 {
		t.Errorf("trackBPM=%v", st.TrackBPM)
	}
	// effective BPM ≈ 128 * 1.06 = 135.68 (within rounding of the pitch quantization)
	if st.EffectiveBPM < 135 || st.EffectiveBPM > 136.5 {
		t.Errorf("effectiveBPM=%v (pitch=%v)", st.EffectiveBPM, st.Pitch)
	}
	if st.Beat != 3 {
		t.Errorf("beat=%d", st.Beat)
	}
}

func TestParseStatusRejectsNonStatus(t *testing.T) {
	if _, ok := ParseStatus([]byte("not a dj link packet")); ok {
		t.Error("should reject a non-DJ-Link packet")
	}
	short := make([]byte, 0x40)
	copy(short, magic)
	short[0x0a] = statusType
	if _, ok := ParseStatus(short); ok {
		t.Error("should reject a too-short status packet")
	}
	// right size, wrong type byte
	wrong := make([]byte, 0xa4)
	copy(wrong, magic)
	wrong[0x0a] = 0x29 // beat packet, not status
	if _, ok := ParseStatus(wrong); ok {
		t.Error("should reject a non-status packet type")
	}
}
