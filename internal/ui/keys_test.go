package ui

import (
	"testing"

	"rave.page/mate/internal/musiclib"
)

func TestPresentKeysDedupe(t *testing.T) {
	tracks := []musiclib.Track{
		{Key: "Am"}, {Key: "8A"}, // same key, two notations → one entry, count 2
		{Key: "C"}, {Key: "Ebm"}, {Key: "garbage"}, {Key: ""},
	}
	got := presentKeys(tracks)
	if len(got) != 3 {
		t.Fatalf("presentKeys = %v, want 3 distinct", got)
	}
	if am, _ := musiclib.ParseKey("8A"); got[am] != 2 {
		t.Errorf("8A count = %d, want 2", got[am])
	}
}

func TestKeyMatches(t *testing.T) {
	sel := map[string]bool{"8A · Am": true}
	if !keyMatches("Am", sel) || !keyMatches("8A", sel) {
		t.Error("Am/8A should match 8A filter")
	}
	if keyMatches("Em", sel) || keyMatches("", sel) || keyMatches("???", sel) {
		t.Error("non-8A keys must not match")
	}
	// empty filter passes everything, including unparsable
	if !keyMatches("???", map[string]bool{}) {
		t.Error("empty filter must pass all")
	}
}

func TestHarmonicSet(t *testing.T) {
	k, _ := musiclib.ParseKey("8A")
	sel := harmonicSet(k)
	for _, want := range []string{"8A · Am", "8B · C", "9A · Em", "7A · Dm"} {
		if !sel[want] {
			t.Errorf("harmonicSet(8A) missing %s", want)
		}
	}
	if len(sel) != 4 {
		t.Errorf("harmonicSet(8A) has %d keys, want 4 (%v)", len(sel), sel)
	}
}
