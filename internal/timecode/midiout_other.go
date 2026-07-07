//go:build !windows

package timecode

// Non-Windows MIDI stub. A portable backend (ALSA/CoreMIDI) is a follow-up; until then the MTC sink
// reports unsupported and stays idle.

// MidiOutDevices returns no ports.
func MidiOutDevices() ([]string, error) { return nil, ErrUnsupported }

// midiOut is the stub output type.
type midiOut struct{}

// openMidiOut always fails off Windows.
func openMidiOut(string) (*midiOut, error) { return nil, ErrUnsupported }

func (m *midiOut) short(byte, byte, byte) {}
func (m *midiOut) long([]byte) error      { return ErrUnsupported }
func (m *midiOut) close()                 {}
