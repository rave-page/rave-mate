//go:build windows

package timecode

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// WinMM midiOut backend (stdlib syscall, no cgo). Sends MTC quarter-frames (short messages) +
// full-frame locates (long/SysEx messages) to a selectable MIDI output port - the user points it at
// a virtual loopback (loopMIDI / Windows MIDI Services) another app listens on.
var (
	procMidiOutGetNumDevs = winmm.NewProc("midiOutGetNumDevs")
	procMidiOutGetDevCaps = winmm.NewProc("midiOutGetDevCapsW")
	procMidiOutOpen       = winmm.NewProc("midiOutOpen")
	procMidiOutShortMsg   = winmm.NewProc("midiOutShortMsg")
	procMidiOutPrepare    = winmm.NewProc("midiOutPrepareHeader")
	procMidiOutLongMsg    = winmm.NewProc("midiOutLongMsg")
	procMidiOutUnprepare  = winmm.NewProc("midiOutUnprepareHeader")
	procMidiOutReset      = winmm.NewProc("midiOutReset")
	procMidiOutClose      = winmm.NewProc("midiOutClose")
)

const midiMapper = 0xFFFFFFFF

type midiOutCaps struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [maxProductName]uint16
	wTechnology    uint16
	wVoices        uint16
	wNotes         uint16
	wChannelMask   uint16
	dwSupport      uint32
}

type midiHdr struct {
	lpData          uintptr
	dwBufferLength  uint32
	dwBytesRecorded uint32
	dwUser          uintptr
	dwFlags         uint32
	lpNext          uintptr
	reserved        uintptr
	dwOffset        uint32
	dwReserved      [8]uintptr
}

func midiOutDeviceName(i uintptr) (string, bool) {
	var caps midiOutCaps
	r, _, _ := procMidiOutGetDevCaps.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
	if r != 0 {
		return "", false
	}
	return syscall.UTF16ToString(caps.szPname[:]), true
}

// MidiOutDevices lists the OS MIDI-output port names.
func MidiOutDevices() ([]string, error) {
	n, _, _ := procMidiOutGetNumDevs.Call()
	out := make([]string, 0, n)
	for i := uintptr(0); i < n; i++ {
		if name, ok := midiOutDeviceName(i); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

func resolveMidiOutID(substr string) uintptr {
	if strings.TrimSpace(substr) == "" {
		return midiMapper
	}
	want := strings.ToLower(substr)
	n, _, _ := procMidiOutGetNumDevs.Call()
	for i := uintptr(0); i < n; i++ {
		if name, ok := midiOutDeviceName(i); ok && strings.Contains(strings.ToLower(name), want) {
			return i
		}
	}
	return midiMapper
}

// midiOut is an open MIDI output port.
type midiOut struct {
	mu     sync.Mutex
	handle uintptr
}

// openMidiOut opens the MIDI output port matching deviceName (substring; "" = MIDI_MAPPER).
func openMidiOut(deviceName string) (*midiOut, error) {
	dev := resolveMidiOutID(deviceName)
	var handle uintptr
	if r, _, _ := procMidiOutOpen.Call(uintptr(unsafe.Pointer(&handle)), dev, 0, 0, 0); r != 0 {
		return nil, fmt.Errorf("timecode: midiOutOpen failed: mmresult=%d", r)
	}
	return &midiOut{handle: handle}, nil
}

// short sends a 1–3 byte channel/system message (status, data1, data2).
func (m *midiOut) short(status, data1, data2 byte) {
	dw := uintptr(status) | uintptr(data1)<<8 | uintptr(data2)<<16
	m.mu.Lock()
	_, _, _ = procMidiOutShortMsg.Call(m.handle, dw)
	m.mu.Unlock()
}

// long sends a SysEx (or other long) message. Prepares + unprepares its own header per call - MTC
// full-frames are infrequent (locate/start/stop) so the overhead is irrelevant.
func (m *midiOut) long(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	buf := append([]byte(nil), data...)
	hdr := midiHdr{lpData: uintptr(unsafe.Pointer(&buf[0])), dwBufferLength: uint32(len(buf))}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, _, _ := procMidiOutPrepare.Call(m.handle, uintptr(unsafe.Pointer(&hdr)), unsafe.Sizeof(hdr)); r != 0 {
		return fmt.Errorf("timecode: midiOutPrepareHeader failed: mmresult=%d", r)
	}
	if r, _, _ := procMidiOutLongMsg.Call(m.handle, uintptr(unsafe.Pointer(&hdr)), unsafe.Sizeof(hdr)); r != 0 {
		_, _, _ = procMidiOutUnprepare.Call(m.handle, uintptr(unsafe.Pointer(&hdr)), unsafe.Sizeof(hdr))
		return fmt.Errorf("timecode: midiOutLongMsg failed: mmresult=%d", r)
	}
	_, _, _ = procMidiOutUnprepare.Call(m.handle, uintptr(unsafe.Pointer(&hdr)), unsafe.Sizeof(hdr))
	return nil
}

// close resets + releases the port.
func (m *midiOut) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, _, _ = procMidiOutReset.Call(m.handle)
	_, _, _ = procMidiOutClose.Call(m.handle)
}
