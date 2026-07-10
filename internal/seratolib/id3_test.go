package seratolib

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"rave.page/mate/internal/musiclib"
)

// --- synthetic-file builders ---

// frame builds one raw ID3 frame for the given tag major.
func frame(t *testing.T, major byte, id string, body []byte) []byte {
	t.Helper()
	raw := []byte(id)
	if major == 4 {
		if len(body) >= 1<<28 {
			t.Fatal("frame too big")
		}
		raw = appendSyncsafe(raw, len(body))
	} else {
		raw = binary.BigEndian.AppendUint32(raw, uint32(len(body)))
	}
	raw = append(raw, 0x00, 0x00)
	return append(raw, body...)
}

// textFrame builds a latin1 text frame body.
func textFrame(t *testing.T, major byte, id, text string) []byte {
	return frame(t, major, id, append([]byte{0x00}, text...))
}

// geobFrame builds a GEOB frame with latin1 encoding and the given description + payload.
func geobFrame(t *testing.T, major byte, desc string, payload []byte) []byte {
	body := []byte{0x00}
	body = append(body, octetStream...)
	body = append(body, 0x00, 0x00)
	body = append(body, desc...)
	body = append(body, 0x00)
	body = append(body, payload...)
	return frame(t, major, "GEOB", body)
}

// tagBytes assembles a full ID3 tag (header + frames + padding), optionally unsyncing (v2.3).
func tagBytes(t *testing.T, major byte, flags byte, padding int, frames ...[]byte) []byte {
	t.Helper()
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	body = append(body, make([]byte, padding)...)
	if flags&0x80 != 0 { // apply v2.3 tag-level unsynchronisation
		var u []byte
		for i := 0; i < len(body); i++ {
			u = append(u, body[i])
			if body[i] == 0xFF && i+1 < len(body) && (body[i+1] == 0x00 || body[i+1]&0xE0 == 0xE0) {
				u = append(u, 0x00)
			}
		}
		body = u
	}
	out := []byte{'I', 'D', '3', major, 0x00, flags}
	out = appendSyncsafe(out, len(body))
	return append(out, body...)
}

// audio is a fake MPEG stream incl. sync bytes that would break a naive splicer.
var audio = []byte{0xFF, 0xFB, 0x90, 0x00, 0x11, 0x22, 0xFF, 0x00, 0x33, 0xFF, 0xFB, 0x44}

func mp3File(t *testing.T, tag []byte) []byte {
	return append(append([]byte{}, tag...), audio...)
}

// --- tests ---

func TestID3WriteInsertAndPreserve(t *testing.T) {
	for _, major := range []byte{3, 4} {
		tit := textFrame(t, major, "TIT2", "Test Title")
		bin := frame(t, major, "APIC", []byte{0x00, 0xFF, 0x00, 0xFF, 0xFB, 0x01})
		other := geobFrame(t, major, "Serato Markers2", []byte{0xAA, 0xBB})
		orig := mp3File(t, tagBytes(t, major, 0, 64, tit, bin, other))

		payload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 125, BPM: 174}})
		if err != nil {
			t.Fatal(err)
		}
		built, err := spliceID3Beatgrid(orig, payload)
		if err != nil {
			t.Fatalf("v2.%d splice: %v", major, err)
		}
		if err := verifySplice("x.mp3", orig, built, []musiclib.GridMarker{{PositionMs: 125, BPM: 174}}); err != nil {
			t.Fatalf("v2.%d verify: %v", major, err)
		}
		// Grid reads back.
		got, found, err := readID3Beatgrid(built)
		if err != nil || !found {
			t.Fatalf("v2.%d read back: found=%v err=%v", major, found, err)
		}
		markers, err := decodeBeatgrid(got)
		if err != nil || len(markers) != 1 {
			t.Fatalf("v2.%d decode: %v %v", major, markers, err)
		}
		if math.Abs(markers[0].BPM-174) > 1e-4 || math.Abs(markers[0].PositionMs-125) > 1e-3 {
			t.Fatalf("v2.%d marker %+v", major, markers[0])
		}
		// Other frames byte-exact.
		bt, _, err := parseID3(built)
		if err != nil {
			t.Fatal(err)
		}
		var kept [][]byte
		for _, f := range bt.frames {
			if !(f.id == "GEOB" && geobDescription(bt.major, f) == beatgridDesc) {
				kept = append(kept, f.raw)
			}
		}
		if len(kept) != 3 || !bytes.Equal(kept[0], tit) || !bytes.Equal(kept[1], bin) || !bytes.Equal(kept[2], other) {
			t.Fatalf("v2.%d frames not preserved byte-exact", major)
		}
		// Audio byte-exact + padding preserved.
		if !bytes.Equal(built[len(built)-len(audio):], audio) {
			t.Fatalf("v2.%d audio changed", major)
		}
		if bt.padding != 64 {
			t.Fatalf("v2.%d padding %d != 64", major, bt.padding)
		}
	}
}

