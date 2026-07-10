package midi

// OutPort is a MIDI destination: a winmm Output or a teVirtualMIDI one-way port.
type OutPort interface {
	Send(status, data1, data2 byte)
	Close()
	PortName() string
}

const (
	// VirtualDJSentinel is the config/UI value selecting the built-in one-way virtual
	// port for THRU / DJ-bridge output (see virtual_windows.go for why it exists).
	VirtualDJSentinel = "@rave-mate:one-way-dj"
	// VirtualDJPortName is the port name DJ apps see for THRU/bridge data.
	VirtualDJPortName = "rave-mate"
	// VirtualMixerPortName is the one-way port name for the software MIDI test
	// controller (midiemit) - distinct so daemon + midi child never collide on create.
	VirtualMixerPortName = "rave-mate mixer"
)

// PortName implements OutPort.
func (o *Output) PortName() string { return o.Name }
