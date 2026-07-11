package seratolib

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func noSerato(t *testing.T) {
	t.Helper()
	orig := seratoRunning
	seratoRunning = func() bool { return false }
	t.Cleanup(func() { seratoRunning = orig })
}

func testM2Cues() []musiclib.CuePoint {
	return []musiclib.CuePoint{
		{Name: "Drop", Kind: musiclib.CueHot, StartMs: 61250, Hotcue: 2},
		{Name: "", Kind: musiclib.CuePlain, StartMs: 90000, Hotcue: -1}, // no Serato repr
		{Name: "Build", Kind: musiclib.CueLoop, StartMs: 120000, LenMs: 8000, Hotcue: -1},
		{Name: "Grid", Kind: musiclib.CueGrid, StartMs: 0, Hotcue: -1}, // grid writer's job
	}
}

func TestM2InnerRoundTrip(t *testing.T) {
	in := []m2Entry{
		{name: "COLOR", data: []byte{0x00, 0x99, 0xFF, 0x99}},
		{name: "CUE", data: encodeM2Cue(0, 1250, m2CueColors[0], "Intro")},
		{name: "LOOP", data: encodeM2Loop(1, 5000, 9000, "")},
		{name: "BPMLOCK", data: []byte{0x01}},
	}
	got, err := decodeM2Inner(encodeM2Inner(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("entries = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i].name != in[i].name || !bytes.Equal(got[i].data, in[i].data) {
			t.Errorf("entry %d mismatch: %+v vs %+v", i, got[i], in[i])
		}
	}
}

func TestM2EnvelopeRoundTrip(t *testing.T) {
	inner := encodeM2Inner([]m2Entry{{name: "CUE", data: encodeM2Cue(3, 42000, m2CueColors[3], "X")}})
	body := encodeM2Body(inner)
	if len(body) < m2MinBody {
		t.Fatalf("body %d bytes, want >= %d (NUL pad)", len(body), m2MinBody)
	}
	back, err := decodeM2Body(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, inner) {
		t.Fatal("envelope did not round-trip")
	}
}

func TestCuesToM2Entries(t *testing.T) {
	entries, skipped := cuesToM2Entries(testM2Cues())
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (memory cue)", skipped)
	}
	if len(entries) != 2 || entries[0].name != "CUE" || entries[1].name != "LOOP" {
		t.Fatalf("entries = %+v", entries)
	}
	cue, ok := decodeM2CuePoint(entries[0])
	if !ok || cue.Hotcue != 2 || cue.StartMs != 61250 || cue.Name != "Drop" {
		t.Errorf("cue = %+v", cue)
	}
	lp, ok := decodeM2CuePoint(entries[1])
	if !ok || lp.Hotcue != 0 || lp.StartMs != 120000 || lp.LenMs != 8000 || lp.Name != "Build" {
		t.Errorf("loop = %+v (index falls back to first free slot)", lp)
	}
	// pad colors follow the Serato per-slot palette
	if entries[0].data[7] != 0x00 || entries[0].data[8] != 0x00 || entries[0].data[9] != 0xCC {
		t.Errorf("cue color = % X, want slot-2 palette 0000CC", entries[0].data[7:10])
	}
}

