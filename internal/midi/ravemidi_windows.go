//go:build windows

package midi

// ravemidi is rave-mate's OWN kernel-mode virtual MIDI driver (driver/ravemidi). When
// installed it is the PREFERRED one-way-port backend; the teVirtualMIDI DLL (loopMIDI's
// driver) stays as the fallback. Client = DeviceIoControl on \\.\RaveMidiCtl via stdlib
// syscall (no cgo). Protocol mirrors driver/ravemidi/ioctl.h byte-for-byte (pack(1);
// all-ULONG headers are naturally aligned, so manual little-endian layout is exact).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"syscall"
)

// raveMIDICtlPath is the driver's control-device path (created by driver/ravemidi).
const raveMIDICtlPath = `\\.\RaveMidiCtl`

// ioctl.h constants (RAVEMIDI_*).
const (
	raveMIDIProtocolVersion = 1
	raveMIDIMaxName         = 32 // WCHARs incl NUL
	raveMIDIDeviceType      = 0x8F63

	raveMIDIKindOutOnly = 0 // apps see an INPUT-only port (the LED-echo killer)

	fileReadData  = 1 // FILE_READ_DATA access bit in CTL_CODE
	fileWriteData = 2 // FILE_WRITE_DATA
)

// raveMIDICtl computes CTL_CODE(RAVEMIDI_DEVICE_TYPE, fn, METHOD_BUFFERED=0, access).
func raveMIDICtl(fn, access uint32) uint32 {
	return raveMIDIDeviceType<<16 | access<<14 | fn<<2
}

var (
	ioctlRaveMIDICreatePort  = raveMIDICtl(0x800, fileReadData|fileWriteData)
	ioctlRaveMIDIDestroyPort = raveMIDICtl(0x801, fileReadData|fileWriteData)
	ioctlRaveMIDIWrite       = raveMIDICtl(0x802, fileWriteData)
)

// errRaveMIDIUnavailable marks the ravemidi driver as not installed/running.
var errRaveMIDIUnavailable = errors.New("ravemidi driver not installed")

// openRaveMIDICtl opens the control device (admin-only SDDL in the driver - fails
// ERROR_ACCESS_DENIED unelevated, which the caller treats as unavailable).
func openRaveMIDICtl() (syscall.Handle, error) {
	p, err := syscall.UTF16PtrFromString(raveMIDICtlPath)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return syscall.InvalidHandle, fmt.Errorf("%w: %v", errRaveMIDIUnavailable, err)
	}
	return h, nil
}

// raveMIDIAvailable reports whether the ravemidi control device exists AND this process
// can open it (driver installed + running + sufficient rights). Safe for UI gating.
func raveMIDIAvailable() bool {
	h, err := openRaveMIDICtl()
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(h)
	return true
}

// RaveMIDIOut is a driver-backed one-way port: apps see an INPUT-only winmm port; only
// this handle can inject data. Port lifetime is bound to the control handle (the driver
// tears creator-owned ports down on handle cleanup), so a crash leaves nothing behind.
// Satisfies OutPort.
type RaveMIDIOut struct {
	name   string
	portID uint32
	mu     sync.Mutex
	h      syscall.Handle
	closed bool
}

// openRaveMIDIOut creates a one-way (apps-see-input-only) port via the ravemidi driver.
func openRaveMIDIOut(name string) (OutPort, error) {
	h, err := openRaveMIDICtl()
	if err != nil {
		return nil, err
	}
	// RAVEMIDI_CREATE_PORT_IN: ULONG Version, ULONG Kind, WCHAR Name[32].
	in := make([]byte, 8+2*raveMIDIMaxName)
	binary.LittleEndian.PutUint32(in[0:], raveMIDIProtocolVersion)
	binary.LittleEndian.PutUint32(in[4:], raveMIDIKindOutOnly)
	u16, err := syscall.UTF16FromString(name) // incl terminating NUL
	if err != nil {
		_ = syscall.CloseHandle(h)
		return nil, err
	}
	if len(u16) > raveMIDIMaxName { // winmm szPname caps at 31 chars + NUL
		u16 = u16[:raveMIDIMaxName]
		u16[raveMIDIMaxName-1] = 0
	}
	for i, c := range u16 {
		binary.LittleEndian.PutUint16(in[8+2*i:], c)
	}
	var out [4]byte // RAVEMIDI_CREATE_PORT_OUT: ULONG PortId
	var ret uint32
	if err := syscall.DeviceIoControl(h, ioctlRaveMIDICreatePort,
		&in[0], uint32(len(in)), &out[0], uint32(len(out)), &ret, nil); err != nil {
		_ = syscall.CloseHandle(h)
		return nil, fmt.Errorf("ravemidi: create port %q: %v", name, err)
	}
	if ret < 4 {
		_ = syscall.CloseHandle(h)
		return nil, fmt.Errorf("ravemidi: create port %q: short reply (%d bytes)", name, ret)
	}
	return &RaveMIDIOut{name: name, portID: binary.LittleEndian.Uint32(out[:]), h: h}, nil
}

// Send injects one short message, sized by status (mirrors VirtualOut.Send).
func (r *RaveMIDIOut) Send(status, data1, data2 byte) {
	n := 3
	switch {
	case status >= 0xF8 || status == 0xF6: // realtime / tune request
		n = 1
	case status&0xF0 == 0xC0 || status&0xF0 == 0xD0 || status == 0xF1 || status == 0xF3:
		n = 2
	}
	// RAVEMIDI_WRITE_IN: ULONG PortId, ULONG ByteCount, then raw MIDI bytes.
	var buf [11]byte
	binary.LittleEndian.PutUint32(buf[0:], r.portID)
	binary.LittleEndian.PutUint32(buf[4:], uint32(n))
	buf[8], buf[9], buf[10] = status, data1, data2
	var ret uint32
	r.mu.Lock()
	if !r.closed {
		_ = syscall.DeviceIoControl(r.h, ioctlRaveMIDIWrite,
			&buf[0], uint32(8+n), nil, 0, &ret, nil)
	}
	r.mu.Unlock()
}

// Close destroys the port + control handle (idempotent - THRU + bridge may share one
// instance). Explicit DESTROY_PORT keeps teardown deterministic; handle cleanup in the
// driver is the crash backstop.
func (r *RaveMIDIOut) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	var ref [4]byte // RAVEMIDI_PORT_REF: ULONG PortId
	binary.LittleEndian.PutUint32(ref[:], r.portID)
	var ret uint32
	_ = syscall.DeviceIoControl(r.h, ioctlRaveMIDIDestroyPort,
		&ref[0], uint32(len(ref)), nil, 0, &ret, nil)
	_ = syscall.CloseHandle(r.h)
}

// PortName implements OutPort.
func (r *RaveMIDIOut) PortName() string { return r.name }

// OneWayAvailable reports whether ANY one-way virtual-port backend is usable
// (ravemidi driver preferred, teVirtualMIDI fallback).
func OneWayAvailable() bool { return raveMIDIAvailable() || VirtualAvailable() }

// OpenOneWayOut opens a one-way port on the best available backend: ravemidi first,
// then teVirtualMIDI. Callers treat the result uniformly via OutPort.
func OpenOneWayOut(name string) (OutPort, error) {
	if raveMIDIAvailable() {
		if p, err := openRaveMIDIOut(name); err == nil {
			return p, nil
		}
		// driver present but create failed (name clash / port cap) - fall through
	}
	return OpenVirtualOut(name)
}
