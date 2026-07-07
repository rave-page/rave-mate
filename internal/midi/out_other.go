//go:build !windows

package midi

// Non-Windows output stub (see driver_other.go).

// OutPorts returns no ports.
func OutPorts() ([]string, error) { return nil, ErrUnsupported }

// Output is the stub output type.
type Output struct{ Name string }

// OpenOutput always fails on unsupported platforms.
func OpenOutput(string) (*Output, error) { return nil, ErrUnsupported }

// Send is a no-op.
func (o *Output) Send(status, data1, data2 byte) {}

// Close is a no-op.
func (o *Output) Close() {}
