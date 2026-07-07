//go:build windows

package midi

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// winmm MIDI-out bindings (stdlib syscall, no cgo). Short messages only - enough for the
// CC/note streams our bridges emit. No MIDI_MAPPER fallback: an explicit (or first real)
// port keeps output off the GS software synth.
var (
	procOutGetNumDevs  = winmm.NewProc("midiOutGetNumDevs")
	procOutGetDevCapsW = winmm.NewProc("midiOutGetDevCapsW")
	procOutOpen        = winmm.NewProc("midiOutOpen")
	procOutShortMsg    = winmm.NewProc("midiOutShortMsg")
	procOutReset       = winmm.NewProc("midiOutReset")
	procOutClose       = winmm.NewProc("midiOutClose")
)

type midiOutCaps struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [maxPName]uint16
	wTechnology    uint16
	wVoices        uint16
	wNotes         uint16
	wChannelMask   uint16
	dwSupport      uint32
}

func outDeviceName(i uintptr) (string, bool) {
	var caps midiOutCaps
	r, _, _ := procOutGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
	if r != 0 {
		return "", false
	}
	return syscall.UTF16ToString(caps.szPname[:]), true
}

// OutPorts lists the names of available MIDI output ports.
func OutPorts() ([]string, error) {
	n, _, _ := procOutGetNumDevs.Call()
	out := make([]string, 0, n)
	for i := range n {
		if name, ok := outDeviceName(i); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// Output is an open MIDI output port.
type Output struct {
	Name   string
	mu     sync.Mutex
	handle uintptr
	closed bool
}

// OpenOutput opens the first output port whose name contains substr (case-insensitive);
// substr "" opens the first available port. Errors when no port matches.
func OpenOutput(substr string) (*Output, error) {
	n, _, _ := procOutGetNumDevs.Call()
	dev := -1
	name := ""
	want := strings.ToLower(strings.TrimSpace(substr))
	for i := range n {
		nm, ok := outDeviceName(i)
		if !ok {
			continue
		}
		if want == "" || strings.Contains(strings.ToLower(nm), want) {
			dev, name = int(i), nm
			break
		}
	}
	if dev < 0 {
		return nil, fmt.Errorf("midi: no output port matching %q (create one with loopMIDI)", substr)
	}
	var handle uintptr
	if r, _, _ := procOutOpen.Call(uintptr(unsafe.Pointer(&handle)), uintptr(dev), 0, 0, 0); r != 0 {
		return nil, fmt.Errorf("midi: midiOutOpen(%q) failed: mmresult=%d", name, r)
	}
	return &Output{Name: name, handle: handle}, nil
}

// Send emits a short message (status, data1, data2).
func (o *Output) Send(status, data1, data2 byte) {
	dw := uintptr(status) | uintptr(data1)<<8 | uintptr(data2)<<16
	o.mu.Lock()
	if !o.closed {
		_, _, _ = procOutShortMsg.Call(o.handle, dw)
	}
	o.mu.Unlock()
}

// Close resets + releases the port.
func (o *Output) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	_, _, _ = procOutReset.Call(o.handle)
	_, _, _ = procOutClose.Call(o.handle)
}
