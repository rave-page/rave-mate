package tagwrite

import (
	"os"
	"path/filepath"
	"testing"

	id3v2 "github.com/bogem/id3v2/v2"
)

func addForeignTXXX(t *testing.T, path string) {
	t.Helper()
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "SERATO_TEST", Value: "keepme"})
	if err := tag.Save(); err != nil {
		t.Fatal(err)
	}
	if err := tag.Close(); err != nil {
		t.Fatal(err)
	}
}

func hasForeignTXXX(t *testing.T, path string) bool {
	t.Helper()
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tag.Close() }()
	for _, f := range tag.GetFrames("TXXX") {
		if udf, ok := f.(id3v2.UserDefinedTextFrame); ok && udf.Description == "SERATO_TEST" && udf.Value == "keepme" {
			return true
		}
	}
	return false
}

func dropsRoundTrip(t *testing.T, path string) {
	t.Helper()
	if ds, err := ReadDrops(path); err != nil || ds != nil {
		t.Fatalf("pre-read: %v %v", ds, err)
	}
	want := []float64{61234.5, 183000}
	if err := WriteDrops(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadDrops(path)
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("read back: %v %v", got, err)
	}
	// other tags coexist untouched
	if err := Write(path, Tags{FieldBPM: "174"}); err != nil {
		t.Fatalf("sibling write: %v", err)
	}
	if got, _ = ReadDrops(path); len(got) != 2 {
		t.Fatalf("drops lost after sibling tag write: %v", got)
	}
	tags, _ := Read(path)
	if tags[FieldBPM] != "174" {
		t.Fatalf("bpm lost: %v", tags)
	}
	// remove
	if err := WriteDrops(path, nil); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got, _ = ReadDrops(path); got != nil {
		t.Fatalf("not removed: %v", got)
	}
}

func TestDropsMP3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	dropsRoundTrip(t, path)
}

func TestDropsFLAC(t *testing.T) {
	src, err := os.ReadFile("testdata/silence.flac")
	if err != nil {
		t.Skipf("no flac fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	dropsRoundTrip(t, path)
}

func TestDropsPreservesOtherTXXX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// simulate a foreign TXXX (e.g. serato/MIK) that must survive our writes
	addForeignTXXX(t, path)
	if err := WriteDrops(path, []float64{1000}); err != nil {
		t.Fatal(err)
	}
	if !hasForeignTXXX(t, path) {
		t.Fatal("foreign TXXX lost")
	}
	if err := WriteDrops(path, nil); err != nil {
		t.Fatal(err)
	}
	if !hasForeignTXXX(t, path) {
		t.Fatal("foreign TXXX lost on removal")
	}
}
