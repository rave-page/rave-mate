// Package rekordboxmap generates a rekordbox MIDI-mapping CSV that makes rekordbox OUTPUT its
// per-deck play/cue state to a virtual MIDI port (e.g. LoopBe / loopMIDI) which rave-mate listens
// on - driving the now-playing overlay's "which deck is live" + cue. rekordbox stores MIDI maps as
// plain CSV imported once via Preferences → Controller → MIDI → IMPORT; this package only WRITES
// the file.
//
// WHY OUTPUT-ONLY PLAY/CUE: rekordbox's MIDI OUT is indicator/illumination only (buttons with an
// LED - Play, Cue, HotCue, Loop). Continuous controls (EQ, channel fader, volume, tempo) are
// INPUT-only in rekordbox - it never sends their positions out (verified against Pioneer's shipped
// DDJ mappings, where those rows have an empty MIDI-OUT column). So EQ/fader for the overlay must
// come from controller-eavesdrop (the app's MIDI source reading the controller directly), NOT here.
//
// CSV FORMAT (reverse-engineered from Pioneer's DDJ-400.midi.csv + a live 7.0.5/7.2.x export, and
// VERIFIED live - rekordbox emitted CC20 per deck on import): a `@file,1,<device>` header line,
// then one 15-column row per control:
//
//	0 Function  1 Name  2 Type  3 MIDI-IN  4 off  5 on  6 _ 7 _  8 MIDI-OUT  9 off 10 on  11 _ 12 _  13 Options  14 Desc
//
// A MIDI code is status<<8 | data1 as 4 upper-hex digits: CC on channel ch = (0xB0|ch)<<8|cc, e.g.
// deck-A Play (CC20, ch0) = B014, deck-B = B114. Deck targeting is by MIDI channel (deck N → ch
// N-1) - confirmed live (a 2-deck probe produced CC20 on ch1 AND ch2). Type "Indicator" =
// output-only (no MIDI-IN); rekordbox sends the on/off value to MIDI-OUT on state change.
//
// Parity with the app decoder (session/sources/midisrc.customCC): CC20→isPlaying (deck scope),
// CC28→cue (channel scope); booleans are on at any nonzero value, so on=127 reads as "playing".
package rekordboxmap

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"rave.page/mate/internal/config"
)

// DefaultDevice is the virtual MIDI port the mapping targets (what the user imports onto + what the
// app's MIDI source listens on). Override via ExportTo if the user's port is named differently.
const DefaultDevice = "LoopBe Internal MIDI"

// Control is one rekordbox MIDI-map row. For our output mapping these are Indicator rows: MidiIn
// empty, MidiOut set to the CC the app decodes.
type Control struct {
	Function string // rekordbox function id, e.g. "PlayPause", "HeadphoneCue"
	Name     string // display label in rekordbox's list
	Type     string // "Indicator" (output-only) | "Button" | "KnobSliderHiRes"
	MidiIn   string // 4-hex MIDI-in code ("" = output-only)
	MidiOut  string // 4-hex MIDI-out code ("" = input-only)
	Off, On  string // indicator off/on values (sent as the message's data byte)
	Options  string // e.g. "Fast;Priority=50"
	Desc     string
}

// Mapping is a device's ordered control set, emitted to CSV in slice order (deterministic).
type Mapping struct {
	Device   string
	Controls []Control
}

// numCols is rekordbox's CSV column count; named indices keep a re-seed localized.
const numCols = 15

const (
	colFunction = 0
	colName     = 1
	colType     = 2
	colMidiIn   = 3
	colInOff    = 4
	colInOn     = 5
	colMidiOut  = 8
	colOutOff   = 9
	colOutOn    = 10
	colOptions  = 13
	colDesc     = 14
)

// ccHex encodes a CC message: status (0xB0|channel) then CC number, 4 upper-hex digits.
func ccHex(ch, cc byte) string { return fmt.Sprintf("%02X%02X", 0xB0|ch, cc) }

// outputDefs is the per-deck control set rekordbox CAN output (indicator/LED state). cc must match
// session/sources/midisrc.customCC. EQ/fader are intentionally absent - rekordbox can't output them.
var outputDefs = []struct {
	cc        byte
	function  string
	nameFmt   string
	descField string
}{
	{20, "PlayPause", "Deck %d Play", "Play/Pause"},
	{28, "HeadphoneCue", "Deck %d Cue", "Headphone cue"},
}

