// Package midi is a minimal MIDI abstraction with an OS-specific driver. On Windows it
// uses winmm.dll via syscall (no cgo, no third-party dependency - see driver_windows.go
// for input, out_windows.go for output); other platforms return ErrUnsupported until a
// portable driver lands. Only the small slice rave-mate needs - enumerate ports, open one
// by name substring, stream/send short messages - is implemented.
package midi

import (
	"errors"
	"fmt"
)

// ErrUnsupported is returned by the no-op driver on platforms without a MIDI backend.
var ErrUnsupported = errors.New("midi: not supported on this platform")

// Message is a 3-byte MIDI channel message (the only kind we consume).
type Message struct {
	Status byte
	Data1  byte
	Data2  byte
}

// IsCC reports a Control Change message (status nibble 0xB).
func (m Message) IsCC() bool { return m.Status&0xF0 == 0xB0 }

// Channel returns the 0-based MIDI channel (0..15).
func (m Message) Channel() int { return int(m.Status & 0x0F) }

// Controller returns the CC number (valid when IsCC).
func (m Message) Controller() byte { return m.Data1 }

// Value returns the CC value 0..127 (valid when IsCC).
func (m Message) Value() byte { return m.Data2 }

// IsSystem reports a System/real-time message (status 0xF0–0xFF) - clock, active-sensing,
// start/stop, SysEx, etc. These carry no per-deck data; a port that only ever shows these is
// effectively silent for our purposes (just Traktor's MIDI clock / keep-alive).
func (m Message) IsSystem() bool { return m.Status&0xF0 == 0xF0 }

// KindName names the message type for the monitor/debugger. For System messages (0xF0–0xFF)
// it decodes the specific real-time/common type so "System" isn't an opaque catch-all.
func (m Message) KindName() string {
	if m.Status&0xF0 == 0xF0 {
		switch m.Status {
		case 0xF0:
			return "SysEx"
		case 0xF1:
			return "MTC"
		case 0xF2:
			return "SongPos"
		case 0xF3:
			return "SongSel"
		case 0xF6:
			return "TuneReq"
		case 0xF8:
			return "Clock"
		case 0xFA:
			return "Start"
		case 0xFB:
			return "Continue"
		case 0xFC:
			return "Stop"
		case 0xFE:
			return "ActiveSense"
		case 0xFF:
			return "Reset"
		}
		return "System"
	}
	switch m.Status & 0xF0 {
	case 0x80:
		return "NoteOff"
	case 0x90:
		if m.Data2 == 0 {
			return "NoteOff" // note-on velocity 0 = note-off
		}
		return "NoteOn"
	case 0xA0:
		return "Aftertouch"
	case 0xB0:
		return "CC"
	case 0xC0:
		return "Program"
	case 0xD0:
		return "ChanPressure"
	case 0xE0:
		return "PitchBend"
	}
	return "?"
}

// Describe renders a one-line human-readable form for the MIDI monitor, e.g.
// "CC  ch1  #20 = 127" or "NoteOn  ch3  note 60 vel 100".
func (m Message) Describe() string {
	kind := m.KindName()
	ch := m.Channel() + 1 // 1-based for display
	switch m.Status & 0xF0 {
	case 0xB0:
		return fmt.Sprintf("%-12s ch%-2d #%-3d = %d", kind, ch, m.Data1, m.Data2)
	case 0x80, 0x90, 0xA0:
		return fmt.Sprintf("%-12s ch%-2d note %-3d vel %d", kind, ch, m.Data1, m.Data2)
	case 0xC0, 0xD0:
		return fmt.Sprintf("%-12s ch%-2d %d", kind, ch, m.Data1)
	case 0xE0:
		return fmt.Sprintf("%-12s ch%-2d %d", kind, ch, int(m.Data1)|int(m.Data2)<<7)
	default:
		return fmt.Sprintf("%-12s %02X %02X %02X", kind, m.Status, m.Data1, m.Data2)
	}
}