func TestWriteCuesMP3(t *testing.T) {
	noSerato(t)
	oldM2 := encodeM2Body(encodeM2Inner([]m2Entry{
		{name: "COLOR", data: []byte{0x00, 0x99, 0xFF, 0x99}},
		{name: "CUE", data: encodeM2Cue(0, 111, m2CueColors[0], "Stale")},
		{name: "BPMLOCK", data: []byte{0x01}},
	}))
	tit := textFrame(t, 3, "TIT2", "Keep Me")
	legacy := geobFrame(t, 3, markersV1Desc, []byte{0x02, 0x05})
	orig := mp3File(t, tagBytes(t, 3, 0, 64, tit, geobFrame(t, 3, markers2Desc, oldM2), legacy))

	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	skipped, err := WriteCues(path, testM2Cues())
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}

	got, found, err := ReadCues(path)
	if err != nil || !found {
		t.Fatalf("read back: found=%v err=%v", found, err)
	}
	if len(got) != 2 {
		t.Fatalf("cues = %+v, want 2", got)
	}
	if got[0].Kind != musiclib.CueHot || got[0].Hotcue != 2 || got[0].StartMs != 61250 || got[0].Name != "Drop" {
		t.Errorf("cue = %+v", got[0])
	}
	if got[1].Kind != musiclib.CueLoop || got[1].LenMs != 8000 {
		t.Errorf("loop = %+v", got[1])
	}

	built, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// non-cue entries preserved, stale CUE gone
	body, found, err := readID3Geob(built, markers2Desc)
	if err != nil || !found {
		t.Fatalf("markers2 geob: found=%v err=%v", found, err)
	}
	inner, err := decodeM2Body(body)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := decodeM2Inner(inner)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, e := range entries {
		names[e.name]++
	}
	if names["COLOR"] != 1 || names["BPMLOCK"] != 1 || names["CUE"] != 1 || names["LOOP"] != 1 {
		t.Errorf("entry census = %v", names)
	}
	for _, e := range entries {
		if e.name == "CUE" && cutNUL(e.data[12:]) == "Stale" {
			t.Error("stale CUE entry survived")
		}
	}
	// legacy Markers_ dropped; TIT2 + audio untouched
	if _, found, _ := readID3Geob(built, markersV1Desc); found {
		t.Error("legacy Serato Markers_ GEOB survived (would shadow Markers2)")
	}
	bt, _, err := parseID3(built)
	if err != nil {
		t.Fatal(err)
	}
	foundTit := false
	for _, f := range bt.frames {
		if f.id == "TIT2" && bytes.Equal(f.raw, tit) {
			foundTit = true
		}
	}
	if !foundTit {
		t.Error("TIT2 no longer byte-identical")
	}
	if !bytes.HasSuffix(built, audio) {
		t.Error("audio region changed")
	}
}

func TestWriteCuesFLAC(t *testing.T) {
	noSerato(t)
	orig := flacFile(t, vorbisBlock("ref", "ARTIST=Test", "TITLE=Song"))
	path := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCues(path, testM2Cues()); err != nil {
		t.Fatal(err)
	}
	got, found, err := ReadCues(path)
	if err != nil || !found {
		t.Fatalf("read back: found=%v err=%v", found, err)
	}
	if len(got) != 2 || got[0].Hotcue != 2 || got[1].Kind != musiclib.CueLoop {
		t.Fatalf("cues = %+v", got)
	}
	// other comments + audio preserved
	built, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _, err := parseFLAC(built)
	if err != nil {
		t.Fatal(err)
	}
	var comments []string
	for _, b := range blocks {
		if b.typ == flacVorbisType {
			_, comments, err = vorbisComments(b.body)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	joined := ""
	for _, c := range comments {
		joined += c + "\n"
	}
	if !bytes.Contains([]byte(joined), []byte("ARTIST=Test")) || !bytes.Contains([]byte(joined), []byte("TITLE=Song")) {
		t.Errorf("comments mutated: %q", joined)
	}
}

func TestWriteCuesUnsupportedAndRunning(t *testing.T) {
	noSerato(t)
	path := filepath.Join(t.TempDir(), "track.wav")
	if err := os.WriteFile(path, []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCues(path, testM2Cues()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	seratoRunning = func() bool { return true }
	if _, err := WriteCues(path, testM2Cues()); !errors.Is(err, ErrSeratoRunning) {
		t.Fatalf("err = %v, want ErrSeratoRunning", err)
	}
}

func TestApplyCuesSerato(t *testing.T) {
	noSerato(t)
	dir := t.TempDir()
	seratoDir := filepath.Join(dir, "_Serato_")
	if err := os.MkdirAll(seratoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mp3Path := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(mp3Path, mp3File(t, tagBytes(t, 3, 0, 32, textFrame(t, 3, "TIT2", "A"))), 0o644); err != nil {
		t.Fatal(err)
	}
	m4aPath := filepath.Join(dir, "b.m4a") // unsupported - skipped, not a failure
	if err := os.WriteFile(m4aPath, []byte("ftyp"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ApplyCuesSerato(seratoDir, []musiclib.CueUpdate{
		{Path: mp3Path, Cues: testM2Cues()},
		{Path: m4aPath, Cues: testM2Cues()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("updated = %d, want 1", res.Updated)
	}
	got, found, err := ReadCues(mp3Path)
	if err != nil || !found || len(got) != 2 {
		t.Fatalf("read back: %+v found=%v err=%v", got, found, err)
	}
}
