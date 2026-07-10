//go:build !windows

package midi

// Non-Windows teVirtualMIDI stub (one-way virtual ports are Windows-only; other platforms
// have native virtual MIDI - ALSA seq / CoreMIDI - wired when those drivers land).

// VirtualAvailable reports false: no teVirtualMIDI driver here.
func VirtualAvailable() bool { return false }

// OneWayAvailable reports false: no one-way virtual-port backend here.
func OneWayAvailable() bool { return false }

// OpenOneWayOut always fails on unsupported platforms.
func OpenOneWayOut(string) (OutPort, error) { return nil, ErrUnsupported }

// VirtualOut is the stub one-way port type.
type VirtualOut struct{ name string }

// OpenVirtualOut always fails on unsupported platforms.
func OpenVirtualOut(string) (*VirtualOut, error) { return nil, ErrUnsupported }

// Send is a no-op.
func (v *VirtualOut) Send(status, data1, data2 byte) {}

// Close is a no-op.
func (v *VirtualOut) Close() {}

// PortName implements OutPort.
func (v *VirtualOut) PortName() string { return v.name }
