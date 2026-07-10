package seratolib

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"rave.page/mate/internal/musiclib"
)

// flacFile assembles a synthetic FLAC: fLaC + STREAMINFO + given extra blocks + fake frames.
func flacFile(t *testing.T, extra ...flacBlock) []byte {
	t.Helper()
	streaminfo := make([]byte, 34) // dummy STREAMINFO body (parser treats bodies opaquely)
	streaminfo[10] = 0x0A
	blocks := append([]flacBlock{{typ: 0, body: streaminfo}}, extra...)
	frames := []byte{0xFF, 0xF8, 0x69, 0x18, 0x00, 0x12, 0x34}
	out, err := renderFLAC(blocks, frames)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func vorbisBlock(vendor string, comments ...string) flacBlock {
	return flacBlock{typ: flacVorbisType, body: renderVorbis(vendor, comments)}
}

func TestFLACWriteRoundTrip(t *testing.T) {
	padding := flacBlock{typ: 1, body: make([]byte, 128)}
	orig := flacFile(t, vorbisBlock("ref v1", "ARTIST=Test", "TITLE=Song"), padding)
	want := []musiclib.GridMarker{{PositionMs: 320, BPM: 172}}
	payload, err := encodeBeatgrid(want)
	if err != nil {
		t.Fatal(err)
	}
	built, err := spliceFLACBeatgrid(orig, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySplice("x.flac", orig, built, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := readFLACBeatgrid(built)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	m, err := decodeBeatgrid(got)
	if err != nil || math.Abs(m[0].BPM-172) > 1e-4 || math.Abs(m[0].PositionMs-320) > 1e-2 {
		t.Fatalf("round-trip: %+v %v", m, err)
	}
	// Other comments + blocks + audio preserved.
	blocks, audioOff, err := parseFLAC(built)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(built[audioOff:], []byte{0xFF, 0xF8, 0x69, 0x18, 0x00, 0x12, 0x34}) {
		t.Fatal("audio changed")
	}
	if !bytes.Equal(blocks[2].body, padding.body) || blocks[2].typ != 1 {
		t.Fatal("padding block not byte-exact")
	}
	vendor, comments, err := vorbisComments(blocks[1].body)
	if err != nil || vendor != "ref v1" {
		t.Fatalf("vendor changed: %q %v", vendor, err)
	}
	if comments[0] != "ARTIST=Test" || comments[1] != "TITLE=Song" {
		t.Fatalf("comments reordered/lost: %v", comments)
	}
	// Serato base64 convention: no '=' padding, wrapped at 72 chars.
	sv := comments[2]
	if !strings.HasPrefix(sv, "SERATO_BEATGRID=") || strings.Contains(sv[len("SERATO_BEATGRID="):], "=") {
		t.Fatalf("base64 padding present: %q", sv)
	}
}

func TestFLACInsertWhenNoVorbisBlock(t *testing.T) {
	orig := flacFile(t) // STREAMINFO only
	payload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 0, BPM: 90}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := spliceFLACBeatgrid(orig, payload)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _, err := parseFLAC(built)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].typ != 0 || blocks[1].typ != flacVorbisType {
		t.Fatalf("vorbis block not inserted after STREAMINFO: %v", blocks)
	}
	if _, found, err := readFLACBeatgrid(built); err != nil || !found {
		t.Fatal(found, err)
	}
}

func TestFLACReplaceExisting(t *testing.T) {
	oldPayload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 100, BPM: 100}})
	if err != nil {
		t.Fatal(err)
	}
	orig := flacFile(t, vorbisBlock("v", "SERATO_BEATGRID="+encodeSeratoB64(beatgridDesc, oldPayload), "ARTIST=A"))
	newPayload, err := encodeBeatgrid([]musiclib.GridMarker{{PositionMs: 200, BPM: 133}})
	if err != nil {
		t.Fatal(err)
	}
	built, err := spliceFLACBeatgrid(orig, newPayload)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := readFLACBeatgrid(built)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	m, err := decodeBeatgrid(got)
	if err != nil || math.Abs(m[0].BPM-133) > 1e-4 {
		t.Fatalf("replace didn't take: %v %v", m, err)
	}
	blocks, _, err := parseFLAC(built)
	if err != nil {
		t.Fatal(err)
	}
	_, comments, err := vorbisComments(blocks[1].body)
	if err != nil || len(comments) != 2 {
		t.Fatalf("duplicate/lost comments: %v %v", comments, err)
	}
}

func TestSeratoB64Tolerance(t *testing.T) {
	payload := []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x43, 0x2E, 0x00, 0x00, 0x00}
	enc := encodeSeratoB64(beatgridDesc, payload)
	// Tolerate padding, linefeeds, trailing NULs and Serato's stray trailing char.
	for _, v := range []string{enc, enc + "\n", enc + "=", enc + "\x00", enc + "A"} {
		got, err := decodeSeratoB64(beatgridDesc, v)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("variant %q: %v % x", v[len(v)-3:], err, got)
		}
	}
}

func TestFLACNotAFlac(t *testing.T) {
	if _, err := spliceFLACBeatgrid([]byte("ID3xxxx"), []byte{0x01, 0x00}); err == nil {
		t.Fatal("non-FLAC not refused")
	}
	// Truncated block header refused.
	bad := []byte("fLaC")
	bad = append(bad, 0x00, 0x00, 0x00) // 3 bytes only
	if _, _, err := parseFLAC(bad); err == nil {
		t.Fatal("truncated header not refused")
	}
	// Block overrunning the file refused.
	bad2 := []byte("fLaC")
	bad2 = append(bad2, 0x80)
	bad2 = append(bad2, 0x00, 0x01, 0x00) // claims 256-byte body, none present
	if _, _, err := parseFLAC(bad2); err == nil {
		t.Fatal("overrun not refused")
	}
}