// RavePageOutputMapping returns the rekordbox→rave-mate output mapping: Play + Cue per deck (8
// rows), each deck on its own MIDI channel (deck N → channel N-1), output as the CC the app decodes.
func RavePageOutputMapping(device string) Mapping {
	if device == "" {
		device = DefaultDevice
	}
	ctrls := make([]Control, 0, len(outputDefs)*4)
	for ch := byte(0); ch < 4; ch++ {
		deck := int(ch) + 1
		for _, d := range outputDefs {
			ctrls = append(ctrls, Control{
				Function: d.function,
				Name:     fmt.Sprintf(d.nameFmt, deck),
				Type:     "Indicator",
				MidiOut:  ccHex(ch, d.cc),
				Off:      "0",
				On:       "127",
				Options:  "Fast;Priority=50",
				Desc:     fmt.Sprintf("%s → CC%d ch%d (deck %s)", d.descField, d.cc, deck, string(rune('A'+ch))),
			})
		}
	}
	return Mapping{Device: device, Controls: ctrls}
}

// row renders a control into the fixed-width CSV row.
func (c Control) row() []string {
	r := make([]string, numCols)
	r[colFunction] = c.Function
	r[colName] = c.Name
	r[colType] = c.Type
	r[colMidiIn] = c.MidiIn
	r[colInOff] = c.Off
	r[colInOn] = c.On
	r[colMidiOut] = c.MidiOut
	r[colOutOff] = c.Off
	r[colOutOn] = c.On
	r[colOptions] = c.Options
	r[colDesc] = c.Desc
	return r
}

// WriteCSV emits the mapping as rekordbox CSV: a `@file,1,<device>` header then one row per
// control. UTF-8, no BOM, CRLF (matches Pioneer's exports). The csv writer quotes fields with
// commas (e.g. a description) - rekordbox tolerates standard CSV quoting.
func (m Mapping) WriteCSV(w io.Writer) error {
	bw := bufio.NewWriter(w)
	if _, err := fmt.Fprintf(bw, "@file,1,%s\r\n", m.Device); err != nil {
		return err
	}
	for _, c := range m.Controls {
		if err := writeRow(bw, c.row()); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// writeRow writes one CRLF-terminated CSV record (minimal quoting: only when a field needs it).
func writeRow(w *bufio.Writer, fields []string) error {
	for i, f := range fields {
		if i > 0 {
			if err := w.WriteByte(','); err != nil {
				return err
			}
		}
		if needsQuote(f) {
			if _, err := fmt.Fprintf(w, "%q", f); err != nil {
				return err
			}
		} else if _, err := w.WriteString(f); err != nil {
			return err
		}
	}
	_, err := w.WriteString("\r\n")
	return err
}

func needsQuote(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ',', '"', '\r', '\n':
			return true
		}
	}
	return false
}

// DefaultSettingsDir returns rekordbox's per-user settings dir: %APPDATA%\Pioneer\rekordbox on
// Windows. Errors on non-Windows (macOS path differs; Linux unsupported - re-seed before claiming).
func DefaultSettingsDir() (string, error) {
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("rekordbox settings dir only resolved on Windows (got %s)", runtime.GOOS)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Pioneer", "rekordbox"), nil
}

const defaultExportName = "RavePage-rekordbox.csv"

// Export writes the output mapping (default device) to path; empty path → defaultExportName in the
// rekordbox settings dir if resolvable, else the app config dir. Returns nothing extra - the caller
// already knows the path it passed.
func Export(path string) error { return ExportTo(path, DefaultDevice) }

// ExportTo writes the output mapping for a specific virtual-MIDI device name to path.
func ExportTo(path, device string) error {
	if path == "" {
		p, err := defaultExportPath()
		if err != nil {
			return err
		}
		path = p
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := RavePageOutputMapping(device).WriteCSV(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// defaultExportPath picks the CSV destination when none is given.
func defaultExportPath() (string, error) {
	if dir, err := DefaultSettingsDir(); err == nil && dir != "" {
		return filepath.Join(dir, defaultExportName), nil
	}
	return config.DataPath(defaultExportName)
}