func TestID3ReplaceExistingBeatgrid(t *testing.T) {
	oldPayload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 500, BPM: 120}})
	if err != nil {
		t.Fatal(err)
	}
	tit := textFrame(t, 3, "TIT2", "T")
	orig := mp3File(t, tagBytes(t, 3, 0, 16, geobFrame(t, 3, beatgridDesc, oldPayload), tit))

	newPayload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 250, BPM: 140}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := spliceID3Beatgrid(orig, newPayload)
	if err != nil {
		t.Fatal(err)
	}
	bt, _, err := parseID3(built)
	if err != nil {
		t.Fatal(err)
	}
	geobs := 0
	for _, f := range bt.frames {
		if f.id == "GEOB" && geobDescription(3, f) == beatgridDesc {
			geobs++
		}
	}
	if geobs != 1 {
		t.Fatalf("want exactly 1 beatgrid GEOB, got %d", geobs)
	}
	got, found, err := readID3Beatgrid(built)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	m, err := decodeBeatgrid(got)
	if err != nil || math.Abs(m[0].BPM-140) > 1e-4 {
		t.Fatalf("replace didn't take: %v %v", m, err)
	}
}

func TestID3NoTagCreatesFresh(t *testing.T) {
	orig := append([]byte{}, audio...)
	payload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 0, BPM: 128}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := spliceID3Beatgrid(orig, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(built[len(built)-len(audio):], audio) {
		t.Fatal("audio changed")
	}
	if _, found, err := readID3Beatgrid(built); err != nil || !found {
		t.Fatal(found, err)
	}
}

func TestID3V23Unsync(t *testing.T) {
	// Body contains 0xFF 0xFB (false sync) -> unsynced on disk; splicer must resync + still
	// carry the frame content intact (tag is rewritten without unsync, so compare semantics).
	bin := frame(t, 3, "PRIV", []byte{0xFF, 0xFB, 0x00, 0xFF})
	orig := mp3File(t, tagBytes(t, 3, 0x80, 8, bin))
	payload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 10, BPM: 100}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := spliceID3Beatgrid(orig, payload)
	if err != nil {
		t.Fatal(err)
	}
	bt, _, err := parseID3(built)
	if err != nil {
		t.Fatal(err)
	}
	if bt.flags&0x80 != 0 {
		t.Fatal("unsync flag not cleared")
	}
	var priv []byte
	for _, f := range bt.frames {
		if f.id == "PRIV" {
			priv = f.raw
		}
	}
	if !bytes.Equal(priv, bin) {
		t.Fatalf("PRIV frame content lost across resync: %x", priv)
	}
	if !bytes.Equal(built[len(built)-len(audio):], audio) {
		t.Fatal("audio changed")
	}
}

func TestID3Refusals(t *testing.T) {
	// v2.2
	v22 := append([]byte{'I', 'D', '3', 2, 0, 0}, appendSyncsafe(nil, 0)...)
	if _, err := spliceID3Beatgrid(mp3File(t, v22), []byte{0x01, 0x00}); err == nil {
		t.Fatal("v2.2 not refused")
	}
	// v2.4 tag-level unsync
	v24u := append([]byte{'I', 'D', '3', 4, 0, 0x80}, appendSyncsafe(nil, 0)...)
	if _, err := spliceID3Beatgrid(mp3File(t, v24u), []byte{0x01, 0x00}); err == nil {
		t.Fatal("v2.4 tag unsync not refused")
	}
	// corrupt frame ID
	bad := tagBytes(t, 3, 0, 0, frame(t, 3, "TIT2", []byte{0}))
	bad[10] = 0x01 // clobber the frame ID
	if _, err := spliceID3Beatgrid(mp3File(t, bad), []byte{0x01, 0x00}); err == nil {
		t.Fatal("corrupt frame ID not refused")
	}
}

func TestBeatgridCodecMultiMarker(t *testing.T) {
	in := []musiclib.GridMarker{
		{PositionMs: 0, BPM: 120},    // 4 beats to next (2s at 120)
		{PositionMs: 2000, BPM: 150}, // terminal
	}
	payload, err := encodeBeatgrid(in)
	if err != nil {
		t.Fatal(err)
	}
	// Layout: 01 00 | count 2 | non-terminal pos+beats | terminal pos+bpm | footer
	if len(payload) != 2+4+8+8+1 || payload[0] != 0x01 || binary.BigEndian.Uint32(payload[2:6]) != 2 {
		t.Fatalf("bad layout: % x", payload)
	}
	if beats := binary.BigEndian.Uint32(payload[10:14]); beats != 4 {
		t.Fatalf("beats-till-next = %d, want 4", beats)
	}
	out, err := decodeBeatgrid(payload)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(out[0].BPM-120) > 0.01 || math.Abs(out[1].BPM-150) > 0.01 || math.Abs(out[1].PositionMs-2000) > 0.5 {
		t.Fatalf("round-trip drifted: %+v", out)
	}
	// Non-integral beat span refused.
	if _, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 0, BPM: 120}, {PositionMs: 2100, BPM: 150}}); err == nil {
		t.Fatal("non-integral segment not refused")
	}
}
