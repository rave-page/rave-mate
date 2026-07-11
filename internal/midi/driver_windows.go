//go:build windows

package midi

import (
	"fmt"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// winmm MIDI-in bindings. We use the callback (CALLBACK_FUNCTION) variant so short messages
// arrive as MIM_DATA with the packed message in dwParam1 - the only reliable way to read
// CC streams. The callback runs on a winmm-owned thread; it only does a non-blocking channel
// send, so it never blocks the OS thread.
var (
	winmm             = syscall.NewLazyDLL("winmm.dll")
	procInGetNumDevs  = winmm.NewProc("midiInGetNumDevs")
	procInGetDevCapsW = winmm.NewProc("midiInGetDevCapsW")
	procInOpen        = winmm.NewProc("midiInOpen")
	procInStart       = winmm.NewProc("midiInStart")
	procInStop        = winmm.NewProc("midiInStop")
	procInReset       = winmm.NewProc("midiInReset")
	procInClose       = winmm.NewProc("midiInClose")
)

const (
	callbackFunction = 0x00030000
	mimData          = 0x3C3
	maxPName         = 32
)

type midiInCaps struct {
	wMid           uint16
	wPid           uint16
	vDriverVersion uint32
	szPname        [maxPName]uint16
	dwSupport      uint32
}

func deviceName(i uintptr) (string, bool) {
	var caps midiInCaps
	r, _, _ := procInGetDevCapsW.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps))
	if r != 0 {
		return "", false
	}
	return syscall.UTF16ToString(caps.szPname[:]), true
}

// Ports lists the names of available MIDI input ports.
func Ports() ([]string, error) {
	n, _, _ := procInGetNumDevs.Call()
	out := make([]string, 0, n)
	for i := range n {
		if name, ok := deviceName(i); ok {
			out = append(out, name)
		}
	}
	return out, nil
}

// Input is an open MIDI input port streaming messages on a buffered channel.
type Input struct {
	Name   string
	handle uintptr
	ch     chan Message
	closed atomic.Bool
	thru   atomic.Pointer[func(Message)] // synchronous THRU forward, run in the winmm callback; nil = off
	stop   func()                        // non-winmm backend teardown (driver IOCTL reader); nil = winmm
}

// Open opens the input port matching substr (case-insensitive): an exact name match
// wins over the first substring match, so "A 61" prefers the hardware device over a
// derived virtual port like "A 61 (rave-mate)". substr "" opens the first available
// port. The returned Input streams messages via Messages().
//
// A managed reserved port ("<Name> (rave-mate)") is INTERNAL in driver protocol v3 —
// hidden from winmm — so it resolves to a direct driver IOCTL reader first; winmm
// enumeration is the fallback (covers an older installed driver whose reserved
// ports are still winmm-visible).
func Open(substr string) (*Input, error) {
	if in, ok := tryOpenDriverInput(substr); ok {
		return in, nil
	}
	n, _, _ := procInGetNumDevs.Call()
	dev := -1
	name := ""
	want := strings.ToLower(substr)
	for i := range n {
		nm, ok := deviceName(i)
		if !ok {
			continue
		}
		lnm := strings.ToLower(nm)
		if lnm == want {
			dev, name = int(i), nm
			break
		}
		if dev < 0 && (want == "" || strings.Contains(lnm, want)) {
			dev, name = int(i), nm
			if want == "" {
				break
			}
		}
	}
	if dev < 0 {
		return nil, fmt.Errorf("midi: no input port matching %q", substr)
	}

	in := &Input{Name: name, ch: make(chan Message, 256)}
	// One trampoline per Input, capturing its channel. NewCallback trampolines are never
	// freed, so a handful of ports over a process lifetime is fine.
	cb := syscall.NewCallback(func(_ uintptr, wMsg uintptr, _ uintptr, p1 uintptr, _ uintptr) uintptr {
		if wMsg == mimData && !in.closed.Load() {
			m := Message{Status: byte(p1), Data1: byte(p1 >> 8), Data2: byte(p1 >> 16)}
			// THRU FIRST, on the winmm callback thread, before the decode/channel hop - the
			// lowest-latency controller→DJ-app path (no goroutine-scheduling delay). midiOutShortMsg
			// is non-blocking (driver-queued) and safe to call here for a *different* device (the
			// loopback output), which is the classic MIDI-thru pattern. Keep the forward trivial -
			// no allocations, no locks beyond Output's own - so the OS MIDI thread never stalls.
			if fn := in.thru.Load(); fn != nil {
				(*fn)(m)
			}
			select {
			case in.ch <- m:
			default: // drop if the consumer is behind - never block the MIDI thread
			}
		}
		return 0
	})

	var handle uintptr
	if r, _, _ := procInOpen.Call(uintptr(unsafe.Pointer(&handle)), uintptr(dev), cb, 0, callbackFunction); r != 0 {
		return nil, fmt.Errorf("midi: midiInOpen(%q) failed: mmresult=%d", name, r)
	}
	in.handle = handle
	if r, _, _ := procInStart.Call(handle); r != 0 {
		_, _, _ = procInClose.Call(handle) // best-effort cleanup; start error is what matters
		return nil, fmt.Errorf("midi: midiInStart failed: mmresult=%d", r)
	}
	return in, nil
}

// Messages returns the stream of incoming messages. The channel is never closed (the
// callback may fire briefly after Close); range over it via a ctx-bounded select instead.
func (in *Input) Messages() <-chan Message { return in.ch }

// SetThru installs a forward run synchronously in the MIDI-in callback for every incoming
// message, before it's queued to Messages() - the lowest-latency THRU (controller → DJ app).
// nil disables. Set once right after Open; a handful of messages in the microseconds before
// the store just take the normal channel path.
func (in *Input) SetThru(fn func(Message)) {
	if fn == nil {
		in.thru.Store(nil)
		return
	}
	in.thru.Store(&fn)
}

// Close stops and releases the port. After Close the callback drops any late messages.
func (in *Input) Close() error {
	if in.closed.Swap(true) {
		return nil
	}
	if in.stop != nil { // driver-backed reader: cancel the pended IOCTL_READ
		in.stop()
		return nil
	}
	// Stop → reset → close; ignore MMRESULTs (teardown is best-effort, nothing to recover).
	_, _, _ = procInStop.Call(in.handle)
	_, _, _ = procInReset.Call(in.handle)
	_, _, _ = procInClose.Call(in.handle)
	return nil
}
