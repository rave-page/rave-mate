package mediapipe

import (
	"bytes"
	"testing"
)

// Synthetic Annex-B builders. sc4 = 4-byte start code.
var sc4 = []byte{0, 0, 0, 1}

func h264NAL(typ byte, payload ...byte) []byte {
	return append(append(append([]byte{}, sc4...), typ&0x1f), payload...)
}

func hevcNAL(typ byte, payload ...byte) []byte {
	return append(append(append([]byte{}, sc4...), typ<<1, 0x01), payload...)
}

type gotAU struct {
	au   []byte
	info auInfo
}

func collectAUs(dst *[]gotAU) auEmit {
	return func(au []byte, info auInfo) { *dst = append(*dst, gotAU{au: au, info: info}) }
}

func TestAUSplitterH264(t *testing.T) {
	var got []gotAU
	s := newAUSplitter(false, collectAUs(&got))

	var stream []byte
	// AU1: AUD + SPS + PPS + IDR (keyframe). AU2: AUD + non-IDR slice. AU3 pending.
	au1 := bytes.Join([][]byte{h264NAL(9, 0xF0), h264NAL(7, 0x42), h264NAL(8, 0xCE), h264NAL(5, 0x11, 0x22)}, nil)
	au2 := bytes.Join([][]byte{h264NAL(9, 0xF0), h264NAL(1, 0x33)}, nil)
	au3head := h264NAL(9, 0xF0)
	stream = append(append(append(stream, au1...), au2...), au3head...)

	// Feed in torn chunks to exercise buffering.
	for i := 0; i < len(stream); i += 5 {
		end := i + 5
		if end > len(stream) {
			end = len(stream)
		}
		if _, err := s.Write(stream[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 {
		t.Fatalf("emitted %d AUs, want 2 (third has no terminator yet)", len(got))
	}
	if !bytes.Equal(got[0].au, au1) || !got[0].info.Keyframe || got[0].info.Config {
		t.Fatalf("AU1: %+v", got[0].info)
	}
	if !bytes.Equal(got[1].au, au2) || got[1].info.Keyframe {
		t.Fatalf("AU2: %+v", got[1].info)
	}
}

func TestAUSplitterHEVC(t *testing.T) {
	var got []gotAU
	s := newAUSplitter(true, collectAUs(&got))
	// AU1: AUD(35) + VPS(32) + SPS(33) + PPS(34) + IDR_W_RADL(19). AU2: AUD + TRAIL_R(1).
	au1 := bytes.Join([][]byte{hevcNAL(35, 0x50), hevcNAL(32), hevcNAL(33), hevcNAL(34), hevcNAL(19, 0xAB)}, nil)
	au2 := bytes.Join([][]byte{hevcNAL(35, 0x50), hevcNAL(1, 0xCD)}, nil)
	if _, err := s.Write(append(append(append([]byte{}, au1...), au2...), hevcNAL(35)...)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("emitted %d, want 2", len(got))
	}
	if !got[0].info.Keyframe || got[1].info.Keyframe {
		t.Fatalf("keyframe flags: %+v %+v", got[0].info, got[1].info)
	}
	if !bytes.Equal(got[0].au, au1) {
		t.Fatal("AU1 bytes mangled")
	}
}

func TestAUSplitterMidStreamJoin(t *testing.T) {
	var got []gotAU
	s := newAUSplitter(false, collectAUs(&got))
	// Garbage tail of a previous AU before the first AUD must be discarded.
	garbage := []byte{0x13, 0x37, 0x00, 0x00}
	au1 := bytes.Join([][]byte{h264NAL(9, 0xF0), h264NAL(5, 0x01)}, nil)
	if _, err := s.Write(append(append(garbage, au1...), h264NAL(9, 0xF0)...)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !bytes.Equal(got[0].au, au1) {
		t.Fatalf("join: %d AUs", len(got))
	}
}

func TestAUSplitterConfigOnly(t *testing.T) {
	var got []gotAU
	s := newAUSplitter(false, collectAUs(&got))
	au := bytes.Join([][]byte{h264NAL(9, 0xF0), h264NAL(7), h264NAL(8)}, nil)
	if _, err := s.Write(append(au, h264NAL(9, 0xF0)...)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].info.Config || got[0].info.Keyframe {
		t.Fatalf("config AU: %+v", got)
	}
}

func TestJPEGSplitter(t *testing.T) {
	var got []gotAU
	s := newJPEGSplitter(collectAUs(&got))
	pic1 := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x01, 0x02, 0xFF, 0xD9}
	pic2 := []byte{0xFF, 0xD8, 0xAA, 0xFF, 0xD9}
	stream := append(append([]byte{}, pic1...), pic2...)
	// Torn feed incl. split markers.
	for i := 0; i < len(stream); i += 3 {
		end := i + 3
		if end > len(stream) {
			end = len(stream)
		}
		if _, err := s.Write(stream[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || !bytes.Equal(got[0].au, pic1) || !bytes.Equal(got[1].au, pic2) {
		t.Fatalf("jpeg split: %d", len(got))
	}
	for _, g := range got {
		if !g.info.Keyframe {
			t.Fatal("every JPEG picture is a keyframe")
		}
	}
}
