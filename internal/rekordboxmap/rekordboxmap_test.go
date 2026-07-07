package rekordboxmap

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

func TestOutputMappingShape(t *testing.T) {
	m := RavePageOutputMapping("")
	if got, want := len(m.Controls), 8; got != want { // Play + Cue × 4 decks
		t.Fatalf("controls = %d, want %d", got, want)
	}
	if m.Device != DefaultDevice {
		t.Errorf("device = %q, want %q", m.Device, DefaultDevice)
	}
}

func TestWriteCSVHeaderColumnsAndKnownRows(t *testing.T) {
	var buf bytes.Buffer
	if err := RavePageOutputMapping("").WriteCSV(&buf); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "@file,1,"+DefaultDevice+"\r\n") {
		t.Fatalf("missing/incorrect @file header: %q", out[:min(40, len(out))])
	}
	if !strings.Contains(out, "\r\n") {
		t.Errorf("expected CRLF line endings")
	}
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := len(recs), 1+8; got != want { // header + 8 controls
		t.Fatalf("rows = %d, want %d", got, want)
	}
	for i, rec := range recs[1:] {
		if len(rec) != numCols {
			t.Fatalf("row %d has %d cols, want %d", i, len(rec), numCols)
		}
	}
	// First control = deck A Play: Indicator, no MIDI-IN, MIDI-OUT = CC20 ch0 = B014, on=127.
	first := recs[1]
	if first[colFunction] != "PlayPause" {
		t.Errorf("function = %q, want PlayPause", first[colFunction])
	}
	if first[colType] != "Indicator" {
		t.Errorf("type = %q, want Indicator", first[colType])
	}
	if first[colMidiIn] != "" {
		t.Errorf("MIDI-IN = %q, want empty (output-only)", first[colMidiIn])
	}
	if first[colMidiOut] != "B014" {
		t.Errorf("MIDI-OUT = %q, want B014", first[colMidiOut])
	}
	if first[colOutOn] != "127" {
		t.Errorf("on value = %q, want 127 (decodes as playing)", first[colOutOn])
	}
}

func TestCCHexPerDeck(t *testing.T) {
	// Play (CC20) across decks A..D: B014, B114, B214, B314.
	want := []string{"B014", "B114", "B214", "B314"}
	for ch := byte(0); ch < 4; ch++ {
		if got := ccHex(ch, 20); got != want[ch] {
			t.Errorf("ccHex(%d,20) = %q, want %q", ch, got, want[ch])
		}
	}
}
