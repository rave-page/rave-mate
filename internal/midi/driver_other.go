//go:build !windows

package midi

// Non-Windows stub. A portable backend (e.g. ALSA/CoreMIDI via a vetted dependency) is a
// follow-up; until then the MIDI source reports unsupported and stays idle.

// Ports returns no ports.
func Ports() ([]string, error) { return nil, ErrUnsupported }

// Input is the stub input type.
type Input struct{ Name string }

// Open always fails on unsupported platforms.
func Open(string) (*Input, error) { return nil, ErrUnsupported }

// Messages returns a nil channel (never delivers).
func (in *Input) Messages() <-chan Message { return nil }

// Close is a no-op.
func (in *Input) Close() error { return nil }
